package alerting

import (
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	// nosemgrep: go.lang.security.audit.xss.import-text-template.import-text-template -- the test simulates Prometheus expanding annotations (which uses text/template); no HTML is output
	texttemplate "text/template"

	"gopkg.in/yaml.v3"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

func strPtr(s string) *string { return &s }

func newFakeReloadServer(t *testing.T, status func() int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status())
	}))
}

func readRulesFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, customRulesFileName))
	if err != nil {
		t.Fatalf("read rules file: %v", err)
	}
	return string(b)
}

func TestRenderRulesFleetWideVsSingleNode(t *testing.T) {
	rules := []store.AlertRule{
		{ID: "r1", Name: "fleet-wide cpu", Metric: "cpu", Operator: "gt", Threshold: 80, ForDuration: "5m", Severity: "warning"},
		{ID: "r2", Name: "single node mem", Metric: "mem", NodeID: strPtr("node-a1"), Operator: "lt", Threshold: 10, ForDuration: "1m", Severity: "critical"},
	}
	b, err := renderRules(rules, api.NotifyLanguageZh)
	if err != nil {
		t.Fatal(err)
	}
	var f promRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("rendered yaml doesn't parse: %v\n%s", err, b)
	}
	if len(f.Groups) != 1 || len(f.Groups[0].Rules) != 2 {
		t.Fatalf("want 1 group with 2 rules, got %+v", f)
	}
	byID := map[string]promRule{}
	for _, r := range f.Groups[0].Rules {
		byID[r.Alert] = r
	}
	fleetWide := byID["custom_rule_r1"]
	if fleetWide.Expr != "bsos_node_cpu_percent > 80" {
		t.Errorf("fleet-wide expr should have no node selector, got %q", fleetWide.Expr)
	}
	singleNode := byID["custom_rule_r2"]
	if singleNode.Expr != `bsos_node_mem_percent{node="node-a1"} < 10` {
		t.Errorf("single-node expr should have node selector, got %q", singleNode.Expr)
	}
}

// alert: must be the stable identifier (custom_rule_<id>), not the user-entered name:
// renaming must not make Alertmanager treat it as a new alert, nor make the fingerprint in the alerts table change.
func TestRenderRulesAlertNameIsStableNotUserText(t *testing.T) {
	rules := []store.AlertRule{
		{ID: "abc-123", Name: "有空格 and 特殊字元 !@# 的顯示名稱", Metric: "cpu", Operator: "gt", Threshold: 1, ForDuration: "1m", Severity: "warning"},
	}
	b, err := renderRules(rules, api.NotifyLanguageZh)
	if err != nil {
		t.Fatal(err)
	}
	var f promRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	rule := f.Groups[0].Rules[0]
	if rule.Alert != "custom_rule_abc-123" {
		t.Errorf("alert name should be the stable id-derived name, got %q", rule.Alert)
	}
	if strings.Contains(rule.Alert, "顯示名稱") {
		t.Errorf("user's free-text name must never leak into alert:, got %q", rule.Alert)
	}
	if got := expandAnnotation(t, rule.Annotations["summary"]); got != "有空格 and 特殊字元 !@# 的顯示名稱" {
		t.Errorf("user's name should land in annotations.summary, got %q", got)
	}
}

// Notifications are for humans: the rule name goes in an annotation (not a label, so renaming does not restart a firing alert),
// and the condition is written with the metric's Chinese name and unit, without exposing PromQL.
func TestRenderRulesHumanReadableAnnotations(t *testing.T) {
	rules := []store.AlertRule{
		{ID: "r1", Name: "peer 偏低", Metric: "peer_count", NodeID: strPtr("node-a1"), Operator: "lt", Threshold: 10, ForDuration: "1m", Severity: "warning"},
		{ID: "r2", Name: "NodeCpuHigh", Preset: strPtr("NodeCpuHigh"), Metric: "cpu", Operator: "gt", Threshold: 90, ForDuration: "5m", Severity: "warning"},
		{ID: "r3", Name: "流量", Metric: "net_rx", Operator: "gt", Threshold: 1000000, ForDuration: "1h", Severity: "warning"},
	}
	b, err := renderRules(rules, api.NotifyLanguageZh)
	if err != nil {
		t.Fatal(err)
	}
	var f promRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"custom_rule_r1": "條件:已連線 Peers < 10,持續 1 分鐘。",
		"NodeCpuHigh":    "條件:CPU > 90%,持續 5 分鐘。",
		"custom_rule_r3": "條件:網路接收 > 1000000 B/s,持續 1 小時。",
	}
	for _, r := range f.Groups[0].Rules {
		if got := r.Annotations["description"]; got != want[r.Alert] {
			t.Errorf("%s description = %q, want %q", r.Alert, got, want[r.Alert])
		}
		if strings.Contains(r.Annotations["description"], "bsos_node_") {
			t.Errorf("%s description 露出 PromQL: %q", r.Alert, r.Annotations["description"])
		}
		name := map[string]string{"custom_rule_r1": "peer 偏低", "NodeCpuHigh": "NodeCpuHigh", "custom_rule_r3": "流量"}[r.Alert]
		if got := expandAnnotation(t, r.Annotations["rule_name"]); got != name {
			t.Errorf("%s annotations.rule_name 展開後 = %q, want %q", r.Alert, got, name)
		}
		if _, ok := r.Labels["rule_name"]; ok {
			t.Errorf("%s 規則名稱不得放進 labels(會改變告警身分)", r.Alert)
		}
	}
}

// Threshold rules carry the value at firing time (for the frontend to build the summary per language); fixed rules' expressions are mostly 0/1, so they carry none.
func TestRenderRulesValueAnnotation(t *testing.T) {
	b, err := renderRules(DefaultRules(), api.NotifyLanguageZh)
	if err != nil {
		t.Fatal(err)
	}
	var f promRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range f.Groups[0].Rules {
		_, fixed := fixedRules[r.Alert]
		v, has := r.Annotations["value"]
		if fixed && has {
			t.Errorf("%s 是固定規則,不該帶 value", r.Alert)
		}
		if !fixed {
			seen++
			if v != "{{ $value }}" {
				t.Errorf("%s 是門檻型規則,value = %q", r.Alert, v)
			}
		}
	}
	if seen == 0 {
		t.Fatal("預設規則裡沒有門檻型規則,測不到")
	}
}

// Rule names are user-entered and Prometheus expands annotations as templates: {{ ... }} in a name must appear verbatim, not be executed.
func TestRenderRulesUserTextIsNotTemplate(t *testing.T) {
	name := `{{ query "up" }} 與 }} 及 "引號"`
	b, err := renderRules([]store.AlertRule{{ID: "x", Name: name, Metric: "cpu", Operator: "gt", Threshold: 1, ForDuration: "1m", Severity: "warning"}}, api.NotifyLanguageZh)
	if err != nil {
		t.Fatal(err)
	}
	var f promRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"summary", "rule_name"} {
		if got := expandAnnotation(t, f.Groups[0].Rules[0].Annotations[k]); got != name {
			t.Errorf("%s 展開後 = %q, want 原文 %q", k, got, name)
		}
	}
}

// expandAnnotation: expands an annotation as text/template the way Prometheus does (without query and other functions:
// using one fails to parse, which is exactly how unwrapped user text gets caught).
func expandAnnotation(t *testing.T, s string) string {
	t.Helper()
	tmpl, err := texttemplate.New("a").Parse(s)
	if err != nil {
		t.Fatalf("annotation 不是合法模板(使用者文字沒包好?):%q: %v", s, err)
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, nil); err != nil {
		t.Fatalf("annotation 展開失敗:%q: %v", s, err)
	}
	return buf.String()
}

func TestRenderRulesSkipsUnknownMetricOperatorDuration(t *testing.T) {
	rules := []store.AlertRule{
		{ID: "bad-metric", Name: "x", Metric: "not_a_real_metric", Operator: "gt", Threshold: 1, ForDuration: "1m", Severity: "warning"},
		{ID: "bad-op", Name: "x", Metric: "cpu", Operator: "startswith", Threshold: 1, ForDuration: "1m", Severity: "warning"},
		{ID: "bad-duration", Name: "x", Metric: "cpu", Operator: "gt", Threshold: 1, ForDuration: "3 weeks", Severity: "warning"},
		{ID: "ok", Name: "x", Metric: "cpu", Operator: "gt", Threshold: 1, ForDuration: "1m", Severity: "warning"},
	}
	b, err := renderRules(rules, api.NotifyLanguageZh)
	if err != nil {
		t.Fatal(err)
	}
	var f promRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Groups[0].Rules) != 1 || f.Groups[0].Rules[0].Alert != "custom_rule_ok" {
		t.Fatalf("only the well-formed rule should render, got %+v", f.Groups[0].Rules)
	}
}

// RulesService.apply: when reload fails, write the old content back and reload again, leaving no half-applied state.
func TestApplyRollsBackOnReloadFailure(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	shouldFail := false // toggled mid-test to control whether this reload counts as successful
	srv := newFakeReloadServer(t, func() int {
		calls++
		if shouldFail {
			return 500
		}
		return 200
	})
	defer srv.Close()

	svc := &RulesService{rulesDir: dir, reloadURL: srv.URL}

	// First apply a "good" rule set and confirm it lands and reload succeeds (1 call).
	good := []store.AlertRule{{ID: "good", Name: "ok", Metric: "cpu", Operator: "gt", Threshold: 1, ForDuration: "1m", Severity: "warning"}}
	if err := svc.write(good, api.NotifyLanguageZh, true); err != nil {
		t.Fatalf("first apply (good rules, reload succeeds) should not error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("want 1 reload call for the successful apply, got %d", calls)
	}
	goodContent := readRulesFile(t, dir)
	if !strings.Contains(goodContent, "custom_rule_good") {
		t.Fatalf("expected good rule content, got %s", goodContent)
	}

	// Second apply: reload always fails (simulating Prometheus rejecting the new content).
	calls = 0
	shouldFail = true
	bad := []store.AlertRule{{ID: "bad", Name: "x", Metric: "cpu", Operator: "gt", Threshold: 2, ForDuration: "1m", Severity: "warning"}}
	err := svc.write(bad, api.NotifyLanguageZh, true)
	if err == nil {
		t.Fatal("apply should return the reload error, not swallow it")
	}
	if !strings.Contains(err.Error(), "reload rejected") {
		t.Errorf("error should surface Prometheus's rejection, got: %v", err)
	}
	// 1st: the reload after writing the new content (fails). 2nd: the retry reload after rolling back to the old content.
	if calls != 2 {
		t.Fatalf("want exactly 2 reload calls (fail then rollback-retry), got %d", calls)
	}
	afterContent := readRulesFile(t, dir)
	if afterContent != goodContent {
		t.Fatalf("file should be rolled back to the last-good content, got %s want %s", afterContent, goodContent)
	}
}

func TestRenderDefaultRules(t *testing.T) {
	b, err := renderRules(DefaultRules(), api.NotifyLanguageZh)
	if err != nil {
		t.Fatal(err)
	}
	var f promRuleFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("rendered yaml doesn't parse: %v\n%s", err, b)
	}
	got := map[string]promRule{}
	for _, r := range f.Groups[0].Rules {
		got[r.Alert] = r
	}
	if len(got) != 15 {
		t.Fatalf("want 15 default rules, got %d:\n%s", len(got), b)
	}
	// Alert names keep the codes from the original rules.yml: the inhibit rule and existing notifications rely on them.
	pd := got["PlatformDown"]
	if pd.Expr != `up{job="bsos-platform"} == 0` || pd.For != "1m" || pd.KeepFiringFor != "2m" {
		t.Errorf("PlatformDown rendered wrong: %+v", pd)
	}
	// Chain-wide rules: the multiplier goes into expr, not per node; the chain-height-unchanged rule was removed (ChainSlowSevere covers it).
	if _, ok := got["NodeChainStalled"]; ok {
		t.Error("NodeChainStalled should be gone")
	}
	for name, x := range map[string]string{"ChainSlow": "> 5", "ChainSlowSevere": "> 20"} {
		r := got[name]
		if !strings.HasPrefix(r.Expr, `min(timestamp(besu_blockchain_chain_head_timestamp{job="bsos-nodes"})`) || !strings.HasSuffix(r.Expr, "bsos_chain_block_period_seconds "+x) ||
			r.For != "1m" || r.Labels["scope"] != "consensus" {
			t.Errorf("%s rendered wrong: %+v", name, r)
		}
	}
	if vm := got["ValidatorsMissing"]; vm.Expr != `(max(bsos_chain_validators_not_producing) > 0) unless on () ((bsos_chain_validators_not_producing > on () bsos_chain_fault_tolerance) and on () bsos_chain_halted == 1)` || vm.For != "2m" || vm.Labels["severity"] != "warning" {
		t.Errorf("ValidatorsMissing rendered wrong: %+v", vm)
	}
	if ql := got["QuorumLost"]; ql.Expr != `(bsos_chain_validators_not_producing > on () bsos_chain_fault_tolerance) and on () bsos_chain_halted == 1` || ql.For != "1m" || ql.Labels["severity"] != "critical" {
		t.Errorf("QuorumLost rendered wrong: %+v", ql)
	}
	cpu := got["NodeCpuHigh"]
	if cpu.Expr != `(bsos_node_cpu_percent > 90) or (bsos_node_cpu_percent > 85 and on (node) ALERTS{alertname="NodeCpuHigh", alertstate="firing"})` ||
		cpu.For != "5m" || cpu.KeepFiringFor != "2m" || cpu.Labels["scope"] != "hw" || cpu.Labels["severity"] != "warning" {
		t.Errorf("NodeCpuHigh rendered wrong: %+v", cpu)
	}
	if !strings.Contains(cpu.Annotations["summary"], "CPU") {
		t.Errorf("preset summary should be the preset template, got %q", cpu.Annotations["summary"])
	}
}

func TestKindAndAlertname(t *testing.T) {
	for _, r := range DefaultRules() {
		if Alertname(r) != *r.Preset {
			t.Errorf("preset %s: alertname %q", *r.Preset, Alertname(r))
		}
		wantThreshold := r.Metric != ""
		if (Kind(r) == api.AlertRuleKindThreshold) != wantThreshold {
			t.Errorf("preset %s: kind %s", *r.Preset, Kind(r))
		}
	}
	user := store.AlertRule{ID: "x", Metric: "cpu"}
	if Kind(user) != api.AlertRuleKindThreshold || Alertname(user) != "custom_rule_x" {
		t.Errorf("user rule: kind %s alertname %s", Kind(user), Alertname(user))
	}
}

// A copy of the rendered default rules in each language is kept under deploy/ for promtool rule unit tests
// (rules_test.yml / rules_test_en.yml; CI: prom-rules.yml), which check the annotations as Prometheus expands them.
// After changing default rules, regenerate with UPDATE_GOLDEN=1 go test ./internal/alerting/ -run TestDefaultRulesGolden.
const goldenHeader = "# Generated from the default rules in backend/internal/alerting, for promtool tests only; Prometheus does not read this file.\n# Regenerate: cd backend && UPDATE_GOLDEN=1 go test ./internal/alerting/ -run TestDefaultRulesGolden\n"

func TestDefaultRulesGolden(t *testing.T) {
	for lang, file := range map[api.NotifyLanguage]string{api.NotifyLanguageZh: "rules-defaults.gen.yml", api.NotifyLanguageEn: "rules-defaults-en.gen.yml"} {
		b, err := renderRules(DefaultRules(), lang)
		if err != nil {
			t.Fatal(err)
		}
		want := goldenHeader + string(b)
		path := filepath.Join("..", "..", "..", "deploy", "platform", "docker-compose", file)
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s 跟預設規則不同步,重產:UPDATE_GOLDEN=1 go test ./internal/alerting/ -run TestDefaultRulesGolden", path)
		}
	}
}

// The rules file is written in the notification language: default rule summaries and descriptions, and the condition
// text of threshold rules. A rule name the user typed stays as typed.
func TestRenderRulesLanguage(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	user := store.AlertRule{ID: "u1", Name: "夜測 CPU", Metric: "cpu", Operator: "gt", Threshold: 90, ClearThreshold: f(85), ForDuration: "5m", Severity: "warning"}
	rules := append(DefaultRules(), user)
	han := regexp.MustCompile(`\p{Han}`)
	for _, c := range []struct {
		lang      api.NotifyLanguage
		offline   string
		condition string
	}{
		{api.NotifyLanguageZh, "節點 {{ $labels.node }} 連線中斷", "條件:CPU > 90% 觸發、≤ 85% 恢復,持續 5 分鐘。"},
		{api.NotifyLanguageEn, "Node {{ $labels.node }} disconnected", "Condition: CPU > 90% (clears at ≤ 85%), for 5 minutes."},
	} {
		b, err := renderRules(rules, c.lang)
		if err != nil {
			t.Fatal(err)
		}
		var pf promRuleFile
		if err := yaml.Unmarshal(b, &pf); err != nil {
			t.Fatal(err)
		}
		got := map[string]promRule{}
		for _, r := range pf.Groups[0].Rules {
			got[r.Alert] = r
		}
		if s := got["NodeOffline"].Annotations["summary"]; s != c.offline {
			t.Errorf("%s: NodeOffline summary = %q, want %q", c.lang, s, c.offline)
		}
		u := got["custom_rule_u1"]
		if u.Annotations["description"] != c.condition || u.Annotations["rule_name"] != `{{ "夜測 CPU" }}` {
			t.Errorf("%s: user rule annotations = %v", c.lang, u.Annotations)
		}
		if c.lang != api.NotifyLanguageEn {
			continue
		}
		// English: nothing Chinese anywhere except the name the user typed (rule_name, and the summary of a user rule).
		for _, r := range pf.Groups[0].Rules {
			for k, v := range r.Annotations {
				typed := k == "rule_name" || (k == "summary" && r.Alert == "custom_rule_u1")
				if !typed && han.MatchString(v) {
					t.Errorf("en: %s %s still has Chinese: %q", r.Alert, k, v)
				}
			}
		}
	}
}

// Prometheus not up yet at boot: the file must keep the new content and not roll back to an empty rule set.
func TestLoadPathKeepsFileWhenPrometheusDown(t *testing.T) {
	dir := t.TempDir()
	svc := &RulesService{rulesDir: dir, reloadURL: "http://127.0.0.1:1/-/reload"}
	if err := svc.write(DefaultRules(), api.NotifyLanguageZh, false); err == nil {
		t.Fatal("want reload error")
	}
	if got := readRulesFile(t, dir); !strings.Contains(got, "NodeOffline") {
		t.Fatalf("rules file should keep the rendered rules, got:\n%s", got)
	}
}

// RegisterFixedRule: registered rules are appended after the default rules, with Kind and condition as registered; a duplicate code panics.
func TestRegisterFixedRule(t *testing.T) {
	savedRules, savedExtra := fixedRules, extraDefaults
	fixedRules = maps.Clone(fixedRules)
	t.Cleanup(func() { fixedRules, extraDefaults = savedRules, savedExtra })

	RegisterFixedRule("TestExtra", FixedRule{Kind: api.AlertRuleKind("test_extra"), Scope: "security", Expr: `x == 1`, Summary: Text{"摘要", "sum-en"}, Description: Text{"說明", "desc-en"}, Severity: "warning", ForDuration: "5m"})
	rules := DefaultRules()
	last := rules[len(rules)-1]
	if *last.Preset != "TestExtra" || last.Severity != "warning" || last.ForDuration != "5m" || Kind(last) != "test_extra" {
		t.Fatalf("登記的預設規則: %+v kind=%s", last, Kind(last))
	}
	out, err := renderRules([]store.AlertRule{last}, api.NotifyLanguageZh)
	if err != nil || !strings.Contains(string(out), "alert: TestExtra") || !strings.Contains(string(out), "expr: x == 1") || !strings.Contains(string(out), "summary: 摘要") {
		t.Fatalf("渲染: %s %v", out, err)
	}
	if out, _ := renderRules([]store.AlertRule{last}, api.NotifyLanguageEn); !strings.Contains(string(out), "summary: sum-en") || !strings.Contains(string(out), "description: desc-en") {
		t.Fatalf("英文渲染: %s", out)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("代號重複應 panic")
		}
	}()
	RegisterFixedRule("NodeOffline", FixedRule{})
}

// Recovery wait and dual thresholds: 0s emits no keep_firing_for; no `or` when the clear threshold is unset or equals the firing threshold; all four operators compare the clear threshold using the firing operator.
func TestRenderKeepFiringAndClear(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		op    string
		clear *float64
		expr  string
	}{
		{"gt", f(85), `(bsos_node_cpu_percent > 90) or (bsos_node_cpu_percent > 85 and on (node) ALERTS{alertname="custom_rule_r", alertstate="firing"})`},
		{"gte", f(85), `(bsos_node_cpu_percent >= 90) or (bsos_node_cpu_percent >= 85 and on (node) ALERTS{alertname="custom_rule_r", alertstate="firing"})`},
		{"lt", f(95), `(bsos_node_cpu_percent < 90) or (bsos_node_cpu_percent < 95 and on (node) ALERTS{alertname="custom_rule_r", alertstate="firing"})`},
		{"lte", f(95), `(bsos_node_cpu_percent <= 90) or (bsos_node_cpu_percent <= 95 and on (node) ALERTS{alertname="custom_rule_r", alertstate="firing"})`},
		{"gt", nil, `bsos_node_cpu_percent > 90`},
		{"gt", f(90), `bsos_node_cpu_percent > 90`},
	} {
		r := store.AlertRule{ID: "r", Name: "x", Metric: "cpu", Operator: c.op, Threshold: 90, ClearThreshold: c.clear, ForDuration: "0s", KeepFiringFor: "0s", Severity: "warning"}
		b, err := renderRules([]store.AlertRule{r}, api.NotifyLanguageZh)
		if err != nil {
			t.Fatal(err)
		}
		var pf promRuleFile
		if err := yaml.Unmarshal(b, &pf); err != nil {
			t.Fatal(err)
		}
		pr := pf.Groups[0].Rules[0]
		if pr.Expr != c.expr || pr.For != "0s" || pr.KeepFiringFor != "" {
			t.Errorf("%s %v: %+v", c.op, c.clear, pr)
		}
	}
	r := store.AlertRule{ID: "r", Name: "x", Metric: "cpu", Operator: "gt", Threshold: 90, ClearThreshold: f(85), ForDuration: "0s", KeepFiringFor: "5m"}
	b, _ := renderRules([]store.AlertRule{r}, api.NotifyLanguageZh)
	if !strings.Contains(string(b), "keep_firing_for: 5m") || !strings.Contains(string(b), "條件:CPU > 90% 觸發、≤ 85% 恢復,立即觸發。") {
		t.Fatalf("恢復等待或條件文字不對:\n%s", b)
	}
}
