package auth

import (
	"testing"

	"besu-manager/backend/internal/auth/authtest"
)

// isPublic: only these session-less endpoints may pass; everything else requires login. Missing one blocks legitimate traffic,
// an extra one is an unauthorized bypass, so each is pinned individually.
func TestIsPublic(t *testing.T) {
	public := []struct{ method, path string }{
		{"GET", "/api/v1/auth/config"},
		{"POST", "/api/v1/auth/login"},
		{"GET", "/healthz"},
		{"GET", "/api/v1/platform/besu-log4j2.xml"},
		{"GET", "/besu-agent-linux-amd64"},
		{"HEAD", "/besu-agent-linux-amd64"},
	}
	for _, c := range public {
		if !isPublic(c.method, c.path) {
			t.Errorf("%s %s 應為 public", c.method, c.path)
		}
	}

	// These must require a session, especially /me, /auth/password, logout and every data endpoint.
	protected := []struct{ method, path string }{
		{"GET", "/api/v1/me"},
		{"POST", "/api/v1/auth/password"},
		{"POST", "/api/v1/auth/logout"},
		{"GET", "/api/v1/nodes"},
		{"GET", "/api/v1/auth/config2"},    // a look-alike path must not pass
		{"GET", "/api/v1/auth/oidc/login"}, // OIDC lives in Enterprise; Core does not let it through
		{"HEAD", "/install.sh"},            // HEAD is public only for the agent download
	}
	for _, c := range protected {
		if isPublic(c.method, c.path) {
			t.Errorf("%s %s 不該是 public", c.method, c.path)
		}
	}
}

// Permissions declared in the contract: privileged path -> required permission. A wrong mapping means privilege escalation or wrongful blocking, so the important ones are pinned individually (the permissions themselves live only in the contract).
func TestOperationPerms(t *testing.T) {
	ops := authtest.Load(t, coreSpec)
	cases := []struct {
		method, path string
		want         Permission
	}{
		{"POST", "/api/v1/join-tokens", PermNodesOnboard},
		{"POST", "/api/v1/join-tokens/jt-1/revoke", PermNodesOnboard},
		{"GET", "/api/v1/nodes/node-a1/peers", PermMonitorView}, // callers without monitor:view (cross-org forwarding, admin) get the allowlisted fields (server publicPeer)
		{"POST", "/api/v1/manual-peers", PermNodesOnboard},
		{"DELETE", "/api/v1/manual-peers/abcd", PermNodesOnboard},
		{"GET", "/api/v1/users", PermMembersManage},
		{"GET", "/api/v1/auth/login-events", PermMembersManage},
		{"POST", "/api/v1/users", PermMembersManage},
		{"PATCH", "/api/v1/users/alice", PermMembersManage},
		{"POST", "/api/v1/users/alice/reset-password", PermMembersManage},
		{"POST", "/api/v1/validator-votes", PermNodesOnboard},
		{"DELETE", "/api/v1/validator-votes/0xabc", PermNodesOnboard},
		{"GET", "/api/v1/nodes/node-a1/custom-peers", PermMonitorView},
		{"POST", "/api/v1/nodes/node-a1/custom-peers", PermNodesOnboard},
		{"DELETE", "/api/v1/nodes/node-a1/custom-peers/abcd", PermNodesOnboard},
		{"PUT", "/api/v1/nodes/node-a1/peer-shared", PermNodesOnboard},
		{"PUT", "/api/v1/nodes/node-a1/allowlist", PermNodesOnboard},
		{"DELETE", "/api/v1/nodes/node-a1/allowlist", PermNodesOnboard},
		{"PUT", "/api/v1/peer-names/abababababababababababababababababababababababababababababababababababababababababababababababababababababababababababababababab", PermNodesOnboard},
		{"DELETE", "/api/v1/peer-names/abababababababababababababababababababababababababababababababababababababababababababababababababababababababababababababababab", PermNodesOnboard},
		// Deleting a node (offboarding) needs nodes:onboard, but GET on node details/subresources must not be caught by this rule.
		{"DELETE", "/api/v1/nodes/node-a1", PermNodesOnboard},
		{"GET", "/api/v1/nodes/node-a1", PermMonitorView},
		// The log level is set by issuing admin_changeLogLevel to the node; viewer cannot change it.
		{"PUT", "/api/v1/nodes/node-a1/log-level", PermNodesOnboard},
		{"GET", "/api/v1/nodes/node-a1/logs", PermMonitorView},
		{"PUT", "/api/v1/nodes/node-a1/ports", PermNodesOnboard},
		{"PUT", "/api/v1/nodes/node-a1/name", PermNodesOnboard},
		{"GET", "/api/v1/shared-peers", PermMonitorView},
		// RPC users are shared across the organization, not under /nodes/{id}; writes need nodes:onboard, reads need monitor:view (visible on the operations page but not editable).
		{"GET", "/api/v1/rpc-users", PermMonitorView},
		{"POST", "/api/v1/rpc-users", PermNodesOnboard},
		{"DELETE", "/api/v1/rpc-users/u1", PermNodesOnboard},
		{"POST", "/api/v1/rpc-users/u1/token", PermNodesOnboard},
		{"GET", "/api/v1/rpc-users/u1/events", PermMonitorView},
		{"GET", "/api/v1/rpc-method-catalog", PermMonitorView},

		// Every role must be able to use these (admin lacks monitor:view): change own password, view self, log out.
		{"POST", "/api/v1/auth/password", PermLoggedIn},
		{"GET", "/api/v1/me", PermLoggedIn},
		{"POST", "/api/v1/auth/logout", PermLoggedIn},
		// Reading monitoring = monitor:view (admin does not have it).
		{"GET", "/api/v1/nodes", PermMonitorView},
		// The validator set and pending votes have their own validators:view; voting is still nodes:onboard.
		{"GET", "/api/v1/validators", PermValidatorsView},
		{"GET", "/api/v1/validator-votes", PermValidatorsView},
		{"POST", "/api/v1/validator-votes", PermNodesOnboard},
		{"DELETE", "/api/v1/validator-votes/0xabc", PermNodesOnboard},

		// Alert notification settings: writes on every subpath are nodes:onboard; viewer cannot change SMTP.
		{"GET", "/api/v1/alert-settings", PermMonitorView},
		{"PUT", "/api/v1/alert-settings/smtp", PermNodesOnboard},
		{"DELETE", "/api/v1/alert-settings/smtp", PermNodesOnboard},
		{"PUT", "/api/v1/alert-settings/webhooks", PermNodesOnboard},
		{"POST", "/api/v1/alert-settings/test-smtp", PermNodesOnboard},
		{"POST", "/api/v1/alert-settings/test-webhook", PermNodesOnboard},
		{"PUT", "/api/v1/alert-settings/telegram", PermNodesOnboard},
		{"DELETE", "/api/v1/alert-settings/slack", PermNodesOnboard},
		{"POST", "/api/v1/alert-settings/test-telegram", PermNodesOnboard},
		{"POST", "/api/v1/alert-settings/test-slack", PermNodesOnboard},
	}
	for _, c := range cases {
		if got := permOf(t, ops, c.method, c.path); got != c.want {
			t.Errorf("%s %s 宣告 %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

// roleCan: the role x permission matrix (must match the frontend's auth/permissions.ts).
func TestRoleCanMatrix(t *testing.T) {
	type want struct {
		role string
		perm Permission
		ok   bool
	}
	cases := []want{
		// viewer: sees monitoring and validators
		{"viewer", PermMonitorView, true},
		{"viewer", PermValidatorsView, true},
		{"viewer", PermNodesOnboard, false},
		{"viewer", PermMembersManage, false},
		// operator: + node onboarding and operations, but cannot manage members
		{"operator", PermValidatorsView, true},
		{"operator", PermNodesOnboard, true},
		{"operator", PermMembersManage, false},
		// admin: manages accounts only (the only role with members:manage); no monitoring, no nodes
		{"admin", PermMembersManage, true},
		{"admin", PermMonitorView, false},
		{"admin", PermValidatorsView, false},
		{"admin", PermNodesOnboard, false},
		// unknown roles (including ones left over from Enterprise): always denied
		{"", PermMonitorView, false},
		{"superuser", PermMembersManage, false},
	}
	for _, c := range cases {
		if got := roleCan(c.role, c.perm); got != c.ok {
			t.Errorf("roleCan(%q,%q)=%v, want %v", c.role, c.perm, got, c.ok)
		}
	}

	// Security invariant: only admin has members:manage.
	for _, r := range []string{"viewer", "operator"} {
		if roleCan(r, PermMembersManage) {
			t.Errorf("%s 不該有 members:manage", r)
		}
	}
}

// Core has only three roles; Enterprise adds its own roles by swapping in its own table.
func TestValidRoleCore(t *testing.T) {
	for _, r := range []string{"viewer", "operator", "admin"} {
		if !ValidRole(r) {
			t.Errorf("%s 應是合法角色", r)
		}
	}
	if ValidRole("superuser") {
		t.Error("Core 不該接受三個以外的角色")
	}
}

// Pass/deny results for role x contract permission: admin can only reach accounts; monitoring and validators are blocked by the backend, not merely hidden by the frontend.
func TestRoleRouteAccess(t *testing.T) {
	ops := authtest.Load(t, coreSpec)
	for _, c := range []struct {
		role, method, path string
		ok                 bool
	}{
		{"admin", "GET", "/api/v1/users", true},
		{"admin", "POST", "/api/v1/alerts/read", false},
		{"viewer", "POST", "/api/v1/alerts/read", true},
		{"viewer", "POST", "/api/v1/alerts/read-all", true},
		{"operator", "POST", "/api/v1/alerts/read", true},
		{"operator", "POST", "/api/v1/alerts/read-all", true},
		{"admin", "GET", "/api/v1/auth/login-events", true},
		{"admin", "GET", "/api/v1/me", true},
		{"admin", "POST", "/api/v1/auth/password", true},
		{"admin", "POST", "/api/v1/auth/logout", true},
		{"admin", "GET", "/api/v1/nodes", false},
		{"admin", "GET", "/api/v1/chain", false},
		{"admin", "GET", "/api/v1/validators", false},
		{"admin", "GET", "/api/v1/alerts", false},
		{"admin", "POST", "/api/v1/join-tokens", false},
		{"viewer", "GET", "/api/v1/nodes", true},
		{"viewer", "GET", "/api/v1/validators", true},
		{"viewer", "GET", "/api/v1/validator-votes", true},
		{"viewer", "POST", "/api/v1/validator-votes", false},
		{"viewer", "GET", "/api/v1/users", false},
		{"viewer", "GET", "/api/v1/auth/login-events", false},
		{"viewer", "GET", "/api/v1/me", true},
		{"operator", "GET", "/api/v1/validators", true},
		{"operator", "POST", "/api/v1/join-tokens", true},
		{"operator", "GET", "/api/v1/users", false},
		{"operator", "GET", "/api/v1/auth/login-events", false},
		// visible on the operations page but not editable: reads are monitor:view, writes are nodes:onboard
		{"viewer", "GET", "/api/v1/permissioning", true},
		{"viewer", "PUT", "/api/v1/permissioning", false},
		{"viewer", "PUT", "/api/v1/nodes/x/allowlist", false},
		{"viewer", "PUT", "/api/v1/nodes/x/allowlist/shared", false},
		{"viewer", "POST", "/api/v1/nodes/x/allowlist/sync", false},
		{"viewer", "GET", "/api/v1/account-names", true},
		{"viewer", "PUT", "/api/v1/account-names/0xabc", false},
		{"viewer", "DELETE", "/api/v1/account-names/0xabc", false},
		{"operator", "PUT", "/api/v1/nodes/x/allowlist/shared", true},
		{"operator", "POST", "/api/v1/nodes/x/allowlist/sync", true},
		{"operator", "PUT", "/api/v1/account-names/0xabc", true},
		{"viewer", "GET", "/api/v1/rpc-users", true},
		{"viewer", "GET", "/api/v1/rpc-users/u1/events", true},
		{"viewer", "POST", "/api/v1/rpc-users", false},
		{"viewer", "POST", "/api/v1/rpc-users/u1/token", false},
		{"viewer", "GET", "/api/v1/shared-peers", true},
		{"viewer", "GET", "/api/v1/nodes/x/custom-peers", true},
		{"viewer", "POST", "/api/v1/nodes/x/custom-peers", false},
		{"viewer", "PUT", "/api/v1/nodes/x/peer-shared", false},
		{"operator", "PUT", "/api/v1/nodes/x/peer-shared", true},
		{"viewer", "GET", "/api/v1/nodes/x/logs", true},
		{"viewer", "PUT", "/api/v1/nodes/x/log-level", false},
		{"viewer", "GET", "/api/v1/alert-settings", true},
		{"viewer", "PUT", "/api/v1/alert-settings/webhooks", false},
		{"viewer", "POST", "/api/v1/alert-settings/test-smtp", false},
		{"viewer", "GET", "/api/v1/join-tokens", true},
		{"viewer", "POST", "/api/v1/join-tokens", false},
		{"admin", "GET", "/api/v1/permissioning", false},
		{"admin", "GET", "/api/v1/rpc-users", false},
		{"admin", "GET", "/api/v1/nodes/x/logs", false},
		{"admin", "GET", "/api/v1/rpc-users/u1/events", false},
		{"admin", "GET", "/api/v1/shared-peers", false},
		{"admin", "GET", "/api/v1/nodes/x/custom-peers", false},
		{"admin", "GET", "/api/v1/alert-settings", false},
	} {
		if got := allows(t, ops, c.role, c.method, c.path); got != c.ok {
			t.Errorf("%s %s %s: 放行=%v, want %v(權限 %q)", c.role, c.method, c.path, got, c.ok, permOf(t, ops, c.method, c.path))
		}
	}
	// view self, change password, log out: every role must be able to call these
	for _, role := range []string{"viewer", "operator", "admin"} {
		for _, r := range [][2]string{{"GET", "/api/v1/me"}, {"POST", "/api/v1/auth/password"}, {"POST", "/api/v1/auth/logout"}} {
			if !allows(t, ops, role, r[0], r[1]) {
				t.Errorf("%s 應能打 %s %s", role, r[0], r[1])
			}
		}
	}
}

const coreSpec = "../../../contract/openapi.yaml"

func permOf(t *testing.T, ops []authtest.Op, method, path string) Permission {
	return Permission(authtest.Perm(t, ops, method, path))
}

func allows(t *testing.T, ops []authtest.Op, role, method, path string) bool {
	return Permits(role, permOf(t, ops, method, path))
}
