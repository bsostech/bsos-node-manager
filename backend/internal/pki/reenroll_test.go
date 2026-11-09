package pki

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"besu-manager/backend/internal/reqinfo"
	"besu-manager/backend/internal/store"
	"besu-manager/backend/internal/store/storetest"
)

// enrollFixture: Service on the test DB, plus a pending token bound to vn-01 carrying the name tokenName
// (plaintext "tok").
func enrollFixture(t *testing.T, tokenName string) (*Service, *store.Store) {
	t.Helper()
	st := pkiTestStore(t)
	ctx := context.Background()
	s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	id, now := "vn-01", time.Now().UTC()
	if err := st.CreateJoinToken(ctx, store.JoinToken{ID: "jt-1", NodeName: &tokenName, SecretHash: tokenHash("tok"), Status: "pending",
		NodeID: id, CreatedAt: now, ExpiresAt: now.Add(time.Hour), RpcPort: 8545, MetricsPort: 9545}); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func postEnroll(t *testing.T, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	return postEnrollCSR(t, h, false)
}

// postEnrollCSR is postEnroll with an option to corrupt the CSR signature: the CSR still parses, but the CA
// refuses to sign it.
func postEnrollCSR(t *testing.T, h http.HandlerFunc, badSignature bool) *httptest.ResponseRecorder {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	if badSignature {
		csrDER[len(csrDER)-1] ^= 0xff
	}
	body, _ := json.Marshal(joinRequest{Token: "tok", CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))})
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	return w
}

func nodeName(t *testing.T, st *store.Store) string {
	t.Helper()
	n, err := st.GetNode(context.Background(), "vn-01")
	if err != nil || n == nil || n.Name == nil {
		t.Fatalf("node = %v, err = %v", n, err)
	}
	return *n.Name
}

// join writes the name on the token as the node name.
func TestJoinWritesNodeName(t *testing.T) {
	s, st := enrollFixture(t, "台北主驗證者")
	if w := postEnroll(t, s.handleJoin); w.Code != http.StatusOK {
		t.Fatalf("join = %d %s", w.Code, w.Body)
	}
	if got := nodeName(t, st); got != "台北主驗證者" {
		t.Fatalf("name = %q", got)
	}
}

// re-enroll only replaces the cert and leaves the inventory row alone: the name on the token does not
// overwrite the node's existing name.
func TestReEnrollKeepsNodeName(t *testing.T) {
	s, st := enrollFixture(t, "別的名字")
	name := "台北主驗證者"
	if err := st.UpsertOwnNode(context.Background(), "vn-01", "org-test", 8545, 9545, &name); err != nil {
		t.Fatal(err)
	}
	entries := storetest.CaptureAuditForTest(t, func(event string, _ map[string]any) bool { return event == "re_enroll" })
	w := postEnroll(t, s.handleReEnroll)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enroll = %d %s", w.Code, w.Body)
	}
	if got := nodeName(t, st); got != name {
		t.Fatalf("name = %q", got)
	}
	// An opening entry without a result, then a done entry naming the new serial, so the audit trail links
	// the reissue to the cert the agent now holds
	leaf := respLeaf(t, w)
	if len(*entries) != 2 || (*entries)[0]["result"] != nil || (*entries)[1]["result"] != "done" ||
		(*entries)[1]["serial"] != leaf.SerialNumber.String() || (*entries)[1]["revoked_certs"] != int64(0) {
		t.Fatalf("audit = %v", *entries)
	}
}

// respLeaf parses the leaf cert out of a successful join/re-enroll response.
func respLeaf(t *testing.T, w *httptest.ResponseRecorder) *x509.Certificate {
	t.Helper()
	var resp joinResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	b, _ := pem.Decode([]byte(resp.CertPEM))
	if b == nil {
		t.Fatalf("no cert in response: %s", w.Body)
	}
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// Revocations live in the DB; the in-memory deny list must pick them up both on start (Load) and on refresh,
// otherwise a revoked agent cert passes mTLS after a restart.
func TestDenyListLoadsRevokedFromDB(t *testing.T) {
	s, st := enrollFixture(t, "n")
	w := postEnroll(t, s.handleJoin)
	if w.Code != http.StatusOK {
		t.Fatalf("join = %d %s", w.Code, w.Body)
	}
	serial := respLeaf(t, w).SerialNumber.String()
	ctx := context.Background()
	if n, err := st.RevokeNodeCerts(ctx, "vn-01", "retired", "admin"); err != nil || n != 1 {
		t.Fatalf("revoke: n=%d err=%v", n, err)
	}
	if err := s.RefreshDenyList(ctx); err != nil {
		t.Fatal(err)
	}
	if !s.IsRevoked(serial) {
		t.Fatal("refresh did not pick up the revoked serial")
	}
	restarted, err := Load(ctx, st, s.master, "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.IsRevoked(serial) {
		t.Fatal("Load did not pick up the revoked serial")
	}
}

// Audit write fails: join issues no cert, creates no node and does not consume the token; re-enroll does not
// revoke the old cert (fail-closed).
func TestEnrollFailClosed(t *testing.T) {
	s, st := enrollFixture(t, "n")
	storetest.FailAuditForTest(t)
	if w := postEnroll(t, s.handleJoin); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("join = %d %s", w.Code, w.Body)
	}
	ctx := context.Background()
	if n, _ := st.GetNode(ctx, "vn-01"); n != nil {
		t.Fatal("稽核失敗時不該建出節點")
	}
	if tk, _ := st.GetJoinTokenByHash(ctx, tokenHash("tok")); tk == nil || tk.Status != "pending" {
		t.Fatalf("token 不該作廢: %+v", tk)
	}

	name := "n"
	if err := st.UpsertOwnNode(ctx, "vn-01", "org-test", 8545, 9545, &name); err != nil {
		t.Fatal(err)
	}
	if w := postEnroll(t, s.handleReEnroll); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("re-enroll = %d %s", w.Code, w.Body)
	}
	if tk, _ := st.GetJoinTokenByHash(ctx, tokenHash("tok")); tk == nil || tk.Status != "pending" {
		t.Fatalf("re-enroll 失敗時 token 不該作廢: %+v", tk)
	}
}

// When an expired token is rejected, the record carries the actual status (expired, not the pending in the DB
// column) plus the node id bound to the token. Same code path for join and re-enroll.
func TestEnrollRejectedExpiredAudit(t *testing.T) {
	for _, tc := range []struct {
		event string
		h     func(*Service) http.HandlerFunc
	}{{"join", func(s *Service) http.HandlerFunc { return s.handleJoin }}, {"re_enroll", func(s *Service) http.HandlerFunc { return s.handleReEnroll }}} {
		t.Run(tc.event, func(t *testing.T) {
			st := pkiTestStore(t)
			ctx := context.Background()
			s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			id, past := "vn-01", time.Now().UTC().Add(-2*time.Hour)
			if err := st.CreateJoinToken(ctx, store.JoinToken{ID: "jt-old", SecretHash: tokenHash("tok"), Status: "pending",
				NodeID: id, CreatedAt: past, ExpiresAt: past.Add(time.Hour), RpcPort: 8545, MetricsPort: 9545}); err != nil {
				t.Fatal(err)
			}
			got := storetest.CaptureAuditForTest(t, func(event string, _ map[string]any) bool { return event == tc.event })
			if w := postEnroll(t, tc.h(s)); w.Code != http.StatusUnauthorized {
				t.Fatalf("%s = %d %s", tc.event, w.Code, w.Body)
			}
			if len(*got) != 1 || (*got)[0]["result"] != "failed" || (*got)[0]["reason"] != "join_token_expired" || (*got)[0]["node_id"] != "vn-01" {
				t.Fatalf("%s 被拒紀錄不對: %v", tc.event, *got)
			}
		})
	}
}

// Each refusal records result=failed with its own reason code, which the audit page translates.
func TestEnrollRejectionReasons(t *testing.T) {
	join := func(s *Service) http.HandlerFunc { return s.handleJoin }
	reEnroll := func(s *Service) http.HandlerFunc { return s.handleReEnroll }
	addNode := func(t *testing.T, s *Service, st *store.Store) {
		name := "n"
		if err := st.UpsertOwnNode(context.Background(), "vn-01", "org-test", 8545, 9545, &name); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name   string
		event  string
		h      func(*Service) http.HandlerFunc
		token  func(*store.JoinToken)
		setup  func(*testing.T, *Service, *store.Store)
		want   string
		badCSR bool
	}{
		{"unknown token", "join", join, func(tk *store.JoinToken) { tk.SecretHash = tokenHash("other") }, nil, "join_token_not_found", false},
		{"used token", "join", join, func(tk *store.JoinToken) { tk.Status = "used" }, nil, "join_token_used", false},
		{"revoked token", "re_enroll", reEnroll, func(tk *store.JoinToken) { tk.Status = "revoked" }, nil, "join_token_revoked", false},
		{"node already exists", "join", join, nil, addNode, "join_node_exists", false},
		{"node still online", "re_enroll", reEnroll, nil, func(t *testing.T, s *Service, st *store.Store) {
			addNode(t, s, st)
			s.hub.set("vn-01", &agentConn{})
		}, "re_enroll_node_online", false},
		{"CA refuses to issue", "join", join, nil, nil, "cert_issue_failed", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := pkiTestStore(t)
			ctx := context.Background()
			s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			id, now := "vn-01", time.Now().UTC()
			tk := store.JoinToken{ID: "jt-1", SecretHash: tokenHash("tok"), Status: "pending",
				NodeID: id, CreatedAt: now, ExpiresAt: now.Add(time.Hour), RpcPort: 8545, MetricsPort: 9545}
			if c.token != nil {
				c.token(&tk)
			}
			if err := st.CreateJoinToken(ctx, tk); err != nil {
				t.Fatal(err)
			}
			if c.setup != nil {
				c.setup(t, s, st)
			}
			got := storetest.CaptureAuditForTest(t, func(event string, _ map[string]any) bool { return event == c.event })
			if w := postEnrollCSR(t, c.h(s), c.badCSR); w.Code < 400 {
				t.Fatalf("%s = %d %s", c.event, w.Code, w.Body)
			}
			if len(*got) != 1 || (*got)[0]["result"] != "failed" || (*got)[0]["reason"] != c.want {
				t.Fatalf("audit = %v, want failed %s", *got, c.want)
			}
			// Until a token with a bound node is found there is no node; the entry must not carry an empty node_id.
			noNode := c.want == "join_token_not_found"
			if _, has := (*got)[0]["node_id"]; has == noNode {
				t.Fatalf("node_id present = %v in %v", has, (*got)[0])
			}
		})
	}
}

// Join tokens carry a source-IP allowlist: from a source outside the list, both join and re-enroll return
// 403, write a denial audit entry and change nothing; inside the list they proceed as usual. The source comes
// from reqinfo (8443 always uses the connection address); httptest's source is always 192.0.2.1.
func TestEnrollSourceAllowlist(t *testing.T) {
	for _, tc := range []struct {
		event string
		h     func(*Service) http.HandlerFunc
		setup func(*testing.T, *store.Store)
	}{
		{"join", func(s *Service) http.HandlerFunc { return s.handleJoin }, func(*testing.T, *store.Store) {}},
		{"re_enroll", func(s *Service) http.HandlerFunc { return s.handleReEnroll }, func(t *testing.T, st *store.Store) {
			name := "n"
			if err := st.UpsertOwnNode(context.Background(), "vn-01", "org-test", 8545, 9545, &name); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.event, func(t *testing.T) {
			for _, c := range []struct {
				allowed []string
				want    int
			}{{[]string{"10.0.0.0/8"}, http.StatusForbidden}, {[]string{"10.0.0.0/8", "192.0.2.0/24"}, http.StatusOK}} {
				st := pkiTestStore(t)
				ctx := context.Background()
				s, err := Load(ctx, st, testMaster(t), "org-test", 72*time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				id, now, name := "vn-01", time.Now().UTC(), "n"
				if err := st.CreateJoinToken(ctx, store.JoinToken{ID: "jt-1", NodeName: &name, AllowedSources: c.allowed, SecretHash: tokenHash("tok"), Status: "pending",
					NodeID: id, CreatedAt: now, ExpiresAt: now.Add(time.Hour), RpcPort: 8545, MetricsPort: 9545}); err != nil {
					t.Fatal(err)
				}
				tc.setup(t, st)
				var results []string
				saved := store.AuditSink
				store.AuditSink = func(_ context.Context, _ store.AuditDB, event string, detail map[string]any) error {
					if event == tc.event {
						results = append(results, fmt.Sprint(detail["result"], " ", detail["reason"]))
					}
					return nil
				}
				w := postEnroll(t, reqinfo.Middleware(nil)(tc.h(s)).ServeHTTP)
				store.AuditSink = saved
				if w.Code != c.want {
					t.Fatalf("allowed %v: %s = %d %s", c.allowed, tc.event, w.Code, w.Body)
				}
				tk, _ := st.GetJoinTokenByHash(ctx, tokenHash("tok"))
				if c.want == http.StatusForbidden {
					if tk == nil || tk.Status != "pending" {
						t.Fatalf("被擋時 token 不該作廢: %+v", tk)
					}
					if len(results) != 1 || results[0] != "failed join_source_not_allowed" {
						t.Fatalf("audit %v", results)
					}
					if tc.event == "join" {
						if n, _ := st.GetNode(ctx, "vn-01"); n != nil {
							t.Fatal("被擋時不該建出節點")
						}
					}
				} else if tk == nil || tk.Status != "used" {
					t.Fatalf("放行後 token 應已使用: %+v", tk)
				}
			}
		})
	}
}
