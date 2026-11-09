package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func rpcTestStore(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL absent: isolated PostgreSQL integration test not run")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || !strings.Contains(strings.ToLower(u.Path), "test") {
		t.Fatal("TEST_DATABASE_URL must point to a local database with test in its name")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("rpc_test_%s", strings.ToLower(rand.Text()))
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	s := New(pool)
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

// Node name: join writes the name carried by the token; writing again for the same id (re-enroll does not go
// through here, but a resent join does) does not overwrite; renaming and clearing go through SetNodeName.
func TestNodeName(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	nm := "台北主驗證者"
	if err := s.UpsertOwnNode(ctx, "vn-01", "org-A", 8545, 9545, &nm); err != nil {
		t.Fatal(err)
	}
	other := "別的名字"
	if err := s.UpsertOwnNode(ctx, "vn-01", "org-A", 8545, 9545, &other); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNode(ctx, "vn-01")
	if err != nil || n == nil || n.Name == nil || *n.Name != nm {
		t.Fatalf("name = %v, err = %v", n, err)
	}
	if ok, err := s.SetNodeName(ctx, "vn-01", nil); err != nil || !ok {
		t.Fatalf("clear: %v %v", ok, err)
	}
	if n, _ := s.GetNode(ctx, "vn-01"); n.Name != nil {
		t.Fatalf("清除後應為 nil:%v", *n.Name)
	}
	if ok, _ := s.SetNodeName(ctx, "nope", &nm); ok {
		t.Fatal("不存在的節點應回 false")
	}
	list, err := s.ListNodes(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "vn-01" {
		t.Fatalf("ListNodes = %v, %v", list, err)
	}
}

// The agent runtime survives in the DB (it is read while the agent is offline) and the column rejects
// anything but the two runtimes.
func TestNodeAgentRuntime(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	if err := s.UpsertOwnNode(ctx, "vn-01", "org-A", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.GetNode(ctx, "vn-01"); n.AgentRuntime != nil {
		t.Fatalf("new node should have no runtime: %v", *n.AgentRuntime)
	}
	if err := s.SetNodeAgentRuntime(ctx, "vn-01", "docker"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListNodes(ctx)
	if err != nil || len(list) != 1 || list[0].AgentRuntime == nil || *list[0].AgentRuntime != "docker" {
		t.Fatalf("ListNodes = %+v, %v", list, err)
	}
	if err := s.SetNodeAgentRuntime(ctx, "vn-01", "podman"); err == nil {
		t.Fatal("an unknown runtime should be rejected")
	}
}

// The join time is compared with collector snapshot times, which come from the platform clock, so it
// must come from the platform clock too, not from the database's now().
func TestJoinTimeUsesPlatformClock(t *testing.T) {
	s := rpcTestStore(t)
	ctx := context.Background()
	joined := time.Date(2001, 2, 3, 4, 5, 6, 7000, time.UTC)
	s.now = func() time.Time { return joined }
	if err := s.UpsertOwnNode(ctx, "vn-01", "org-A", 8545, 9545, nil); err != nil {
		t.Fatal(err)
	}
	n, err := s.GetNode(ctx, "vn-01")
	if err != nil || n == nil || n.AllowlistInitialSince == nil {
		t.Fatalf("node = %v, err = %v", n, err)
	}
	if !n.AllowlistInitialSince.Equal(joined) {
		t.Fatalf("join time = %v, want the platform clock's %v", *n.AllowlistInitialSince, joined)
	}
}
