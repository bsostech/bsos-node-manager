package server

import (
	"context"
	"testing"

	"besu-manager/backend/internal/alerting"
	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/auth"
)

// ValidThreshold: percentage metrics 0-100, others must not be negative; every default rule's
// threshold must pass.
func TestValidThresholdRange(t *testing.T) {
	cases := []struct {
		metric api.MetricId
		th     float32
		ok     bool
	}{
		{api.Cpu, 0, true}, {api.Cpu, 100, true}, {api.Cpu, 100.5, false}, {api.Disk, -1, false},
		{api.PeerCount, 0, true}, {api.Tps, 5000, true}, {api.PeerCount, -1, false},
	}
	for _, c := range cases {
		if got := ValidThreshold(c.metric, api.Gt, c.th) == nil; got != c.ok {
			t.Errorf("ValidThreshold(%s, %v) ok = %v, want %v", c.metric, c.th, got, c.ok)
		}
	}
	for _, r := range alerting.DefaultRules() {
		if r.Metric == "" {
			continue
		}
		if msg := ValidThreshold(api.MetricId(r.Metric), api.AlertRuleOperator(r.Operator), float32(r.Threshold)); msg != nil {
			t.Errorf("預設規則 %s 過不了驗證:%s", r.Name, msg.Message)
		}
	}
}

// ValidClear: separate reasons for a recovery threshold out of range and on the wrong side.
func TestValidClearReasons(t *testing.T) {
	over, high := float32(120), float32(95)
	if e := ValidClear(api.Cpu, api.Gt, 90, &over); e == nil || *e.Reason != api.ErrorReasonClearThresholdOutOfRange {
		t.Fatalf("恢復門檻 120 應 clear_threshold_out_of_range: %#v", e)
	}
	if e := ValidClear(api.Cpu, api.Gt, 90, &high); e == nil || *e.Reason != api.ErrorReasonClearThresholdWrongSide {
		t.Fatalf("> 90 觸發、恢復門檻 95 應 clear_threshold_wrong_side: %#v", e)
	}
	if e := ValidClear(api.Cpu, api.Gt, 90, nil); e != nil {
		t.Fatalf("沒帶恢復門檻應通過: %#v", e)
	}
}

// ValidClear: each of the four operators is tested on the recovery side, the trigger side and equal
// to the threshold; both ends of the range (0, percentage 100) must be accepted.
func TestValidClearSides(t *testing.T) {
	wrong := api.ErrorReasonClearThresholdWrongSide
	cases := []struct {
		metric api.MetricId
		op     api.AlertRuleOperator
		th, c  float32
		want   api.ErrorReason // "" = passes
	}{
		{api.Cpu, api.Gt, 50, 40, ""}, {api.Cpu, api.Gt, 50, 50, ""}, {api.Cpu, api.Gt, 50, 60, wrong},
		{api.Cpu, api.Gte, 50, 40, ""}, {api.Cpu, api.Gte, 50, 50, ""}, {api.Cpu, api.Gte, 50, 60, wrong},
		{api.Cpu, api.Lt, 50, 60, ""}, {api.Cpu, api.Lt, 50, 50, ""}, {api.Cpu, api.Lt, 50, 40, wrong},
		{api.Cpu, api.Lte, 50, 60, ""}, {api.Cpu, api.Lte, 50, 50, ""}, {api.Cpu, api.Lte, 50, 40, wrong},
		{api.Cpu, api.Gt, 50, 0, ""}, {api.Cpu, api.Lt, 50, 100, ""},
		{api.Cpu, api.Gt, 50, -1, api.ErrorReasonClearThresholdOutOfRange},
		{api.PeerCount, api.Lt, 50, 150, ""}, // non-percentage metrics have no upper limit of 100
	}
	for _, c := range cases {
		e := ValidClear(c.metric, c.op, c.th, &c.c)
		if (c.want == "" && e != nil) || (c.want != "" && (e == nil || *e.Reason != c.want)) {
			t.Errorf("%s %s %v、恢復 %v:得 %#v,want %q", c.metric, c.op, c.th, c.c, e, c.want)
		}
	}
}

// Rule edits answer with the saved rule: defaults are marked as such, only threshold rules carry
// metric/operator/threshold, and updatedBy names who made the change. A fixed-condition rule can
// still be renamed, re-graded and disabled; slow block production accepts the minimum multiplier.
// Restoring defaults answers with the restored rules.
func TestAlertRuleUpdateResponses(t *testing.T) {
	s, _ := accessTestServer(t)
	s.rulesSvc = alerting.NewRulesService(s.store, t.TempDir(), "")
	root := "root"
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{Username: &root, Name: "Alice Admin", Role: "admin"})
	if err := s.rulesSvc.SeedDefaults(ctx); err != nil {
		t.Fatal(err)
	}
	customRule(t, s, "mine", 70)
	rules := map[string]api.AlertRule{}
	list, err := s.ListAlertRules(ctx, api.ListAlertRulesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list.(api.ListAlertRules200JSONResponse) {
		rules[r.Name] = r
	}
	for _, d := range alerting.DefaultRules() {
		if r, ok := rules[d.Name]; !ok || !r.IsDefault {
			t.Fatalf("default rule %s: isDefault = false or missing", d.Name)
		}
	}
	if rules["mine"].IsDefault {
		t.Fatal("custom rule reported as default")
	}
	update := func(id string, b api.UpdateAlertRuleJSONRequestBody) api.UpdateAlertRuleResponseObject {
		t.Helper()
		r, err := s.UpdateAlertRule(ctx, api.UpdateAlertRuleRequestObject{Id: id, Body: &b})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	off, no := rules["NodeOffline"], false
	got, ok := update(off.Id, api.UpdateAlertRuleJSONRequestBody{Name: "offline", Enabled: &no,
		ForDuration: api.AlertRuleDurationN5m, Severity: api.AlertRuleUpdateRequestSeverityWarning}).(api.UpdateAlertRule200JSONResponse)
	if !ok || got.Name != "offline" || got.Enabled || got.Severity != api.AlertRuleSeverityWarning || !got.IsDefault || got.Metric != nil || got.UpdatedBy != "Alice Admin" {
		t.Fatalf("fixed-condition rule edit: %#v", got)
	}
	cpuMetric := api.Cpu
	if r, ok := update(off.Id, api.UpdateAlertRuleJSONRequestBody{Name: "offline", Metric: &cpuMetric,
		ForDuration: api.AlertRuleDurationN5m, Severity: api.AlertRuleUpdateRequestSeverityWarning}).(api.UpdateAlertRule400JSONResponse); !ok || !hasReason(api.Error(r), api.ErrorReasonRuleConditionFixed) {
		t.Fatalf("fixed-condition rule given a metric should be 400 rule_condition_fixed: %#v", r)
	}

	minMul := float32(alerting.ChainSlowMinThreshold)
	slow := rules["ChainSlow"]
	got, ok = update(slow.Id, api.UpdateAlertRuleJSONRequestBody{Name: slow.Name, Threshold: &minMul,
		ForDuration: api.AlertRuleDurationN1m, Severity: api.AlertRuleUpdateRequestSeverityWarning}).(api.UpdateAlertRule200JSONResponse)
	if !ok || got.Threshold == nil || *got.Threshold != minMul || got.Metric != nil || !got.IsDefault {
		t.Fatalf("ChainSlow at the minimum multiplier: %#v", got)
	}

	cpu, op, th := rules["NodeCpuHigh"], api.Gt, float32(80)
	got, ok = update(cpu.Id, api.UpdateAlertRuleJSONRequestBody{Name: cpu.Name, Metric: &cpuMetric, Operator: &op, Threshold: &th,
		ForDuration: api.AlertRuleDurationN5m, Severity: api.AlertRuleUpdateRequestSeverityWarning}).(api.UpdateAlertRule200JSONResponse)
	if !ok || got.Metric == nil || *got.Metric != api.Cpu || got.Operator == nil || *got.Operator != api.Gt || got.Threshold == nil || *got.Threshold != th {
		t.Fatalf("threshold rule edit: %#v", got)
	}

	rr, err := s.RestoreDefaultAlertRules(ctx, api.RestoreDefaultAlertRulesRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	restored, ok := rr.(api.RestoreDefaultAlertRules200JSONResponse)
	if !ok {
		t.Fatalf("restore: %#v", rr)
	}
	back := map[string]api.AlertRule{}
	for _, r := range restored {
		back[r.Name] = r
	}
	var cpuDefault float32
	for _, d := range alerting.DefaultRules() {
		if d.Name == "NodeCpuHigh" {
			cpuDefault = float32(d.Threshold)
		}
	}
	if c := back["NodeCpuHigh"]; c.Threshold == nil || *c.Threshold != cpuDefault {
		t.Fatalf("NodeCpuHigh not restored to %v: %#v", cpuDefault, c)
	}
	if o, ok := back["NodeOffline"]; !ok || !o.Enabled {
		t.Fatalf("NodeOffline not restored: %#v", back)
	}
}
