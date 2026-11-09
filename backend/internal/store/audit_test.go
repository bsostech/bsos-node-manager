package store

import (
	"context"
	"testing"
)

// The Core edition does no auditing: with no writer, Audit does nothing, and there are no audit tables.
func TestCoreHasNoAudit(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if AuditSink != nil {
		t.Fatal("Core 的 store 套件不應裝上稽核寫入端")
	}
	if err := s.Audit(ctx, "login", map[string]any{"username": "x"}); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT to_regclass('audit_log') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("Core 的 schema 不應有 audit_log")
	}
}
