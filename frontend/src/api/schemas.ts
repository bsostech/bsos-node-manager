/** Common aliases pulled from the types generated from the contract (types.ts is produced by `npm run gen:api`; do not edit by hand). */
import type { components } from './types';

export type NodeSummary = components['schemas']['NodeSummary'];
export type NodeDetail = components['schemas']['NodeDetail'];
export type NodeLogs = components['schemas']['NodeLogs'];
export type NodeLogLine = components['schemas']['NodeLogLine'];
export type NodeStatus = components['schemas']['NodeStatus'];
export type NodeCapabilities = components['schemas']['NodeCapabilities'];
export type AgentRuntime = components['schemas']['AgentRuntime'];
export type MetricId = components['schemas']['MetricId'];
export type MetricSeries = components['schemas']['MetricSeries'];
export type FleetNodeStatus = components['schemas']['FleetNodeStatus'];

/** Node certificate / connection status (in the contract, backend GET /nodes/{id}/cert). */
export type NodeCertStatus = components['schemas']['NodeCertStatus'];
export type PermissioningPolicy = components['schemas']['PermissioningPolicy'];

// ── Node RPC access (Node RPC Auth)──
export type RpcUser = components['schemas']['RpcUser'];
export type RpcUserCreate = components['schemas']['RpcUserCreate'];
export type RpcTokenRequest = components['schemas']['RpcTokenRequest'];
export type RpcTokenIssued = components['schemas']['RpcTokenIssued'];
export type RpcPublicKey = components['schemas']['RpcPublicKey'];
export type AgentBundleInfo = components['schemas']['AgentBundleInfo'];
export type RpcMethodCatalog = components['schemas']['RpcMethodCatalog'];
export type JoinToken = components['schemas']['JoinToken'];
export type PeerInfo = components['schemas']['PeerInfo'];
export type NodePeers = components['schemas']['NodePeers'];
export type NodeTxPool = components['schemas']['NodeTxPool'];
export type TxPoolSender = components['schemas']['TxPoolSender'];
export type TxPoolTx = components['schemas']['TxPoolTx'];
export type ManualPeer = components['schemas']['ManualPeer'];
export type SharedPeers = components['schemas']['SharedPeers'];
export type SharedPeer = components['schemas']['SharedPeer'];
export type LocalUser = components['schemas']['LocalUser'];
export type LocalUserCreated = components['schemas']['LocalUserCreated'];
export type PasswordResetResult = components['schemas']['PasswordResetResult'];
export type LocalUserCreateRequest = components['schemas']['LocalUserCreateRequest'];
export type AccountMailStatus = components['schemas']['AccountMailStatus'];
export type CustomPeer = components['schemas']['CustomPeer'];
export type NodeCustomPeers = components['schemas']['NodeCustomPeers'];
export type JoinTokenCreated = components['schemas']['JoinTokenCreated'];
export type JoinTokenTtl = components['schemas']['JoinTokenTtl'];
export type JoinTokenCreateRequest = components['schemas']['JoinTokenCreateRequest'];
export type ProposerMetric = components['schemas']['ProposerMetric'];
export type Role = components['schemas']['Role'];
export type Me = components['schemas']['Me'];
export type AuthConfig = components['schemas']['AuthConfig'];
export type LoginRequest = components['schemas']['LoginRequest'];
export type LoginEvent = components['schemas']['LoginEvent'];
export type LoginEventList = components['schemas']['LoginEventList'];
export type LoginFailReason = components['schemas']['LoginFailReason'];
export type PasswordChange = components['schemas']['PasswordChange'];

// ── Validator governance (on-chain validator set + voting through our own validators)──
export type ValidatorVotes = components['schemas']['ValidatorVotes'];
export type ValidatorVote = components['schemas']['ValidatorVote'];
export type ValidatorVoteResult = components['schemas']['ValidatorVoteResult'];
export type ValidatorVoteRecord = components['schemas']['ValidatorVoteRecord'];
export type ValidatorSetEntry = components['schemas']['ValidatorSetEntry'];
export type ValidatorTurn = components['schemas']['ValidatorTurn'];
export type ChainInfo = components['schemas']['ChainInfo'];
export type ChainNode = components['schemas']['ChainNode'];

// ── Alerting (Prometheus/Alertmanager)──
export type Alert = components['schemas']['Alert'];
export type AlertList = components['schemas']['AlertList'];
export type AlertStatus = components['schemas']['AlertStatus'];
export type AlertSettings = components['schemas']['AlertSettings'];
export type SmtpSettings = components['schemas']['SmtpSettings'];
export type SmtpSecurity = components['schemas']['SmtpSecurity'];
export type AlertWebhookUrl = components['schemas']['AlertWebhookUrl'];
export type TelegramSettings = components['schemas']['TelegramSettings'];
export type SlackSettings = components['schemas']['SlackSettings'];
export type AlertLocale = components['schemas']['AlertLocale'];
export type NotifyLanguage = components['schemas']['NotifyLanguage'];
export type AlertRule = components['schemas']['AlertRule'];
export type AlertRuleCreateRequest = components['schemas']['AlertRuleCreateRequest'];
export type AlertRuleUpdateRequest = components['schemas']['AlertRuleUpdateRequest'];
export type AlertRuleOperator = components['schemas']['AlertRuleOperator'];
export type AlertRuleDuration = components['schemas']['AlertRuleDuration'];

// Permissioning and access: responses for non-secret operations.
export type RpcUserEvent = components['schemas']['RpcUserEvent'];
export type PolicyApplyNodeResult = components['schemas']['PolicyApplyNodeResult'];
export type PermissioningSaveResult = components['schemas']['PermissioningSaveResult'];
export type NodeAllowlist = components['schemas']['NodeAllowlist'];
export type NodeLogLevelRequest = components['schemas']['NodeLogLevelRequest'];
export type SharedPeerNode = components['schemas']['SharedPeerNode'];
export type NodePortsRequest = components['schemas']['NodePortsRequest'];
export type NodeNameRequest = components['schemas']['NodeNameRequest'];
export type HourState = components['schemas']['HourState'];
