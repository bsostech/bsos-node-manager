// Package storetest: store helpers for tests only (imported only by _test.go files, never in production binaries).
package storetest

import (
	"context"
	"errors"
	"sync"
	"testing"

	"besu-manager/backend/internal/store"
)

// FailAuditForTest: during the test, replaces the audit writer with one that always fails, and restores it at the
// end (for testing fail-closed). events receives the event name of every write attempt, including failed ones.
func FailAuditForTest(t testing.TB) *[]string {
	return SinkForTest(t, errors.New("audit down"))
}

// SinkForTest: during the test, replaces the audit writer with one that only collects events (a nil err
// counts as a successful write).
func SinkForTest(t testing.TB, err error) *[]string {
	t.Helper()
	saved := store.AuditSink
	var events []string
	store.AuditSink = func(_ context.Context, _ store.AuditDB, event string, detail map[string]any) error {
		if r, _ := detail["result"].(string); r != "" {
			event += ":" + r
		}
		events = append(events, event)
		return err
	}
	t.Cleanup(func() { store.AuditSink = saved })
	return &events
}

// CaptureAuditForTest: during the test, replaces the audit writer with one that collects the detail of every
// entry keep accepts (all of them when keep is nil); every write succeeds.
func CaptureAuditForTest(t testing.TB, keep func(event string, detail map[string]any) bool) *[]map[string]any {
	t.Helper()
	saved := store.AuditSink
	var mu sync.Mutex // handlers may audit from more than one goroutine
	var got []map[string]any
	store.AuditSink = func(_ context.Context, _ store.AuditDB, event string, detail map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		if keep == nil || keep(event, detail) {
			got = append(got, detail)
		}
		return nil
	}
	t.Cleanup(func() { store.AuditSink = saved })
	return &got
}
