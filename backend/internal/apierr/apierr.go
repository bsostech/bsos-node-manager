// Package apierr: error responses that carry a reason. The message is produced from the reason's English template with params; the UI translates by reason into the user's language.
// auth, server and enterprise all use it, so it lives in its own package that depends only on generated types, avoiding import cycles.
package apierr

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"besu-manager/backend/internal/api"
)

// templates maps reason → English template; {name} is filled from params. Every value of the contract enum needs a template (checked by apierr_test).
var templates = map[api.ErrorReason]string{
	api.ErrorReasonFieldRequired:                 "{field} is required",
	api.ErrorReasonFieldNotAllowed:               "{field} is not an allowed value",
	api.ErrorReasonFieldFormat:                   "{field} has an invalid format",
	api.ErrorReasonFieldType:                     "{field} has the wrong type",
	api.ErrorReasonBodyNotJson:                   "Request body is not valid JSON",
	api.ErrorReasonBodyDecodeFailed:              "Request body is not valid JSON ({detail})",
	api.ErrorReasonContentTypeJson:               "Request must send Content-Type: application/json",
	api.ErrorReasonParamFormat:                   "Invalid parameter ({detail})",
	api.ErrorReasonEncodedSlash:                  "Path must not contain an encoded slash",
	api.ErrorReasonRouteNotFound:                 "API route not found",
	api.ErrorReasonMethodNotAllowed:              "This API does not support this method",
	api.ErrorReasonDbUnavailable:                 "Database temporarily unavailable, try again later",
	api.ErrorReasonLoginRequired:                 "Sign-in required",
	api.ErrorReasonPasswordChangeRequired:        "Change your password first",
	api.ErrorReasonPermissionDenied:              "Permission denied",
	api.ErrorReasonNotLoggedIn:                   "Not signed in",
	api.ErrorReasonNotLocalAccount:               "Not signed in, or not a local account",
	api.ErrorReasonLastAdmin:                     "This is the last active admin; it can’t be disabled, deleted or demoted",
	api.ErrorReasonUserNotFound:                  "No such local user",
	api.ErrorReasonBadCredentials:                "Wrong username or password",
	api.ErrorReasonNoRole:                        "No matching role",
	api.ErrorReasonWrongCurrentPassword:          "Current password is wrong",
	api.ErrorReasonPasswordTooShort:              "Password must be at least 12 characters",
	api.ErrorReasonPasswordTooLong:               "Password is too long (72 bytes at most)",
	api.ErrorReasonPasswordContainsUsername:      "Password must not contain the username",
	api.ErrorReasonPasswordCommon:                "This is a common password; choose another",
	api.ErrorReasonPasswordUnchanged:             "The new password must differ from the current one",
	api.ErrorReasonInvalidEmail:                  "Invalid email; enter the address only (e.g. alice@example.com)",
	api.ErrorReasonEmailRequiredToSend:           "An email is required to send the temporary password",
	api.ErrorReasonAccountHasNoEmail:             "This account has no email",
	api.ErrorReasonAccountMailNotConfigured:      "No password mail server configured (ACCOUNT_SMTP_*)",
	api.ErrorReasonUserExists:                    "Username already exists",
	api.ErrorReasonCannotDisableSelf:             "You can’t disable yourself",
	api.ErrorReasonCannotDeleteSelf:              "You can’t delete yourself",
	api.ErrorReasonCannotResetSelf:               "You can’t reset your own password",
	api.ErrorReasonEnodeScheme:                   `enode is missing the "enode://" prefix: {value}`,
	api.ErrorReasonEnodeNoHost:                   "enode is missing the host after '@': {value}",
	api.ErrorReasonEnodePubkeyLength:             "enode public key must be 128 hex characters, got {got}: {value}",
	api.ErrorReasonEnodePubkeyHex:                "enode public key is not valid hex: {value}",
	api.ErrorReasonEnodePubkeyInvalid:            "enode public key is not a valid secp256k1 key: {value}",
	api.ErrorReasonEnodeHostPort:                 "Must be ip:port ({detail})",
	api.ErrorReasonEnodeHostNotIp:                "Host must be an IP address (Besu doesn’t accept host names in an enode): {value}",
	api.ErrorReasonEnodeHostUnroutable:           "Host must be reachable from other nodes, not 0.0.0.0 / loopback / multicast: {value}",
	api.ErrorReasonEnodePortRange:                "Port must be 1–65535: {value}",
	api.ErrorReasonEnodeDiscport:                 "Only discport=<0-65535> is accepted after ? in an enode: {value}",
	api.ErrorReasonPeerIsSelf:                    "This is {nodeId} itself",
	api.ErrorReasonPeerInSharedList:              "Already in the shared list; this node connects to it already",
	api.ErrorReasonPeerListFull:                  "The list is full ({limit} entries)",
	api.ErrorReasonPeerExists:                    "This public key is already in the list",
	api.ErrorReasonPeerNotFound:                  "This public key is not in the list",
	api.ErrorReasonManualPeerListFull:            "The shared list is full ({limit} entries)",
	api.ErrorReasonManualPeerExists:              "This public key is already in the shared list",
	api.ErrorReasonManualPeerNotFound:            "This public key is not in the shared list",
	api.ErrorReasonNodeNotOwn:                    "No such node in this organization",
	api.ErrorReasonPolicyStale:                   "Someone else updated the list; refresh and try again",
	api.ErrorReasonAllowlistDisabled:             "Node {nodeId} doesn’t have the account allowlist enabled",
	api.ErrorReasonNodeUnreachable:               "Node {nodeId} can’t be reached right now",
	api.ErrorReasonAllowlistMismatch:             "Node {nodeId}’s list still differs after the write",
	api.ErrorReasonAllowlistNoExtras:             "This node has no extra addresses",
	api.ErrorReasonAlreadyValidator:              "This address is already a validator",
	api.ErrorReasonNotValidator:                  "This address isn’t a validator",
	api.ErrorReasonVoteNoConsensus:               "No node in this organization reports a consensus type (IBFT2 / QBFT), so votes can’t be sent",
	api.ErrorReasonNodeNotFound:                  "No such node",
	api.ErrorReasonLogLevelFailed:                "Couldn’t set the log level ({detail})",
	api.ErrorReasonNodeLogsFailed:                "Couldn’t read the node logs ({detail})",
	api.ErrorReasonTxpoolFailed:                  "Couldn’t read the transaction pool ({detail})",
	api.ErrorReasonTxpoolApiDisabled:             "The node doesn’t have the TXPOOL API enabled",
	api.ErrorReasonNameRequired:                  "Name is required ({limit} characters or fewer)",
	api.ErrorReasonPeerIsOwnNode:                 "This is one of this organization’s nodes; rename it in the node settings",
	api.ErrorReasonMonitoringNotConfigured:       "Prometheus isn’t configured (PROMETHEUS_URL)",
	api.ErrorReasonMonitoringDown:                "Monitoring source unreachable",
	api.ErrorReasonThresholdOutOfRange:           "Threshold out of range",
	api.ErrorReasonClearThresholdOutOfRange:      "Recovery threshold out of range",
	api.ErrorReasonClearThresholdWrongSide:       "The recovery threshold must be on the other side of the trigger threshold (for a greater-than rule it can’t be higher; for a less-than rule it can’t be lower)",
	api.ErrorReasonThresholdRuleFieldsRequired:   "A threshold rule needs metric, operator and threshold",
	api.ErrorReasonPresetRuleFieldsLocked:        "On a default rule you can only change the threshold, recovery threshold, duration, recovery wait and severity",
	api.ErrorReasonChainSlowFieldsLocked:         "On this rule you can only change the multiple, duration and severity",
	api.ErrorReasonChainSlowThresholdMin:         "The multiple must be at least {min}",
	api.ErrorReasonRuleConditionFixed:            "This rule’s condition is fixed; you can only change the duration and severity",
	api.ErrorReasonRuleNotFound:                  "No such rule",
	api.ErrorReasonPresetRuleUndeletable:         "Default rules can’t be deleted; disable them instead",
	api.ErrorReasonSmtpPasswordRequired:          "Password is required when a username is set",
	api.ErrorReasonWebhookNameRequired:           "Webhook {index} has no name",
	api.ErrorReasonWebhookUrlInvalid:             "Webhook {index}’s URL must be an absolute http(s) URL",
	api.ErrorReasonTelegramFieldsRequired:        "Bot token and chat id are both required",
	api.ErrorReasonTelegramThreadIdInvalid:       "The topic id must be a positive integer",
	api.ErrorReasonSlackUrlRequired:              "Slack webhook URL is required",
	api.ErrorReasonSlackUrlInvalid:               "The Slack webhook URL must be a full URL starting with https://hooks.slack.com/ or https://hooks.slack-gov.com/",
	api.ErrorReasonSmtpTestFailed:                "SMTP test failed ({detail})",
	api.ErrorReasonWebhookTestFailed:             "Webhook test failed ({detail})",
	api.ErrorReasonTelegramTestFailed:            "Telegram test failed ({detail})",
	api.ErrorReasonSlackTestFailed:               "Slack test failed ({detail})",
	api.ErrorReasonRpcUsernameReserved:           "Username can’t be empty; names starting with bsos- are reserved for the platform",
	api.ErrorReasonRpcUsernameFormat:             "Username may only use letters, digits, - and _",
	api.ErrorReasonRpcPermissionFormat:           "Invalid permission format: {value} (must be namespace:* or namespace:method)",
	api.ErrorReasonRpcPermissionUnknownNamespace: "Unknown namespace: {value}",
	api.ErrorReasonRpcPermissionUnknownMethod:    "Method not in the catalog: {value}",
	api.ErrorReasonRpcUserExists:                 "An RPC access identity with this name already exists",
	api.ErrorReasonRpcUserNotFound:               "No such RPC access identity",
	api.ErrorReasonRpcTokenTtlXor:                "Give exactly one of ttl and expiresAt",
	api.ErrorReasonRpcTokenExpiryPast:            "The expiry must be in the future",
	api.ErrorReasonRpcTokenExpiryTooFar:          "The expiry can be at most 1 year away",
	api.ErrorReasonAllowedSourceInvalid:          "The source IP allowlist takes one IP or CIDR per entry: {value}",
	api.ErrorReasonAllowedSourcesTooMany:         "The source IP allowlist can have at most {limit} entries",
	api.ErrorReasonJoinTokenNotFound:             "No such token",
	api.ErrorReasonJoinTokenNotRevocable:         "A join code that’s already used or revoked can’t be revoked",
	api.ErrorReasonJoinTokenExpired:              "The join code has expired",
	api.ErrorReasonJoinTokenUsed:                 "The join code has already been used",
	api.ErrorReasonJoinTokenRevoked:              "The join code has been revoked",
	api.ErrorReasonJoinSourceNotAllowed:          "The source IP isn’t in the join code’s allowlist",
	api.ErrorReasonJoinNodeExists:                "The node already exists; delete it first to onboard it again",
	api.ErrorReasonReEnrollNodeOnline:            "The node is still connected; re-enrollment is only for nodes that can’t reach the platform",
	api.ErrorReasonCertIssueFailed:               "Certificate issuance failed ({detail})",
	api.ErrorReasonBodyRequired:                  "Request body is required",
	api.ErrorReasonFieldSchemaMismatch:           "{field} doesn’t match the schema ({detail})",
	api.ErrorReasonBodySchemaMismatch:            "Request body doesn’t match the schema ({detail})",
	api.ErrorReasonCrossOrgDisabled:              "Cross-org read-only access isn’t enabled (CROSS_ORG_ENABLED)",
	api.ErrorReasonAccessKeyEndpointRequired:     "No usable default address; enter the read-only endpoint (host:port) the other org can actually reach",
	api.ErrorReasonAccessKeyEndpointFormat:       "The 8544 address must be host:port",
	api.ErrorReasonAccessKeyEndpointPort:         "The 8544 address’s port must be 1–65535",
	api.ErrorReasonAccessKeyNotFound:             "No such key",
	api.ErrorReasonAccessKeyAlreadyRevoked:       "Already revoked",
	api.ErrorReasonAccessKeyInactive:             "The key is no longer valid (revoked or expired)",
	api.ErrorReasonAccessKeyRevoked:              "The key has been revoked",
	api.ErrorReasonAccessKeyMalformed:            "The access key format is wrong (it should be one string starting with bsk1_)",
	api.ErrorReasonAccessKeyOwn:                  "This is a key issued by this organization",
	api.ErrorReasonExternalOrgExists:             "This organization has already been added; to change the key, delete it first and add it again",
	api.ErrorReasonExternalOrgNameTaken:          "The organization name is taken by another root: {org} already exists with fingerprint {existing}…, this key’s fingerprint is {incoming}…; check with the other org, delete the wrong one and add again",
	api.ErrorReasonRemoteFingerprintMismatch:     "The other side’s certificate doesn’t match the key (root fingerprint or organization name)",
	api.ErrorReasonRemoteKeyUnauthorized:         "The key is no longer valid (revoked or invalid)",
	api.ErrorReasonRemoteKeyExpired:              "The key has expired",
	api.ErrorReasonRemoteBadResponse:             "The other side responded abnormally",
	api.ErrorReasonRemoteForbidden:               "The other side hasn’t opened this data",
	api.ErrorReasonRemoteUnreachable:             "Can’t reach the read-only endpoint",
	api.ErrorReasonExternalOrgNotFound:           "No such external organization",
	api.ErrorReasonExternalNodeNotFound:          "No such external organization or node",
	api.ErrorReasonRemoteBadFormat:               "The other side’s response has the wrong format",
	api.ErrorReasonSsoLdapSettingsRequired:       "connector=ldap requires LDAP settings",
	api.ErrorReasonSsoOidcSettingsRequired:       "connector=oidc requires OIDC settings",
	api.ErrorReasonSsoConnectorInvalid:           "connector must be ldap or oidc",
	api.ErrorReasonSsoRedirectUrlRequired:        "OIDC requires a redirect URL (the platform’s public URL + /api/v1/auth/oidc/callback, matching what is registered at the IdP)",
	api.ErrorReasonSsoReenterSecret:              "The connection target changed; enter the password / client secret again",
	api.ErrorReasonSsoLdapSaveFailed:             "Couldn’t save the LDAP settings ({detail})",
	api.ErrorReasonSsoOidcConnectFailed:          "Couldn’t connect OIDC; IdP discovery unreachable or misconfigured ({detail})",
	api.ErrorReasonSsoLdapTestFailed:             "LDAP test failed ({detail})",
	api.ErrorReasonSsoOidcTestFailed:             "IdP discovery unreachable or misconfigured ({detail})",
	api.ErrorReasonSsoLockout:                    "Connect SSO before disabling local accounts",
	api.ErrorReasonRoleMappingGroupRequired:      "Group name is required",
	api.ErrorReasonRoleMappingNotFound:           "No such mapping",
	api.ErrorReasonOidcNotEnabled:                "OIDC sign-in is not enabled",
	api.ErrorReasonOidcFlowExpired:               "The sign-in flow timed out",
	api.ErrorReasonOidcStateMismatch:             "Sign-in couldn’t be verified",
	api.ErrorReasonOidcVerifyFailed:              "IdP verification failed",
	api.ErrorReasonRoleMappingChanged:            "The role mapping just changed",
	api.ErrorReasonTimeRangeOrder:                "from must be earlier than to",
	api.ErrorReasonTimeRangeTooLong:              "The time range can be at most {days} days",
	api.ErrorReasonCursorInvalid:                 "Invalid after cursor",
	api.ErrorReasonAlertRuleApplyFailed:          "Couldn’t apply the alert rules ({detail})",
	api.ErrorReasonAlertSettingsSaveFailed:       "Save failed ({detail})",
	api.ErrorReasonOperationFailed:               "Internal error ({detail})",
	api.ErrorReasonInternalError:                 "The server failed to handle the request; the cause is in the server log",
	api.ErrorReasonAuditUnavailable:              "Couldn’t write the audit log; the operation was not performed",
	api.ErrorReasonNodeRpcNoResponse:             "Node RPC didn’t respond ({detail})",
	api.ErrorReasonNodeRpcUnauthorized:           "Node RPC authentication failed (401)",
	api.ErrorReasonNodeRpcHttpStatus:             "Node RPC returned HTTP {status}",
	api.ErrorReasonNodeRpcBadResponse:            "Couldn’t parse the node RPC response ({detail})",
	api.ErrorReasonNodeRpcMissingResult:          "{method}: the node returned no result for this call",
	api.ErrorReasonNodeRpcError:                  "{method} returned error {code} ({message})",
	api.ErrorReasonConnResolveFailed:             "Could not resolve host {host} ({detail})",
	api.ErrorReasonConnAddressBlocked:            "Address {address} is in a loopback or link-local range; connection refused",
	api.ErrorReasonConnNoAddress:                 "No address found for {host}",
	api.ErrorReasonSmtpHandshakeFailed:           "SMTP handshake failed ({detail})",
	api.ErrorReasonSmtpEhloFailed:                "EHLO failed ({detail})",
	api.ErrorReasonSmtpAuthUnsupported:           "The SMTP server offers no login on this connection; if it only offers one after encryption, choose STARTTLS or TLS, otherwise leave the username empty",
	api.ErrorReasonSmtpAuthLoginRequired:         "The SMTP server lists PLAIN first, which needs encryption: choose STARTTLS or TLS",
	api.ErrorReasonSmtpCertUntrusted:             "Server certificate not trusted: for an internal CA, add its certificate in the deployment settings, or choose None",
	api.ErrorReasonSmtpCertNotInCa:               "Server certificate not trusted: its issuer is not among the internal CA certificates in the deployment settings",
	api.ErrorReasonSmtpStarttlsRequired:          "The SMTP server requires encryption before login: choose STARTTLS or TLS",
	api.ErrorReasonSmtpAuthFailed:                "Authentication failed ({detail})",
	api.ErrorReasonWebhookTestUrlInvalid:         "Enter a full URL starting with http:// or https://",
	api.ErrorReasonTlsHandshakeFailed:            "TLS handshake failed ({detail})",
	api.ErrorReasonTelegramTokenFormat:           "Bot token format is invalid",
	api.ErrorReasonTelegramUnreachable:           "Could not reach Telegram at {url} ({detail})",
	api.ErrorReasonTelegramTokenInvalid:          "Bot token is invalid; check it with BotFather",
	api.ErrorReasonTelegramForbidden:             "The bot is not allowed to post in this chat; make sure it has been added to the group ({description})",
	api.ErrorReasonTelegramChatNotFound:          "Chat ID not found; check the chat ID and that the bot is in the group",
	api.ErrorReasonTelegramThreadNotFound:        "Topic ID not found ({description})",
	api.ErrorReasonTelegramHttpStatus:            "Telegram returned {status} ({description})",
	api.ErrorReasonSlackUnreachable:              "Could not reach Slack ({detail})",
	api.ErrorReasonSlackHttpStatus:               "Slack returned {status} ({body})",
	api.ErrorReasonSsoLdapUnreachable:            "Could not connect to {url} ({detail})",
	api.ErrorReasonSsoLdapStarttlsFailed:         "StartTLS failed ({detail})",
	api.ErrorReasonSsoLdapBindFailed:             "Service account bind failed ({detail})",
	api.ErrorReasonAllowlistPermUnavailable:      "The node does not expose the PERM namespace (rpc-http-api)",
	api.ErrorReasonAllowlistFileDisabled:         "The node has not enabled the accounts allowlist file (permissions-accounts-config-file-enabled)",
	api.ErrorReasonAllowlistPersistFailed:        "The node cannot write its allowlist file; check that the file exists and the besu user can write to it.",
	api.ErrorReasonAllowlistReadFailed:           "Could not read the node’s allowlist",
	api.ErrorReasonConnDialFailed:                "Couldn’t connect ({detail})",
	api.ErrorReasonSmtpStarttlsUnsupported:       "The SMTP server doesn’t support STARTTLS; choose TLS if it encrypts from the first byte (usually port 465), otherwise set Encryption to None",
	api.ErrorReasonSmtpStarttlsFailed:            "STARTTLS failed ({detail})",
	api.ErrorReasonSmtpAuthMechUnsupported:       "Unsupported SMTP authentication method {mechs} (only PLAIN and LOGIN are supported)",
	api.ErrorReasonSmtpSenderRejected:            "Sender rejected ({detail})",
	api.ErrorReasonSmtpRecipientRejected:         "Recipient rejected ({detail})",
	api.ErrorReasonSmtpDataFailed:                "DATA command failed ({detail})",
	api.ErrorReasonSmtpWriteFailed:               "Couldn’t send the message body ({detail})",
	api.ErrorReasonSmtpNotAccepted:               "The server didn’t accept the message ({detail})",
	api.ErrorReasonExternalOrgInternal:           "Platform internal error (details are in the platform log)",
	api.ErrorReasonNoValidatorNodes:              "This organization has no validator nodes",
}

// Message is the reason's English sentence (with params filled in).
func Message(reason api.ErrorReason, params map[string]string) string {
	msg := templates[reason]
	for k, v := range params {
		msg = strings.ReplaceAll(msg, "{"+k+"}", v)
	}
	return msg
}

// New returns an error carrying a reason. Each generated per-op error type is a defined type of api.Error, so callers convert directly,
// e.g. api.UpdateLocalUser409JSONResponse(apierr.New(api.ErrorCodeConflict, api.ErrorReasonLastAdmin, nil)).
func New(code api.ErrorCode, reason api.ErrorReason, params map[string]string) api.Error {
	e := api.Error{Code: code, Message: Message(reason, params), Reason: &reason}
	if len(params) > 0 {
		e.Params = &params
	}
	return e
}

// Write writes the error as a JSON response (for middleware and routing code that writes responses outside the strict handler).
func Write(w http.ResponseWriter, status int, e api.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(e)
}

// coded is the underlying error carrying a reason. Error() is the original sentence meant for logs, Unwrap returns the original error,
// so errors.Is / errors.As work as before; handlers use ReasonOf to pull out the reason for the user.
type coded struct {
	Reason api.ErrorReason
	Params map[string]string
	Err    error
}

func (e *coded) Error() string { return e.Err.Error() }
func (e *coded) Unwrap() error { return e.Err }

// Wrap attaches a reason to err. The wrapping layer attaches only when the inner error has no reason yet (the inner cause is more fundamental).
func Wrap(reason api.ErrorReason, params map[string]string, err error) error {
	if _, _, ok := ReasonOf(err); ok {
		return err
	}
	return &coded{Reason: reason, Params: params, Err: err}
}

// ReasonOf returns the reason and params carried on the error chain.
func ReasonOf(err error) (api.ErrorReason, map[string]string, bool) {
	var c *coded
	if errors.As(err, &c) {
		return c.Reason, c.Params, true
	}
	return "", nil, false
}
