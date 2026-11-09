// Alert rules (default rules + user-created rules): structured form -> Prometheus rule group.
// Raw PromQL is not accepted (same reason NodeRuntimeParams does not take a raw config.toml: one broken expression
// makes Prometheus reject the whole reload, so not just that rule breaks; none of rule_files can be loaded).
package alerting

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/pki"
	"besu-manager/backend/internal/prom"
	"besu-manager/backend/internal/store"
)

const customRulesFileName = "custom-rules.yml"

// RulesService: separate from Service (the Alertmanager notification settings one): the target directory and reload endpoint differ,
// and rules contain no secrets, so no master key is needed.
type RulesService struct {
	store     *store.Store
	rulesDir  string // empty = disabled (e.g. deployments that manage Prometheus themselves and do not wire up this dynamic path)
	reloadURL string // empty = disabled
}

func NewRulesService(st *store.Store, rulesDir, reloadURL string) *RulesService {
	return &RulesService{store: st, rulesDir: rulesDir, reloadURL: reloadURL}
}

// LoadAndApply: runs once at boot so the file exists when Prometheus starts (even if the rule group is empty);
// rule changes go through SaveRule / RestoreDefaults, each of which applies first and persists afterwards.
func (s *RulesService) LoadAndApply(ctx context.Context) error {
	rules, err := s.store.ListEnabledAlertRules(ctx)
	if err != nil {
		return err
	}
	// Prometheus is often not up yet at boot, so the reload fails; do not roll back to the previous file here (on first boot that is an empty rule set).
	// Leave the file; Prometheus reads it when it comes up. Its content is rendered from the DB through the allowlist, so Prometheus has no reason to reject it.
	lang, err := s.language(ctx)
	if err != nil {
		return err
	}
	return s.write(rules, lang, false)
}

// ApplyLanguage: re-renders the enabled rules in lang (default rule summaries and condition text are annotations,
// fixed when the file is written). Called by Service.SaveLocale before it stores the new language.
func (s *RulesService) ApplyLanguage(ctx context.Context, lang api.NotifyLanguage) error {
	rules, err := s.store.ListEnabledAlertRules(ctx)
	if err != nil {
		return err
	}
	return s.write(rules, lang, true)
}

// language: the notification language stored with the alert settings.
// ponytail: read per apply; RulesService writes do not hold Service.saveMu, so a rule saved while the language is being
// changed can land in the old language until the next rule change or restart. Share the lock if that ever matters.
func (s *RulesService) language(ctx context.Context) (api.NotifyLanguage, error) {
	a, err := s.store.GetAlertSettings(ctx)
	if err != nil {
		return "", err
	}
	return languageOf(a.NotifyLanguage), nil
}

// SeedDefaults: adds missing default rules on every start (existing ones are untouched; see store.SeedDefaultAlertRules). Call before LoadAndApply.
func (s *RulesService) SeedDefaults(ctx context.Context) error {
	_, err := s.store.SeedDefaultAlertRules(ctx, DefaultRules())
	return err
}

// RestoreDefaults: resets all default rules to their default values and enables them. Like SaveRule, it applies the candidate rule set first and writes the DB only if Prometheus accepts it.
func (s *RulesService) RestoreDefaults(ctx context.Context, who string) error {
	all, err := s.store.ListAlertRules(ctx)
	if err != nil {
		return err
	}
	existingID := map[string]string{}
	var candidate []store.AlertRule
	for _, r := range all {
		if r.Preset != nil {
			existingID[*r.Preset] = r.ID
		} else if r.Enabled {
			candidate = append(candidate, r)
		}
	}
	defaults := DefaultRules()
	for i := range defaults {
		if id, ok := existingID[*defaults[i].Preset]; ok {
			defaults[i].ID = id
		}
	}
	if err := s.apply(ctx, append(candidate, defaults...)); err != nil {
		return err
	}
	return s.store.RestoreDefaultAlertRules(ctx, defaults, who)
}

// SaveRule: creates (isNew=true) or updates a rule. **It first trial-applies the candidate rule set "current DB + this change"
// to Prometheus and writes the DB only on success**, rather than "write the DB, try to apply, roll back on failure".
// Reason: there is no real transaction spanning Postgres and Prometheus here, and pretending there is would mislead people into
// assuming atomicity. Doing it the other way round (persist only after validation) still guarantees "any rule the DB says is valid is running in Prometheus",
// without inventing a cross-package transaction mechanism this project does not have. The one asymmetry: if the DB write fails after apply succeeds (very rare,
// usually meaning the DB itself is in trouble), Prometheus temporarily runs one extra rule the DB has no record of; the next rule change by anyone
// (or a service restart running LoadAndApply) converges it out of Prometheus automatically, same as the alertmanager.yml convergence logic.
func (s *RulesService) SaveRule(ctx context.Context, r store.AlertRule, isNew bool) (ok bool, err error) {
	existing, err := s.store.ListEnabledAlertRules(ctx)
	if err != nil {
		return false, err
	}
	if !isNew {
		found := false
		for i, e := range existing {
			if e.ID == r.ID {
				found = true
				existing = append(existing[:i], existing[i+1:]...)
				break
			}
		}
		if !found {
			// not in the enabled list: it may be a disabled rule; only the DB knows whether it exists
			cur, err := s.store.GetAlertRule(ctx, r.ID)
			if err != nil {
				return false, err
			}
			if cur == nil {
				return false, nil // 404: up to the caller
			}
		}
	}
	if r.Enabled {
		existing = append(existing, r)
	}
	// same order as LoadAndApply (ListEnabledAlertRules sorts by id), so the rules file does not flip-flop depending on which path wrote it
	sort.Slice(existing, func(i, j int) bool { return existing[i].ID < existing[j].ID })

	if err := s.apply(ctx, existing); err != nil {
		return false, err // Prometheus rejected it: the DB is untouched
	}

	if isNew {
		return true, s.store.InsertAlertRule(ctx, r)
	}
	return s.store.UpdateAlertRule(ctx, r)
}

// DeleteRule: same "validate first, then persist" order as SaveRule. Disabled rules are not rendered anyway, so they are deleted from the DB directly.
func (s *RulesService) DeleteRule(ctx context.Context, id string) (ok bool, err error) {
	existing, err := s.store.ListEnabledAlertRules(ctx)
	if err != nil {
		return false, err
	}
	for i, e := range existing {
		if e.ID == id {
			if err := s.apply(ctx, append(existing[:i], existing[i+1:]...)); err != nil {
				return false, err // Prometheus rejected it: the DB is untouched
			}
			break
		}
	}
	return s.store.DeleteAlertRule(ctx, id)
}

func (s *RulesService) apply(ctx context.Context, rules []store.AlertRule) error {
	lang, err := s.language(ctx)
	if err != nil {
		return err
	}
	return s.write(rules, lang, true)
}

// write: render, write the file, reload. With rollback=true a failed reload writes the previous file back (the rule-change path: the DB is not touched yet, and the file must match the DB).
func (s *RulesService) write(rules []store.AlertRule, lang api.NotifyLanguage, rollback bool) error {
	yamlBytes, err := renderRules(rules, lang)
	if err != nil {
		return err
	}
	if s.rulesDir == "" {
		return nil // disabled
	}

	path := filepath.Join(s.rulesDir, customRulesFileName)
	prevGood, readErr := os.ReadFile(path) // when missing, readErr != nil and prevGood is empty, treated as "previous version = empty rule group"

	if err := pki.WriteAtomic(s.rulesDir, customRulesFileName, yamlBytes, 0o644, -1); err != nil {
		return fmt.Errorf("write %s: %w", customRulesFileName, err)
	}
	if s.reloadURL == "" {
		return nil
	}
	if reloadErr := s.reload(); reloadErr != nil {
		if !rollback {
			return reloadErr
		}
		// Prometheus rejected the new rules: write back the last working content and reload again,
		// so Prometheus always stays on the last successfully applied config and never in a half-applied state.
		if readErr == nil {
			_ = pki.WriteAtomic(s.rulesDir, customRulesFileName, prevGood, 0o644, -1)
		} else {
			_ = pki.WriteAtomic(s.rulesDir, customRulesFileName, emptyRulesYAML(), 0o644, -1)
		}
		_ = s.reload() // best-effort: bring Prometheus back to the old config; if this fails too, do not retry recursively, to avoid an infinite loop
		return reloadErr
	}
	return nil
}

// reload: POST /-/reload. On a non-200 response the error body from Prometheus is wrapped into the error,
// so callers can show the real cause (usually a rule syntax problem) instead of a generic "internal".
func (s *RulesService) reload() error {
	req, err := http.NewRequest(http.MethodPost, s.reloadURL, nil)
	if err != nil {
		return err
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("call Prometheus reload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Prometheus reload rejected (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

// -- Rendering (allowlist-driven; arbitrary strings never reach expr) --

var operatorSymbols = map[string]string{"gt": ">", "lt": "<", "gte": ">=", "lte": "<="}

// durationText: allowed durations -> their wording in notifications. Also serves as the allowlist (shared by duration and recovery wait).
var durationText = map[string]Text{
	"0s": {"立即", "immediately"}, "1m": {"1 分鐘", "1 minute"}, "2m": {"2 分鐘", "2 minutes"}, "5m": {"5 分鐘", "5 minutes"},
	"10m": {"10 分鐘", "10 minutes"}, "30m": {"30 分鐘", "30 minutes"}, "1h": {"1 小時", "1 hour"},
}

// keepFiring: renders the recovery wait as keep_firing_for; 0s or values outside the allowlist produce nothing (= no wait).
func keepFiring(r store.AlertRule) string {
	if _, ok := durationText[r.KeepFiringFor]; !ok || r.KeepFiringFor == "0s" {
		return ""
	}
	return r.KeepFiringFor
}

type promRuleFile struct {
	Groups []promRuleGroup `yaml:"groups"`
}
type promRuleGroup struct {
	Name  string     `yaml:"name"`
	Rules []promRule `yaml:"rules"`
}
type promRule struct {
	Alert         string            `yaml:"alert"`
	Expr          string            `yaml:"expr"`
	For           string            `yaml:"for"`
	KeepFiringFor string            `yaml:"keep_firing_for,omitempty"`
	Labels        map[string]string `yaml:"labels"`
	Annotations   map[string]string `yaml:"annotations"`
}

// renderRules: annotations are rendered in lang; user-entered rule names stay as typed.
func renderRules(rules []store.AlertRule, lang api.NotifyLanguage) ([]byte, error) {
	group := promRuleGroup{Name: "custom"}
	for _, r := range rules {
		if _, ok := durationText[r.ForDuration]; !ok {
			continue // defensive: do not let one bad record block the other rules (the API layer should already have rejected it)
		}
		if f, ok := fixedRuleOf(r); ok {
			pr := promRule{
				Alert: *r.Preset, Expr: f.expr(r), For: r.ForDuration, KeepFiringFor: keepFiring(r),
				Labels:      map[string]string{"severity": r.Severity, "scope": f.scope},
				Annotations: map[string]string{"summary": f.summary.In(lang), "description": f.description.In(lang), "rule_name": tmplLiteral(r.Name)},
			}
			group.Rules = append(group.Rules, pr)
			continue
		}
		metricName, ok := prom.MetricName(api.MetricId(r.Metric))
		if !ok {
			continue
		}
		op, ok := operatorSymbols[r.Operator]
		if !ok {
			continue
		}
		selector := metricName
		if r.NodeID != nil && *r.NodeID != "" {
			// Security: this expression only applies to the own-node series served by /sd/targets (see backend/internal/server/sd.go);
			// this Prometheus scrapes only this organization's nodes, so fleet-wide mode (no node filter) cannot overreach.
			selector = fmt.Sprintf(`%s{node=%q}`, metricName, *r.NodeID)
		}
		expr := fmt.Sprintf("%s %s %g", selector, op, r.Threshold)
		if c := r.ClearThreshold; c != nil && *c != r.Threshold {
			// Dual thresholds: an alert that is already firing keeps firing until it crosses the clear threshold. ALERTS carries all of the alert's labels,
			// so node matches it back to the same machine; while pending during the for wait, only the firing threshold applies.
			expr = fmt.Sprintf(`(%s) or (%s %s %g and on (node) ALERTS{alertname=%q, alertstate="firing"})`, expr, selector, op, *c, Alertname(r))
		}
		scope, summary, hint := "custom", tmplLiteral(r.Name), ""
		if r.Preset != nil {
			if p, ok := thresholdPresets[*r.Preset]; ok {
				scope, summary, hint = p.scope, p.summary.In(lang), p.hint.In(lang)
			}
		}
		group.Rules = append(group.Rules, promRule{
			// alert: uses the stable identifier, not the user-entered name (see Alertname).
			Alert:         Alertname(r),
			Expr:          expr,
			For:           r.ForDuration,
			KeepFiringFor: keepFiring(r),
			Labels:        map[string]string{"severity": r.Severity, "scope": scope},
			// rule_name goes in an annotation, not a label: labels are part of alert identity, so renaming a rule would make a firing alert resolve and then fire again.
			// description is written for the people receiving notifications and contains no PromQL. value = the value at firing time, used by the frontend to build the summary in the UI language.
			Annotations: map[string]string{"summary": summary, "rule_name": tmplLiteral(r.Name), "description": joinSentences(conditionText(r, op, lang), hint, lang), "value": "{{ $value }}"},
		})
	}
	return yaml.Marshal(promRuleFile{Groups: []promRuleGroup{group}})
}

// joinSentences: Chinese sentences run on directly; English ones need a space between them.
func joinSentences(a, b string, lang api.NotifyLanguage) string {
	if b == "" || lang == api.NotifyLanguageZh {
		return a + b
	}
	return a + " " + b
}

// tmplLiteral: wraps user-entered text as a template string constant before it goes into an annotation. Prometheus expands annotations as Go templates,
// so an unwrapped {{ query "..." }} in a name would be executed; wrapped as {{ "..." }} it expands to the original text.
func tmplLiteral(s string) string {
	return "{{ " + strconv.Quote(s) + " }}"
}

// clearOps: the clear side is the inverse of the firing operator (fires at > 90 -> clears at <= 85).
var clearOps = map[string]string{">": "≤", ">=": "<", "<": "≥", "<=": ">"}

// conditionText: condition text for threshold rules, e.g. "Condition: CPU > 90% (clears at ≤ 85%), for 5 minutes." /
// "條件:CPU > 90% 觸發、≤ 85% 恢復,持續 5 分鐘。"; 0s is written as "fires immediately" / "立即觸發".
func conditionText(r store.AlertRule, op string, lang api.NotifyLanguage) string {
	label, unit := prom.MetricLabel(api.MetricId(r.Metric), lang)
	num := func(v float64) string {
		s := strconv.FormatFloat(v, 'f', -1, 64) // not %g: 1000000 would become 1e+06
		if unit != "" && unit != "%" {
			return s + " " + unit
		}
		return s + unit
	}
	cond := fmt.Sprintf("%s %s %s", label, op, num(r.Threshold))
	hasClear := r.ClearThreshold != nil && *r.ClearThreshold != r.Threshold
	if lang == api.NotifyLanguageZh {
		if hasClear {
			cond += fmt.Sprintf(" 觸發、%s %s 恢復", clearOps[op], num(*r.ClearThreshold))
		}
		if r.ForDuration == "0s" {
			return "條件:" + cond + ",立即觸發。"
		}
		return fmt.Sprintf("條件:%s,持續 %s。", cond, durationText[r.ForDuration].Zh)
	}
	if hasClear {
		cond += fmt.Sprintf(" (clears at %s %s)", clearOps[op], num(*r.ClearThreshold))
	}
	if r.ForDuration == "0s" {
		return "Condition: " + cond + ", fires immediately."
	}
	return fmt.Sprintf("Condition: %s, for %s.", cond, durationText[r.ForDuration].En)
}

func emptyRulesYAML() []byte {
	b, _ := yaml.Marshal(promRuleFile{Groups: []promRuleGroup{{Name: "custom"}}})
	return b
}
