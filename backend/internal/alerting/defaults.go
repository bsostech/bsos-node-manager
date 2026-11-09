package alerting

import (
	"fmt"

	"github.com/google/uuid"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// Default rules: on every start the platform adds any that are missing from the rules table. After that they can be edited and disabled like user-created rules, but not deleted;
// "restore default rules" resets them to their default values. The code (preset) is the alert name when firing;
// the Alertmanager inhibit rule (PlatformDown -> NodeOffline in render.go) and existing notifications rely on that name.

// fixedRule: a default rule whose condition is hard-coded. Only duration and severity are editable (chain_slow also exposes a multiplier; see chainSlowExpr).
type fixedRule struct {
	kind        api.AlertRuleKind
	scope       string
	expr        func(r store.AlertRule) string
	summary     Text
	description Text
}

func constExpr(e string) func(store.AlertRule) string {
	return func(store.AlertRule) string { return e }
}

// chainSlowExpr: time since the last block / normal block time > multiplier. Time since the last block = the chain-head age at scrape time (scrape time minus head timestamp),
// taken from the most recent node of this organization. It does not subtract from time(): the scrape interval is 15s, so even a healthy chain is often older than 5 x 2s at evaluation.
// The normal block time and validator counts are computed by the platform (bsos_chain_*, server/metrics.go).
func chainSlowExpr(r store.AlertRule) string {
	return fmt.Sprintf(`min(timestamp(besu_blockchain_chain_head_timestamp{job="bsos-nodes"}) - besu_blockchain_chain_head_timestamp{job="bsos-nodes"}) / on () bsos_chain_block_period_seconds > %g`, r.Threshold)
}

// quorumLostExpr: more validators not producing than the fault tolerance, and the chain has stopped. ValidatorsMissing excludes the same condition via quorumLostUnless, so the two are exact complements.
const quorumLostExpr = `(bsos_chain_validators_not_producing > on () bsos_chain_fault_tolerance) and on () bsos_chain_halted == 1`

func quorumLostUnless(e string) string { return "(" + e + ") unless on () (" + quorumLostExpr + ")" }

// ChainSlowMinThreshold: lower bound for the chain_slow multiplier (below 1x even normal block production would count as slow).
const ChainSlowMinThreshold = 1

// unlessPlatformDown: when the platform cannot be scraped, every node's up drops to 0. Exclude that directly in the expression instead of relying on the for durations of PlatformDown and
// NodeOffline (both editable, both deletable) to give the Alertmanager inhibit rule time to take effect.
const unlessPlatformDown = ` unless on () up{job="bsos-platform"} == 0`

var fixedRules = map[string]fixedRule{
	// Platform down = no node can be scraped (metrics go through the platform proxy). The default receiver is the platform's own webhook,
	// which cannot receive while the platform is down, so this only has real-time value where SMTP or an external webhook is configured.
	"PlatformDown": {
		kind: api.AlertRuleKindPlatformDown, scope: "net",
		expr:    constExpr(`up{job="bsos-platform"} == 0`),
		summary: Text{"平台服務中斷,全體節點指標抓取停擺", "Platform down, no node metrics are being collected"},
		description: Text{
			"Prometheus 抓不到平台的 /metrics。節點指標經平台代理取得,平台不在等於所有節點都抓不到——此時的 NodeOffline 會被抑制,請先排除平台本身。",
			"Prometheus cannot scrape the platform's /metrics. Node metrics are collected through the platform, so while it is down no node can be scraped and NodeOffline is suppressed. Fix the platform first.",
		},
	},
	"NodeOffline": {
		kind: api.AlertRuleKindNodeOffline, scope: "net",
		expr:    constExpr(`up{job="bsos-nodes", source="host"} == 0` + unlessPlatformDown),
		summary: Text{"節點 {{ $labels.node }} 連線中斷", "Node {{ $labels.node }} disconnected"},
		description: Text{
			"Prometheus 抓不到 {{ $labels.node }} 的指標:agent 沒有連上平台,或 agent 已停止。",
			"Prometheus cannot scrape metrics from {{ $labels.node }}: the agent is not connected to the platform, or it has stopped.",
		},
	},
	// agent is up but Besu metrics cannot be scraped. When the agent is offline NodeOffline reports it; this rule does not report it again.
	"BesuMetricsDown": {
		kind: api.AlertRuleKindBesuMetricsDown, scope: "ledger",
		expr:    constExpr(`up{job="bsos-nodes", source="besu"} == 0 unless on (node) up{job="bsos-nodes", source="host"} == 0` + unlessPlatformDown),
		summary: Text{"節點 {{ $labels.node }} Besu metrics 抓不到", "Node {{ $labels.node }} Besu metrics unavailable"},
		description: Text{
			"agent 連得上,但抓不到 Besu 原生 metrics:Besu 沒有在跑,或 metrics-enabled 沒開、metrics 埠與平台記錄的不同。鏈指標此時都不會更新。",
			"The agent is reachable but Besu's own metrics cannot be scraped: Besu is not running, metrics-enabled is off, or the metrics port differs from the one the platform has on record. Chain metrics stop updating meanwhile.",
		},
	},
	// Chain-wide rules: one per chain, with no node label. The frontend groups them under "chain-wide" by alert name (frontend/src/lib/notifications.ts CHAIN_ALERTS).
	"ChainSlow": {
		kind: api.AlertRuleKindChainSlow, scope: "consensus", expr: chainSlowExpr,
		summary: Text{"出塊變慢", "Block production slowed"},
		description: Text{
			"距最後一塊的時間超過正常出塊時間的設定倍數。常見原因:有驗證者沒在出塊(每次輪到它都要等逾時才換人)、節點負載過高或網路延遲。",
			"Time since the last block exceeds the set multiple of the normal block time. Common causes: a validator is not producing (each of its turns waits for the timeout before moving on), high node load, or network latency.",
		},
	},
	"ChainSlowSevere": {
		kind: api.AlertRuleKindChainSlow, scope: "consensus", expr: chainSlowExpr,
		summary:     Text{"出塊嚴重變慢或停止", "Block production severely slowed or stopped"},
		description: Text{"可能有驗證者掉線,或驗證者之間失去共識。", "A validator may have gone offline, or the validators have lost consensus."},
	},
	"ValidatorsMissing": {
		kind: api.AlertRuleKindValidatorsMissing, scope: "consensus",
		// Everything where QuorumLost does not hold lands here: it also fires when more than the fault tolerance are missing but the chain still produces blocks, so there is no gap where neither fires
		expr:    constExpr(quorumLostUnless(`max(bsos_chain_validators_not_producing) > 0`)),
		summary: Text{"有 {{ $value }} 位驗證者未參與共識", "{{ $value }} validator(s) not taking part in consensus"},
		description: Text{
			"有驗證者沒在出塊,鏈還在出塊;再少幾位就會停鏈。到「全鏈健康狀態」看是哪幾位。",
			"Some validators are not producing blocks; the chain still is, but a few more and it stops. See Chain health for which ones.",
		},
	},
	"QuorumLost": {
		kind: api.AlertRuleKindQuorumLost, scope: "consensus",
		// Fires only when the chain has actually stopped: if it is still producing blocks the threshold is still met, even if the count of non-producing validators looks too high
		expr:    constExpr(quorumLostExpr),
		summary: Text{"驗證者不足,停止出塊({{ $value }} 位未參與)", "Not enough validators, block production stopped ({{ $value }} not taking part)"},
		description: Text{
			"沒在出塊的驗證者超過容錯數,湊不到出塊門檻,鏈已停止出塊。",
			"More validators are not producing than the fault tolerance allows, so the threshold for producing a block cannot be met. The chain has stopped producing blocks.",
		},
	},
}

// FixedRule: a fixed-condition default rule added by Enterprise via RegisterFixedRule.
// Kind is not in the Core contract's enum (the open-source edition knows nothing of Enterprise features); the frontend shows its condition text from Enterprise's locale files.
type FixedRule struct {
	Kind        api.AlertRuleKind
	Scope       string
	Expr        string
	Summary     Text
	Description Text
	Severity    string
	ForDuration string
}

// extraDefaults: initial values of registered default rules, appended after Core's default rules.
var extraDefaults []store.AlertRule

// RegisterFixedRule: adds a fixed-condition default rule. Call only at startup, before missing default rules are seeded; read-only afterwards.
// A duplicate code is a programming error and panics. Core never calls it, so open-source default rules are the same as if this function did not exist.
func RegisterFixedRule(preset string, r FixedRule) {
	if _, dup := fixedRules[preset]; dup {
		panic("alerting: duplicate default rule preset " + preset)
	}
	fixedRules[preset] = fixedRule{kind: r.Kind, scope: r.Scope, expr: constExpr(r.Expr), summary: r.Summary, description: r.Description}
	extraDefaults = append(extraDefaults, store.AlertRule{Name: preset, ForDuration: r.ForDuration, Severity: r.Severity})
}

// pct: value for percentage summaries. $value is the raw float (87.34523...), which email and Telegram print verbatim; round to one decimal, and drop .0 for whole numbers.
const pct = `{{ printf "%.1f" $value | reReplaceAll "\\.0$" "" }}%`

// Scope, summary and remediation hint for threshold default rules; the condition text is generated from the current threshold (users can change the threshold, not the metric).
var thresholdPresets = map[string]struct {
	scope         string
	summary, hint Text
}{
	"NodePeerCountLow": {"p2p", Text{"節點 {{ $labels.node }} peer 數過低({{ $value }})", "Node {{ $labels.node }} peer count low ({{ $value }})"},
		Text{"P2P 連線太少,有孤立風險。", "Too few P2P connections; the node risks being isolated."}},
	"NodeCpuHigh": {"hw", Text{"節點 {{ $labels.node }} CPU 使用率過高(" + pct + ")", "Node {{ $labels.node }} CPU usage high (" + pct + ")"}, Text{}},
	"NodeMemHigh": {"hw", Text{"節點 {{ $labels.node }} 記憶體使用率過高(" + pct + ")", "Node {{ $labels.node }} memory usage high (" + pct + ")"}, Text{}},
	"NodeDiskHigh": {"hw", Text{"節點 {{ $labels.node }} 磁碟使用率過高(" + pct + ")", "Node {{ $labels.node }} disk usage high (" + pct + ")"},
		Text{"鏈資料持續成長,建議及早擴容。", "Chain data keeps growing; plan to add disk space early."}},
	"NodeBehind": {"ledger", Text{"節點 {{ $labels.node }} 落後鏈頭 {{ $value }} 塊", "Node {{ $labels.node }} is {{ $value }} blocks behind the chain head"},
		Text{"節點追不上鏈頭:檢查網路、peer 數與主機負載;peer 正常卻一直落後時,重啟 Besu。", "The node cannot keep up with the chain head: check the network, peer count and host load. If peers look fine and it keeps falling behind, restart Besu."}},
	"NodeJvmHeapHigh": {"hw", Text{"節點 {{ $labels.node }} Besu 記憶體(Java 堆積)用了 " + pct, "Node {{ $labels.node }} Besu memory (Java heap) at " + pct},
		Text{"堆積快滿會頻繁 GC、出塊變慢,最後 OOM;考慮調高 Besu 的 -Xmx。", "A nearly full heap means frequent GC, slower block production and eventually OOM; consider raising Besu's -Xmx."}},
	"NodeTxpoolFull": {"ledger", Text{"節點 {{ $labels.node }} 交易池用了 " + pct, "Node {{ $labels.node }} transaction pool at " + pct},
		Text{"池滿後新交易會被拒;到交易池分頁看是不是有交易卡住(nonce 斷號、gas 太低),或調高 tx-pool-max-size。", "Once the pool is full, new transactions are rejected. Check the Tx pool tab for stuck transactions (nonce gap, gas too low), or raise tx-pool-max-size."}},
	"NodeTxpoolStale": {"ledger", Text{"節點 {{ $labels.node }} 交易池最舊一筆已等 {{ $value | " + zhDuration + " }}", "Node {{ $labels.node }} oldest pool transaction has waited {{ $value | humanizeDuration }}"},
		Text{"常見原因是 nonce 斷號或 gas 太低;到交易池分頁看是哪個帳戶。", "Usually a nonce gap or gas too low; check the Tx pool tab for the account."}},
}

// defaultKeepFiring: initial recovery wait for default rules.
const defaultKeepFiring = "2m"

// DefaultRules: initial values of the default rules, with fresh ids on every call (at startup only the missing ones are used).
// The clear threshold must be on the non-firing side of the firing threshold.
func DefaultRules() []store.AlertRule {
	fixed := func(preset, dur string) store.AlertRule {
		return store.AlertRule{Name: preset, ForDuration: dur, Severity: "critical"}
	}
	thr := func(preset, metric, op string, th, clear float64, dur string) store.AlertRule {
		return store.AlertRule{Name: preset, Metric: metric, Operator: op, Threshold: th, ClearThreshold: &clear, ForDuration: dur, Severity: "warning"}
	}
	rules := []store.AlertRule{
		fixed("PlatformDown", "1m"),
		fixed("NodeOffline", "2m"),
		fixed("BesuMetricsDown", "2m"),
		{Name: "ChainSlow", Threshold: 5, ForDuration: "1m", Severity: "warning"},
		{Name: "ChainSlowSevere", Threshold: 20, ForDuration: "1m", Severity: "critical"},
		{Name: "ValidatorsMissing", ForDuration: "2m", Severity: "warning"},
		fixed("QuorumLost", "1m"),
		thr("NodePeerCountLow", "peer_count", "lt", 2, 3, "5m"),
		thr("NodeCpuHigh", "cpu", "gt", 90, 85, "5m"),
		thr("NodeMemHigh", "mem", "gt", 90, 85, "5m"),
		thr("NodeDiskHigh", "disk", "gt", 85, 80, "10m"),
		thr("NodeJvmHeapHigh", "jvm_heap", "gt", 90, 85, "5m"),
		thr("NodeTxpoolFull", "txpool_usage", "gte", 80, 70, "5m"), // only the sequenced txpool has a limit
		thr("NodeTxpoolStale", "txpool_oldest", "gte", 60, 30, "5m"),
		{Name: "NodeBehind", Metric: "blocks_behind", Operator: "gte", Threshold: 5, ForDuration: "2m", Severity: "warning"},
	}
	rules = append(rules, extraDefaults...)
	for i := range rules {
		preset := rules[i].Name
		rules[i].ID = uuid.NewString()
		rules[i].Preset = &preset
		rules[i].Enabled = true
		rules[i].CreatedBy = "system"
		rules[i].KeepFiringFor = defaultKeepFiring
	}
	return rules
}

func fixedRuleOf(r store.AlertRule) (fixedRule, bool) {
	if r.Preset == nil {
		return fixedRule{}, false
	}
	f, ok := fixedRules[*r.Preset]
	return f, ok
}

// Kind: the rule's type, which decides which fields are editable.
func Kind(r store.AlertRule) api.AlertRuleKind {
	if f, ok := fixedRuleOf(r); ok {
		return f.kind
	}
	return api.AlertRuleKindThreshold
}

// Alertname: the alert name when firing. Default rules use their code; user-created rules use a stable id, so renaming does not make Alertmanager treat it as a new alert.
func Alertname(r store.AlertRule) string {
	if r.Preset != nil {
		return *r.Preset
	}
	return "custom_rule_" + r.ID
}
