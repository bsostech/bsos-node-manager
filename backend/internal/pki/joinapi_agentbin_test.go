package pki

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"besu-manager/backend/internal/ca"
	"besu-manager/backend/internal/store"
)

// The mTLS upgrade endpoints serve the agent binary, its checksum and version only to a verified agent of
// this org; without a cert it is 401, with a non-agent cert 403. Unknown arch or a missing file is 404.
func TestAgentBinaryRequiresAgentCert(t *testing.T) {
	st := pkiTestStore(t)
	s, err := Load(context.Background(), st, testMaster(t), "org-test", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := []byte("\x7fELF fake agent build")
	if err := os.WriteFile(filepath.Join(dir, "besu-agent-linux-amd64"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "besu-agent-linux-amd64.version"), []byte("v1.2.3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// arm64 has a binary but no .version file
	if err := os.WriteFile(filepath.Join(dir, "besu-agent-linux-arm64"), bin, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _ := agentListener(t, s, dir)

	agent, _ := issueCert(t, s.CA, ca.CertProfile{Role: ca.RoleAgent, NodeID: "vn-01"})
	platform, _ := issueCert(t, s.CA, ca.CertProfile{Role: ca.RolePlatform})
	sum := sha256.Sum256(bin)

	get := func(cert *tls.Certificate, path string) (int, string) {
		t.Helper()
		resp, err := agentClient(t, s, cert).Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	paths := []string{"/pki/v1/agent-binary/amd64", "/pki/v1/agent-binary/amd64.sha256", "/pki/v1/agent-binary/amd64/version"}
	for _, p := range paths {
		if code, _ := get(nil, p); code != http.StatusUnauthorized {
			t.Errorf("%s 沒有 client 憑證:%d,want 401", p, code)
		}
		if code, _ := get(&platform, p); code != http.StatusForbidden {
			t.Errorf("%s platform 憑證:%d,want 403", p, code)
		}
	}

	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/pki/v1/agent-binary/amd64", http.StatusOK, string(bin)},
		{"/pki/v1/agent-binary/amd64.sha256", http.StatusOK, hex.EncodeToString(sum[:]) + "\n"},
		{"/pki/v1/agent-binary/amd64/version", http.StatusOK, "v1.2.3\n"},
		{"/pki/v1/agent-binary/arm64/version", http.StatusNotFound, ""},
		{"/pki/v1/agent-binary/mips", http.StatusNotFound, ""},
		{"/pki/v1/agent-binary/mips.sha256", http.StatusNotFound, ""},
		{"/pki/v1/agent-binary/..%2f..%2fetc%2fpasswd", http.StatusNotFound, ""},
	} {
		code, body := get(&agent, tc.path)
		if code != tc.code || (tc.code == http.StatusOK && body != tc.body) {
			t.Errorf("%s:%d %q,want %d %q", tc.path, code, body, tc.code, tc.body)
		}
	}
}

// A step after the allowed audit entry fails (here: the DB call is cancelled): join stores no node and
// leaves the token usable, re-enroll revokes nothing; both answer 500 and record a failed entry.
func TestEnrollFailsAfterAccept(t *testing.T) {
	for _, tc := range []struct {
		event string
		h     func(*Service) http.HandlerFunc
	}{
		{"join", func(s *Service) http.HandlerFunc { return s.handleJoin }},
		{"re_enroll", func(s *Service) http.HandlerFunc { return s.handleReEnroll }},
	} {
		t.Run(tc.event, func(t *testing.T) {
			s, st := enrollFixture(t, "n")
			bg := context.Background()
			if tc.event == "re_enroll" {
				name, id := "n", "vn-01"
				if err := st.UpsertOwnNode(bg, "vn-01", "org-test", 8545, 9545, &name); err != nil {
					t.Fatal(err)
				}
				if err := st.InsertIssuedCert(bg, store.IssuedCert{Serial: "old", Subject: "CN=vn-01", NodeID: &id,
					NotBefore: time.Now().UTC(), NotAfter: time.Now().UTC().Add(time.Hour), CertPEM: "x"}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			var results []string
			saved := store.AuditSink
			store.AuditSink = func(_ context.Context, _ store.AuditDB, event string, d map[string]any) error {
				if event == tc.event {
					r, _ := d["result"].(string)
					results = append(results, fmt.Sprint(r, " ", d["reason"]))
					if r == "" { // the opening entry: make the next DB step fail
						cancel()
					}
				}
				return nil
			}
			t.Cleanup(func() { store.AuditSink = saved })

			w := postEnroll(t, func(w http.ResponseWriter, r *http.Request) { tc.h(s)(w, r.WithContext(ctx)) })
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("%s = %d %s", tc.event, w.Code, w.Body)
			}
			if len(results) != 2 || results[1] != "failed operation_failed" {
				t.Fatalf("audit %v", results)
			}
			if tk, _ := st.GetJoinTokenByHash(bg, tokenHash("tok")); tk == nil || tk.Status != "pending" {
				t.Fatalf("失敗時 token 不該作廢: %+v", tk)
			}
			if tc.event == "join" {
				if n, _ := st.GetNode(bg, "vn-01"); n != nil {
					t.Fatal("失敗時不該建出節點")
				}
			} else if revoked, _ := st.RevokedSerials(bg); len(revoked) != 0 {
				t.Fatalf("撤銷失敗時不該有憑證被撤:%v", revoked)
			}
		})
	}
}
