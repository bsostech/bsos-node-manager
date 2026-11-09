// Alert rules: list (monitor:view), edit and restore defaults (nodes:onboard), enforced by each
// operation's security scope in the contract. Creating rules is an Enterprise feature; the validation
// and conversion functions are exported so its handler can share them.
// Structured CRUD; the actual render/reload logic is in internal/alerting.RulesService, this file
// only converts and calls.
package server

import (
	"context"
	"strconv"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/apierr"
	"besu-manager/backend/internal/auth"
	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/store"
)

func ToAPIAlertRule(r store.AlertRule) api.AlertRule {
	out := api.AlertRule{
		Id: r.ID, Kind: alerting.Kind(r), IsDefault: r.Preset != nil, Alertname: alerting.Alertname(r), Name: r.Name,
		ForDuration: api.AlertRuleDuration(r.ForDuration), KeepFiringFor: api.AlertRuleDuration(r.KeepFiringFor), Severity: api.AlertRuleSeverity(r.Severity),
		Enabled: r.Enabled, CreatedBy: r.CreatedBy, UpdatedBy: r.UpdatedBy,
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if out.Kind == api.AlertRuleKindChainSlow {
		th := float32(r.Threshold)
		out.Threshold = &th
	}
	if out.Kind == api.AlertRuleKindThreshold {
		metric, op, th := api.MetricId(r.Metric), api.AlertRuleOperator(r.Operator), float32(r.Threshold)
		out.Metric, out.Operator, out.Threshold, out.NodeId = &metric, &op, &th, r.NodeID
		if r.ClearThreshold != nil {
			c := float32(*r.ClearThreshold)
			out.ClearThreshold = &c
		}
	}
	return out
}

// AlertRuleApplyError: applying failed after a rule save / delete / restore (database, writing the
// rules file, Prometheus reload); the raw text is for operators (shared by Core and Enterprise).
func AlertRuleApplyError(err error) api.Error {
	return apierr.New(api.ErrorCodeInternal, api.ErrorReasonAlertRuleApplyFailed, map[string]string{"detail": err.Error()})
}

// RuleNotFoundError: the rule does not exist (shared by Core rule edits and Enterprise rule
// deletes).
var RuleNotFoundError = apierr.New(api.ErrorCodeNotFound, api.ErrorReasonRuleNotFound, nil)

// ValidRuleCommon checks the fields every rule has. The allowlist is enforced here (the render
// layer checks again defensively); the contract's enum rejects these first and they are unreachable
// over HTTP, so no reason is attached.
func ValidRuleCommon(dur api.AlertRuleDuration, sev string) *api.Error {
	if !dur.Valid() {
		return invalidMsg("unsupported duration")
	}
	if sev != "critical" && sev != "warning" {
		return invalidMsg("unsupported severity")
	}
	return nil
}

// ValidClear: the recovery threshold must be on the recovery side of the trigger threshold (> 90
// triggers -> recovery threshold <= 90), with the same range as the threshold.
func ValidClear(metric api.MetricId, op api.AlertRuleOperator, th float32, clear *float32) *api.Error {
	if clear == nil {
		return nil
	}
	c := *clear
	if c < 0 || (prom.IsPercent(metric) && c > 100) {
		return invalidReason(api.ErrorReasonClearThresholdOutOfRange, nil)
	}
	if (op == api.Gt || op == api.Gte) && c > th || (op == api.Lt || op == api.Lte) && c < th {
		return invalidReason(api.ErrorReasonClearThresholdWrongSide, nil)
	}
	return nil
}

// KeepFiringOr uses def when no recovery wait is given; a given one must be in the allowlist (the
// contract's enum rejects it first).
func KeepFiringOr(v *api.AlertRuleDuration, def string) (string, *api.Error) {
	if v == nil {
		return def, nil
	}
	if !v.Valid() {
		return "", invalidMsg("unsupported resolve wait")
	}
	return string(*v), nil
}

// ValidThreshold checks the condition fields of a threshold rule. Threshold range: percentage
// metrics (prom.IsPercent) 0-100, others must not be negative; thresholdRange in the frontend's
// lib/alertRules.ts enforces the same rule. A metric or comparison outside the allowlist is
// rejected first by the contract's enum.
func ValidThreshold(metric api.MetricId, op api.AlertRuleOperator, th float32) *api.Error {
	if !metric.Valid() {
		return invalidMsg("unsupported metric")
	}
	if !op.Valid() {
		return invalidMsg("unsupported comparison")
	}
	if th < 0 || (prom.IsPercent(metric) && th > 100) {
		return invalidReason(api.ErrorReasonThresholdOutOfRange, nil)
	}
	return nil
}

// PrincipalName is the identifier used in audit fields: the display name (Name is always set), not
// Username (which may be nil for SSO accounts).
func PrincipalName(ctx context.Context) string {
	if p := auth.PrincipalFrom(ctx); p != nil {
		return p.Name
	}
	return "unknown"
}

// ListAlertRules: GET /alert-rules.
func (s *Server) ListAlertRules(ctx context.Context, _ api.ListAlertRulesRequestObject) (api.ListAlertRulesResponseObject, error) {
	rules, err := s.store.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	resp := make(api.ListAlertRules200JSONResponse, 0, len(rules))
	for _, r := range rules {
		resp = append(resp, ToAPIAlertRule(r))
	}
	return resp, nil
}

// UpdateAlertRule: PUT /alert-rules/{id}. Full overwrite; fixed-condition default rules accept only
// duration, severity, name and enabled.
func (s *Server) UpdateAlertRule(ctx context.Context, req api.UpdateAlertRuleRequestObject) (api.UpdateAlertRuleResponseObject, error) {
	b := req.Body
	if b == nil {
		return api.UpdateAlertRule400JSONResponse{Code: api.ErrorCodeInvalid, Message: "missing body"}, nil // the contract's required check rejects this first; unreachable
	}
	cur, err := s.store.GetAlertRule(ctx, req.Id)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return api.UpdateAlertRule404JSONResponse(RuleNotFoundError), nil
	}
	if e := ValidRuleCommon(b.ForDuration, string(b.Severity)); e != nil {
		return api.UpdateAlertRule400JSONResponse(*e), nil
	}
	// Omitted recovery wait and recovery threshold = unchanged, so a caller that changes only the
	// threshold does not wipe them
	keep, e := KeepFiringOr(b.KeepFiringFor, cur.KeepFiringFor)
	if e != nil {
		return api.UpdateAlertRule400JSONResponse(*e), nil
	}
	enabled := true
	if b.Enabled != nil {
		enabled = *b.Enabled
	}
	r := *cur
	r.Name, r.ForDuration, r.Severity, r.Enabled, r.UpdatedBy, r.KeepFiringFor = b.Name, string(b.ForDuration), string(b.Severity), enabled, PrincipalName(ctx), keep
	switch alerting.Kind(*cur) {
	case api.AlertRuleKindThreshold:
		if b.Metric == nil || b.Operator == nil || b.Threshold == nil {
			return api.UpdateAlertRule400JSONResponse(*invalidReason(api.ErrorReasonThresholdRuleFieldsRequired, nil)), nil
		}
		if e := ValidThreshold(*b.Metric, *b.Operator, *b.Threshold); e != nil {
			return api.UpdateAlertRule400JSONResponse(*e), nil
		}
		// Threshold-type default rules only expose the threshold: the alert name and summary
		// describe the original metric (NodeCpuHigh must not turn into watching disk).
		if cur.Preset != nil && (string(*b.Metric) != cur.Metric || string(*b.Operator) != cur.Operator || b.NodeId != nil) {
			return api.UpdateAlertRule400JSONResponse(*invalidReason(api.ErrorReasonPresetRuleFieldsLocked, nil)), nil
		}
		r.Metric, r.Operator, r.Threshold, r.NodeID = string(*b.Metric), string(*b.Operator), float64(*b.Threshold), b.NodeId
		if b.ClearThreshold != nil {
			if e := ValidClear(*b.Metric, *b.Operator, *b.Threshold, b.ClearThreshold); e != nil {
				return api.UpdateAlertRule400JSONResponse(*e), nil
			}
			c := float64(*b.ClearThreshold)
			r.ClearThreshold = &c
		} else if c := r.ClearThreshold; c != nil && ValidClear(*b.Metric, *b.Operator, *b.Threshold, new(float32(*c))) != nil {
			r.ClearThreshold = nil // the new trigger threshold crosses the old recovery threshold: reset the recovery threshold to the trigger threshold
		}
	case api.AlertRuleKindChainSlow:
		// Only the multiplier is exposed: the page's "出塊變慢 / 嚴重變慢" (slow / severely slow block
		// production) also reads this value
		if b.Threshold == nil || b.Metric != nil || b.Operator != nil || b.NodeId != nil || b.ClearThreshold != nil {
			return api.UpdateAlertRule400JSONResponse(*invalidReason(api.ErrorReasonChainSlowFieldsLocked, nil)), nil
		}
		if *b.Threshold < alerting.ChainSlowMinThreshold {
			return api.UpdateAlertRule400JSONResponse(*invalidReason(api.ErrorReasonChainSlowThresholdMin, map[string]string{"min": strconv.Itoa(alerting.ChainSlowMinThreshold)})), nil
		}
		r.Threshold = float64(*b.Threshold)
	default:
		if b.Metric != nil || b.Operator != nil || b.Threshold != nil || b.NodeId != nil || b.ClearThreshold != nil {
			return api.UpdateAlertRule400JSONResponse(*invalidReason(api.ErrorReasonRuleConditionFixed, nil)), nil
		}
	}
	d := map[string]any{"rule_id": r.ID, "before": RuleAuditView(*cur), "after": RuleAuditView(r)}
	if err := s.audit(ctx, "alert_rule_updated", d); err != nil {
		return nil, err
	}
	ok, err := s.rulesSvc.SaveRule(ctx, r, false)
	if err != nil {
		e := AlertRuleApplyError(err)
		s.auditFailedError(ctx, "alert_rule_updated", d, e)
		return api.UpdateAlertRule500JSONResponse(e), nil
	}
	if !ok {
		s.auditFailedError(ctx, "alert_rule_updated", d, RuleNotFoundError)
		return api.UpdateAlertRule404JSONResponse(RuleNotFoundError), nil
	}
	saved, err := s.store.GetAlertRule(ctx, r.ID)
	if err != nil || saved == nil {
		return nil, err
	}
	return api.UpdateAlertRule200JSONResponse(ToAPIAlertRule(*saved)), nil
}

// RestoreDefaultAlertRules: POST /alert-rules/restore-defaults. Returns all rules.
func (s *Server) RestoreDefaultAlertRules(ctx context.Context, _ api.RestoreDefaultAlertRulesRequestObject) (api.RestoreDefaultAlertRulesResponseObject, error) {
	cur, err := s.store.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	before := make([]map[string]any, 0, len(cur))
	for _, r := range cur {
		v := RuleAuditView(r)
		v["rule_id"] = r.ID
		before = append(before, v)
	}
	d := map[string]any{"before": before} // restoring overwrites custom thresholds: record the whole set as it was before
	if err := s.audit(ctx, "alert_rules_restored", d); err != nil {
		return nil, err
	}
	if err := s.rulesSvc.RestoreDefaults(ctx, PrincipalName(ctx)); err != nil {
		e := AlertRuleApplyError(err)
		s.auditFailedError(ctx, "alert_rules_restored", d, e)
		return api.RestoreDefaultAlertRules500JSONResponse(e), nil
	}
	rules, err := s.store.ListAlertRules(ctx)
	if err != nil {
		return nil, err
	}
	resp := make(api.RestoreDefaultAlertRules200JSONResponse, 0, len(rules))
	for _, r := range rules {
		resp = append(resp, ToAPIAlertRule(r))
	}
	return resp, nil
}

// RuleAuditView is the rule content recorded in the audit (conditions and switches; creator and
// timestamps are not recorded).
func RuleAuditView(r store.AlertRule) map[string]any {
	return map[string]any{"name": r.Name, "metric": r.Metric, "node_id": r.NodeID, "operator": r.Operator,
		"threshold": r.Threshold, "clear_threshold": r.ClearThreshold, "for": r.ForDuration, "keep_firing_for": r.KeepFiringFor,
		"severity": r.Severity, "enabled": r.Enabled}
}
