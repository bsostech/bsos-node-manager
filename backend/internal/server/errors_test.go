package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"besu-manager/backend/internal/api"
	"besu-manager/backend/internal/store"
)

// 500 does not return the raw error: it often contains internal hosts, DNs and database addresses,
// and the login API returns it before authentication. Returns Error JSON with internal_error.
func TestStrictResponseErrorHidesInternalDetail(t *testing.T) {
	w := httptest.NewRecorder()
	StrictResponseError(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil), errors.New(`連不上 LDAP: dial tcp: lookup ldap.corp.internal: no such host`))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "ldap.corp.internal") {
		t.Fatalf("500 洩漏了錯誤原文: %d %q", w.Code, w.Body.String())
	}
	var e api.Error
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || !hasReason(e, api.ErrorReasonInternalError) {
		t.Fatalf("500 要是帶 internal_error 的 JSON: %q", w.Body.String())
	}
}

// Audit cannot be written (including wrapped errors): 503 with audit_unavailable, without the raw
// database text.
func TestStrictResponseErrorAuditUnavailable(t *testing.T) {
	w := httptest.NewRecorder()
	StrictResponseError(w, httptest.NewRequest(http.MethodPost, "/api/v1/users", nil), fmt.Errorf("%w: connection refused", store.ErrAudit))
	var e api.Error
	if w.Code != http.StatusServiceUnavailable || json.Unmarshal(w.Body.Bytes(), &e) != nil || !hasReason(e, api.ErrorReasonAuditUnavailable) || strings.Contains(w.Body.String(), "connection refused") {
		t.Fatalf("稽核寫不進去要 503 audit_unavailable: %d %q", w.Code, w.Body.String())
	}
}

// An internal error made the operation fail: the audit stores operation_failed and the raw text
// goes in reason_params.detail.
func TestAuditFailedInternal(t *testing.T) {
	s, _ := accessTestServer(t)
	audited := recordAudit(t)
	s.auditFailedInternal(asAdmin(context.Background()), "node_delete", map[string]any{"node_id": "n1"}, errors.New("寫 nodes: connection refused"))
	d := audited("node_delete")
	if len(d) != 1 || d[0]["reason"] != string(api.ErrorReasonOperationFailed) {
		t.Fatalf("稽核要存 operation_failed: %v", d)
	}
	if p, _ := d[0]["reason_params"].(map[string]string); p["detail"] != "寫 nodes: connection refused" {
		t.Fatalf("原文要在 reason_params.detail: %v", d[0])
	}
}

// The database cannot be reached (here: a real failed connection attempt): 503 db_unavailable, the same as
// the session check answers, without the connection details.
func TestStrictResponseErrorDatabaseUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := pgconn.Connect(ctx, "postgres://u:p@127.0.0.1:1/db?connect_timeout=2")
	if err == nil {
		t.Skip("something answered on 127.0.0.1:1")
	}
	w := httptest.NewRecorder()
	StrictResponseError(w, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil), fmt.Errorf("login: %w", err))
	var e api.Error
	if w.Code != http.StatusServiceUnavailable || json.Unmarshal(w.Body.Bytes(), &e) != nil || !hasReason(e, api.ErrorReasonDbUnavailable) || strings.Contains(w.Body.String(), "127.0.0.1") {
		t.Fatalf("資料庫連不上要 503 db_unavailable: %d %q", w.Code, w.Body.String())
	}
	if store.Unavailable(errors.New("ERROR: relation does not exist")) {
		t.Fatal("查詢本身失敗不算連不上")
	}
}
