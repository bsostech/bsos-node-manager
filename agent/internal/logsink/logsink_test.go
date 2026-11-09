package logsink

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"besu-manager/agent/internal/api"
)

// The buffer keeps only about maxBytes of the newest data: past the limit the oldest are dropped and seq
// keeps increasing.
func TestBufferDropsOldest(t *testing.T) {
	s := New()
	big := strings.Repeat("x", 1<<20) // one 1 MB line
	for i := 0; i < 5; i++ {
		s.add(fmt.Sprintf("%d%s", i, big))
	}
	if s.bytes > maxBytes+maxLine+lineOverhead {
		t.Fatalf("緩衝超量: %d", s.bytes)
	}
	got := s.Read(-1, 10)
	if len(got.Lines) == 0 || got.Lines[0].Seq == 1 || got.Lines[len(got.Lines)-1].Seq != 5 {
		t.Fatalf("應丟最舊、留最新:%v", seqs(got.Lines))
	}
	// cursor in the evicted range: return from the oldest remaining line and report how many are missing.
	oldest := got.Lines[0].Seq
	if r := s.Read(0, 10); r.Lines[0].Seq != oldest || r.Skipped != oldest-1 {
		t.Fatalf("被擠掉的行數不對: first=%d skipped=%d", r.Lines[0].Seq, r.Skipped)
	}
}

// Byte accounting: each line costs len+lineOverhead and the same amount is refunded on drop; the retained
// line count is exactly what fits under the limit, with no drift.
// When the cursor is in the evicted range, skipped equals the missing lines exactly; when the cursor is the
// oldest remaining line, nothing is missing.
func TestBufferAccountingAndSkipped(t *testing.T) {
	s := New()
	text := strings.Repeat("z", 100)
	for i := 0; i < 30000; i++ {
		s.add(text)
	}
	want := maxBytes / (len(text) + lineOverhead) // 12787
	if n := len(s.lines) - s.head; n != want {
		t.Fatalf("留存 %d 行,want %d", n, want)
	}
	oldest := int64(30000 - want + 1)
	if r := s.Read(2, 1); r.Lines[0].Seq != oldest || r.Skipped != oldest-3 {
		t.Fatalf("after=2: first=%d skipped=%d,want %d / %d", r.Lines[0].Seq, r.Skipped, oldest, oldest-3)
	}
	if r := s.Read(oldest, 1); r.Lines[0].Seq != oldest+1 || r.Skipped != 0 {
		t.Fatalf("after=最舊: first=%d skipped=%d", r.Lines[0].Seq, r.Skipped)
	}
}

// Empty lines cost something too: a run of newlines must not grow the line count without bound.
func TestEmptyLinesBounded(t *testing.T) {
	s := New()
	for i := 0; i < 200000; i++ {
		s.add("")
	}
	if n := len(s.lines) - s.head; n > maxBytes/lineOverhead+1 {
		t.Fatalf("空行沒被限制:%d 行", n)
	}
	if len(s.lines) > 2*(maxBytes/lineOverhead+1) {
		t.Fatalf("丟掉的行沒有整理掉:底層 %d 行", len(s.lines))
	}
}

// A newline-only line strips to an empty string and must not panic.
func TestTrimEOLEmpty(t *testing.T) {
	for _, s := range []string{"\n", "\r\n", ""} {
		if got := trimEOL([]byte(s)); len(got) != 0 {
			t.Fatalf("%q → %q", s, got)
		}
	}
}

// Cursor paging: lines after after, limit truncation and cap, next unchanged when there are no new lines,
// epoch included in the response.
func TestReadCursor(t *testing.T) {
	s := New()
	// nothing received yet: return empty, must not panic
	for _, after := range []int64{0, 5} {
		if r := s.Read(after, 10); len(r.Lines) != 0 || r.Skipped != 0 {
			t.Fatalf("空緩衝 after=%d: %v skipped=%d", after, seqs(r.Lines), r.Skipped)
		}
	}
	for i := 1; i <= 10; i++ {
		s.add(fmt.Sprintf("line %d", i))
	}
	if r := s.Read(-1, 3); fmt.Sprint(seqs(r.Lines)) != "[8 9 10]" || r.Next != 10 {
		t.Fatalf("tail: %v next=%d", seqs(r.Lines), r.Next)
	}
	if r := s.Read(4, 3); fmt.Sprint(seqs(r.Lines)) != "[5 6 7]" || r.Next != 7 {
		t.Fatalf("after=4: %v next=%d", seqs(r.Lines), r.Next)
	}
	if r := s.Read(10, 100); len(r.Lines) != 0 || r.Next != 10 {
		t.Fatalf("沒新行: %v next=%d", seqs(r.Lines), r.Next)
	}
	if r := s.Read(4, 3); r.Skipped != 0 {
		t.Fatalf("連續時 skipped 應為 0: %d", r.Skipped)
	}
	if r := s.Read(0, 5000); len(r.Lines) != 10 || r.Epoch == 0 {
		t.Fatalf("limit 超過上限應以上限計、epoch 要有值: %d 行 epoch=%d", len(r.Lines), r.Epoch)
	}
}

// Over real TCP: lines received, overlong lines truncated, a new connection replaces the old (which is
// closed), and connected becomes false after a disconnect.
func TestListen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	s := New()
	go func() { _ = s.Listen(ctx, addr) }()

	var c1 net.Conn
	for i := 0; i < 50 && c1 == nil; i++ {
		c1, _ = net.Dial("tcp", addr)
		time.Sleep(10 * time.Millisecond)
	}
	if c1 == nil {
		t.Fatal("連不上 sink")
	}
	fmt.Fprintf(c1, "first\r\n%s\nlast\n", strings.Repeat("y", maxLine+100))
	waitFor(t, func() bool { return len(s.Read(-1, 10).Lines) == 3 })
	r := s.Read(-1, 10)
	if r.Lines[0].Text != "first" || len(r.Lines[1].Text) != maxLine || r.Lines[2].Text != "last" || !r.Connected {
		t.Fatalf("收到的行不對: %q... connected=%v", r.Lines[0].Text, r.Connected)
	}

	c2, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c1.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c1.Read(make([]byte, 1)); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("舊連線應被新連線取代而關掉,err=%v", err)
	}
	fmt.Fprint(c2, "from second\n")
	waitFor(t, func() bool {
		r := s.Read(-1, 1)
		return len(r.Lines) == 1 && r.Lines[0].Text == "from second" && r.Connected
	})

	c2.Close()
	waitFor(t, func() bool { return !s.Read(-1, 1).Connected })
}

// Addresses: loopback and bridge interface IPs are allowed; 0.0.0.0, hostnames, physical interface IPs are not.
func TestValidateAddr(t *testing.T) {
	bridges := func() (map[string]bool, error) { return map[string]bool{"172.17.0.1": true}, nil }
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:5170", true},
		{"[::1]:5170", true},
		{"172.17.0.1:5170", true},
		{"0.0.0.0:5170", false},
		{"[::]:5170", false},
		{"10.0.0.5:5170", false},
		{"localhost:5170", false},
		{"127.0.0.1:0", false},
		{"127.0.0.1:1", true},
		{"127.0.0.1:65535", true},
		{"127.0.0.1:65536", false},
		{"127.0.0.1", false},
	} {
		if err := validateAddr(tc.addr, bridges); (err == nil) != tc.ok {
			t.Errorf("%s: err=%v want ok=%v", tc.addr, err, tc.ok)
		}
	}
}

func seqs(ls []api.NodeLogLine) []int64 {
	out := make([]int64, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Seq)
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等不到條件成立")
}

// Bridge detection: docker0 (veth members) is internal; br0 with a physical member and a bridge carrying the
// default route are not; a plain interface is not a bridge.
func TestInternalBridge(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("docker0/bridge")
	mk("docker0/brif/veth1")
	mk("veth1")
	mk("br0/bridge")
	mk("br0/brif/eth0")
	mk("eth0/device")
	mk("uplink/bridge") // a bridge with no members that carries the default route (the external interface in some virtualized environments)
	route := filepath.Join(root, "route")
	if err := os.WriteFile(route, []byte("Iface\tDestination\tGateway\nuplink\t00000000\t0100A8C0\ndocker0\t000011AC\t00000000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldNet, oldRoute := sysNet, procRoute
	sysNet, procRoute = root, route
	defer func() { sysNet, procRoute = oldNet, oldRoute }()
	if !internalBridge("docker0") {
		t.Error("docker0 應算內部 bridge")
	}
	if internalBridge("br0") {
		t.Error("掛了實體網卡的 br0 不能算內部")
	}
	if internalBridge("eth0") {
		t.Error("eth0 不是 bridge")
	}
	if internalBridge("uplink") {
		t.Error("承載預設路由的 bridge 不能算內部")
	}
}

type recSpool struct {
	mu  sync.Mutex
	got []string
}

func (r *recSpool) Append(text string) { r.mu.Lock(); r.got = append(r.got, text); r.mu.Unlock() }
func (r *recSpool) Mark(kind string)   { r.mu.Lock(); r.got = append(r.got, "#"+kind); r.mu.Unlock() }
func (r *recSpool) list() string       { r.mu.Lock(); defer r.mu.Unlock(); return fmt.Sprint(r.got) }

// The spool receives every line plus Besu connect/disconnect markers; an old connection replaced by a new
// one does not count as a disconnect.
func TestSpoolGetsLinesAndMarks(t *testing.T) {
	s := New()
	rs := &recSpool{}
	s.Spool = rs
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	go s.Listen(ctx, addr)
	var c1 net.Conn
	var err error
	for range 50 {
		if c1, err = net.Dial("tcp", addr); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(c1, "one")
	waitFor(t, func() bool { return rs.list() == "[#sink_connected one]" })
	c2, _ := net.Dial("tcp", addr) // replaces c1
	fmt.Fprintln(c2, "two")
	waitFor(t, func() bool { return rs.list() == "[#sink_connected one #sink_connected two]" })
	c2.Close()
	waitFor(t, func() bool { return rs.list() == "[#sink_connected one #sink_connected two #sink_disconnected]" })
	c1.Close()
}
