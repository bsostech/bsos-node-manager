package logspool

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"besu-manager/agent/internal/api"
)

// fakePlatform simulates how an Enterprise platform pulls: send the last stored (epoch, seq), verify hashes,
// decode and keep every line.
type fakePlatform struct {
	t      *testing.T
	h      http.Handler
	cursor *Pos
	recs   []record
	epochs []int64 // epoch of each block
}

func (p *fakePlatform) pull() {
	p.t.Helper()
	for {
		url := "/logblocks"
		if p.cursor != nil {
			url += fmt.Sprintf("?after=%d.%d", p.cursor.Epoch, p.cursor.Seq)
		}
		w := httptest.NewRecorder()
		p.h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK {
			p.t.Fatalf("/logblocks %d: %s", w.Code, w.Body)
		}
		var out api.NodeLogBlocks
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			p.t.Fatal(err)
		}
		dec, _ := zstd.NewReader(nil)
		for _, b := range out.Blocks {
			sum := sha256.Sum256(b.Data)
			if hex.EncodeToString(sum[:]) != b.Sha256 {
				p.t.Fatalf("雜湊對不上: %+v", b)
			}
			raw, err := dec.DecodeAll(b.Data, nil)
			if err != nil {
				p.t.Fatal(err)
			}
			sc := bufio.NewScanner(bytes.NewReader(raw))
			for sc.Scan() {
				var r record
				if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
					p.t.Fatal(err)
				}
				p.recs = append(p.recs, r)
			}
			p.epochs = append(p.epochs, b.Epoch)
			p.cursor = &Pos{b.Epoch, b.SeqLast}
		}
		if !out.More {
			return
		}
	}
}

func (p *fakePlatform) lines() []string {
	var out []string
	for _, r := range p.recs {
		if r.L != nil {
			out = append(out, *r.L)
		}
	}
	return out
}

func (p *fakePlatform) marks(kind string) []map[string]any {
	var out []map[string]any
	for _, r := range p.recs {
		if r.Mark != nil && r.Mark["kind"] == kind {
			out = append(out, r.Mark)
		}
	}
	return out
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func open(t *testing.T, dir string, max int64, c *clock) *Spool {
	t.Helper()
	s, err := Open(dir, max)
	if err != nil {
		t.Fatal(err)
	}
	s.now = c.now
	return s
}

// Nothing touches disk before the platform pulls (a Core platform never pulls).
func TestInactiveUntilFirstPull(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logspool")
	s := open(t, dir, DefaultMax, &clock{time.Now()})
	s.Append("hello")
	s.Mark("sink_connected")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("還沒被拉過不該建目錄: %v", err)
	}
	(&fakePlatform{t: t, h: s}).pull()
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("目錄應 0700: %v %v", st, err)
	}
}

// The platform stops pulling for a while, then catches up completely with contiguous seqs and no duplicates;
// pulled blocks are deleted from the spool.
func TestCatchUpWithoutDuplicates(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	s := open(t, dir, DefaultMax, c)
	p := &fakePlatform{t: t, h: s}
	p.pull() // activates; no sealed blocks yet

	big := strings.Repeat("x", 4000)
	for i := range 3000 { // about 12 MB: sealed into several blocks, more than one response (more)
		s.Append(fmt.Sprintf("%d %s", i, big))
		c.t = c.t.Add(time.Millisecond)
	}
	c.t = c.t.Add(sealAge)
	s.mu.Lock()
	if err := s.sealCur(); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()

	p.pull()
	p.pull() // pull again: nothing new, no duplicates
	got := p.lines()
	if len(got) != 3000 {
		t.Fatalf("收到 %d 行", len(got))
	}
	for i, l := range got {
		if !strings.HasPrefix(l, fmt.Sprintf("%d ", i)) {
			t.Fatalf("第 %d 行順序不對: %.20s", i, l)
		}
	}
	for i := 1; i < len(p.recs); i++ {
		if p.recs[i].S != p.recs[i-1].S+1 {
			t.Fatalf("序號不連續: %d → %d", p.recs[i-1].S, p.recs[i].S)
		}
	}
	if p.recs[0].Mark["kind"] != "epoch" || p.recs[0].S != 1 {
		t.Fatalf("每個 epoch 第一筆是 epoch 標記: %+v", p.recs[0])
	}
	// pull once more with the last cursor; all blocks the platform has stored must be deleted
	p.pull()
	if zs, _ := filepath.Glob(filepath.Join(dir, "*.zst")); len(zs) != 0 {
		t.Fatalf("拉走的區塊沒刪: %v", zs)
	}
}

// A full spool drops the oldest and leaves an overflow marker with the dropped range.
func TestOverflowMark(t *testing.T) {
	c := &clock{time.Now()}
	s := open(t, t.TempDir(), 3<<20, c)
	p := &fakePlatform{t: t, h: s}
	p.pull()
	rnd := make([]byte, 3000) // random content does not compress, so blocks come close to 1 MB
	for i := range 1500 {
		_, _ = rand.Read(rnd)
		s.Append(fmt.Sprintf("%d %x", i, rnd))
	}
	c.t = c.t.Add(sealAge)
	s.mu.Lock()
	_ = s.sealCur()
	s.mu.Unlock()
	if s.total > 3<<20 {
		t.Fatalf("暫存超過上限: %d", s.total)
	}
	c.t = c.t.Add(sealAge)
	s.Append("after")
	s.mu.Lock()
	_ = s.sealCur()
	s.mu.Unlock()
	p.pull()
	ov := p.marks("overflow")
	if len(ov) == 0 {
		t.Fatal("沒有 overflow 標記")
	}
	if ov[0]["seqFrom"].(float64) != 1 || ov[0]["seqTo"].(float64) < 2 {
		t.Fatalf("overflow 範圍不對: %v", ov[0])
	}
	if p.recs[0].S <= int64(ov[0]["seqTo"].(float64)) {
		t.Fatalf("收到的應該在丟掉的範圍之後: 第一筆 %d, 丟到 %v", p.recs[0].S, ov[0]["seqTo"])
	}
}

// Dropping several blocks at once (same epoch): one overflow marker whose range covers all dropped blocks.
func TestOverflowMarkSpansBlocks(t *testing.T) {
	s := open(t, t.TempDir(), DefaultMax, &clock{time.Now()})
	p := &fakePlatform{t: t, h: s}
	p.pull()
	for i := range 3 {
		s.Append(fmt.Sprint(i))
		seal(t, s)
	}
	s.mu.Lock()
	from, to := s.blocks[0].SeqFirst, s.blocks[1].SeqLast
	s.max = 1 // keep the last block, drop the first two at once
	s.enforceMax()
	kept := len(s.blocks)
	s.mu.Unlock()
	if kept != 1 {
		t.Fatalf("至少留一塊,剩 %d 塊", kept)
	}
	seal(t, s)
	p.pull()
	ov := p.marks("overflow")
	if len(ov) != 1 || int64(ov[0]["seqFrom"].(float64)) != from || int64(ov[0]["seqTo"].(float64)) != to {
		t.Fatalf("overflow 標記不對: %v,應為 %d–%d", ov, from, to)
	}
}

// A total exactly at the limit is not over: no blocks dropped, no overflow marker.
func TestEnforceMaxAtLimitKeepsBlocks(t *testing.T) {
	s := open(t, t.TempDir(), DefaultMax, &clock{time.Now()})
	(&fakePlatform{t: t, h: s}).pull()
	for i := range 2 {
		s.Append(fmt.Sprint(i))
		seal(t, s)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := s.seq
	s.max = s.total
	s.enforceMax()
	if len(s.blocks) != 2 || s.seq != seq {
		t.Fatalf("剛好等於上限:剩 %d 塊、seq %d → %d", len(s.blocks), seq, s.seq)
	}
}

// Dropped blocks span two epochs: one overflow marker per epoch, each with its own range.
func TestOverflowMarkPerEpoch(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Now()}
	s := open(t, dir, DefaultMax, c)
	(&fakePlatform{t: t, h: s}).pull()
	s.Append("e1")
	seal(t, s)
	s2 := open(t, dir, DefaultMax, c) // restart: epoch increments
	for _, l := range []string{"e2a", "e2b"} {
		s2.Append(l)
		seal(t, s2)
	}
	s2.mu.Lock()
	if len(s2.blocks) != 3 {
		s2.mu.Unlock()
		t.Fatalf("應有 3 塊: %+v", s2.blocks)
	}
	b0, b1 := s2.blocks[0], s2.blocks[1]
	s2.max = 1 // drop the epoch 1 block and the first epoch 2 block
	s2.enforceMax()
	s2.mu.Unlock()
	seal(t, s2)
	p := &fakePlatform{t: t, h: s2}
	p.pull()
	ov := p.marks("overflow")
	if len(ov) != 2 {
		t.Fatalf("應有 2 筆 overflow 標記: %v", ov)
	}
	for i, b := range []meta{b0, b1} {
		if int64(ov[i]["epoch"].(float64)) != b.Epoch || int64(ov[i]["seqFrom"].(float64)) != b.SeqFirst || int64(ov[i]["seqTo"].(float64)) != b.SeqLast {
			t.Errorf("第 %d 筆 overflow %v,want epoch %d %d–%d", i, ov[i], b.Epoch, b.SeqFirst, b.SeqLast)
		}
	}
}

// Blocks on disk but no marker file (Open does not activate): the first pull activates and returns them.
func TestFirstPullActivatesAndReturnsExisting(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Now()}
	s := open(t, dir, DefaultMax, c)
	(&fakePlatform{t: t, h: s}).pull()
	s.Append("on disk")
	seal(t, s)
	if err := os.Remove(filepath.Join(dir, activeFile)); err != nil {
		t.Fatal(err)
	}
	s2 := open(t, dir, DefaultMax, c)
	p := &fakePlatform{t: t, h: s2}
	p.pull()
	if fmt.Sprint(p.lines()) != "[on disk]" {
		t.Fatalf("收到 %v", p.lines())
	}
}

// A response holds up to maxResponse in total (exactly equal still fits); the rest waits for the next pull
// with more set; at least one block.
func TestPickResponseLimit(t *testing.T) {
	s := open(t, t.TempDir(), DefaultMax, &clock{time.Now()})
	(&fakePlatform{t: t, h: s}).pull()
	for _, tc := range []struct {
		sizes []int64
		n     int
		more  bool
	}{
		{[]int64{3 << 20, 1 << 20, 1 << 20}, 2, true}, // 3+1 is exactly 4 MB and fits; one more block exceeds it
		{[]int64{1 << 20, 1 << 20, 1 << 20}, 3, false},
		{[]int64{5 << 20, 1 << 20}, 1, true}, // a single oversized block is still returned
	} {
		s.mu.Lock()
		s.blocks = nil
		for i, sz := range tc.sizes {
			s.blocks = append(s.blocks, meta{Epoch: s.epoch, SeqFirst: int64(i + 1), SeqLast: int64(i + 1), Size: sz})
		}
		s.mu.Unlock()
		pick, more, err := s.pick(nil)
		if err != nil || len(pick) != tc.n || more != tc.more {
			t.Errorf("sizes %v: 回 %d 塊 more=%v err=%v,want %d / %v", tc.sizes, len(pick), more, err, tc.n, tc.more)
		}
	}
}

// Agent restart: the leftover unsealed segment is sealed first, epoch increments, and the new epoch starts at
// 1 with an epoch marker, so the platform can tell.
func TestRestartBumpsEpoch(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Now()}
	s := open(t, dir, DefaultMax, c)
	p := &fakePlatform{t: t, h: s}
	p.pull()
	s.Append("before restart")
	s.Mark("sink_disconnected")
	// simulate power loss: current segment unsealed with a partial line at the end
	f, _ := os.OpenFile(s.cur.Name(), os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString(`{"s":99,"t":"2026`)
	f.Close()

	s2 := open(t, dir, DefaultMax, c)
	if s2.epoch != s.epoch+1 {
		t.Fatalf("epoch 應加一: %d → %d", s.epoch, s2.epoch)
	}
	// block sealed from the leftover: seq range is the epoch marker (1) to sink_disconnected (3); the partial
	// line does not count
	if len(s2.blocks) != 1 || s2.blocks[0].Epoch != s.epoch || s2.blocks[0].SeqFirst != 1 || s2.blocks[0].SeqLast != 3 {
		t.Fatalf("殘段區塊: %+v", s2.blocks)
	}
	s2.Append("after restart")
	c.t = c.t.Add(sealAge)
	s2.mu.Lock()
	_ = s2.sealCur()
	s2.mu.Unlock()
	p.h = s2
	p.pull()
	if fmt.Sprint(p.lines()) != "[before restart after restart]" {
		t.Fatalf("收到 %v", p.lines())
	}
	if len(p.epochs) != 2 || p.epochs[1] != p.epochs[0]+1 {
		t.Fatalf("區塊的 epoch: %v", p.epochs)
	}
	e := p.marks("epoch")
	if len(e) != 2 || e[1]["epoch"].(float64) != float64(s2.epoch) {
		t.Fatalf("epoch 標記: %v", e)
	}
	var first record
	for _, r := range p.recs {
		if r.Mark != nil && r.Mark["kind"] == "epoch" && int64(r.Mark["epoch"].(float64)) == s2.epoch {
			first = r
		}
	}
	if first.S != 1 {
		t.Fatalf("新 epoch 序號從 1 起算: %+v", first)
	}
}

func TestParsePosAndSize(t *testing.T) {
	if p, err := ParsePos("7.1201"); err != nil || p != (Pos{7, 1201}) {
		t.Fatal(p, err)
	}
	for _, bad := range []string{"7", "a.1", "7.-1", ""} {
		if _, err := ParsePos(bad); err == nil {
			t.Fatalf("%q 應該不收", bad)
		}
	}
	for in, want := range map[string]int64{"512MB": 512 << 20, "1G": 1 << 30, "2048": 2048, "64k": 64 << 10} {
		if n, err := ParseSize(in); err != nil || n != want {
			t.Fatalf("%s: %d %v", in, n, err)
		}
	}
	if _, err := ParseSize("0"); err == nil {
		t.Fatal("0 應該不收")
	}
}

// Cursor newer than anything this agent wrote (state directory wiped, epoch restarted): delete nothing,
// start from the oldest.
func TestFutureCursorIgnored(t *testing.T) {
	c := &clock{time.Now()}
	s := open(t, t.TempDir(), DefaultMax, c)
	p := &fakePlatform{t: t, h: s}
	p.pull()
	s.Append("kept")
	s.mu.Lock()
	_ = s.sealCur()
	s.mu.Unlock()
	p.cursor = &Pos{Epoch: 9, Seq: 100} // a previous agent's cursor
	p.pull()
	if fmt.Sprint(p.lines()) != "[kept]" {
		t.Fatalf("收到 %v", p.lines())
	}
}

func seal(t *testing.T, s *Spool) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sealCur(); err != nil {
		t.Fatal(err)
	}
}

// After a seal failure (directory temporarily unwritable), keep writing the same segment without truncating
// even if reopening fails too; after recovery the sealed block's seq range covers every line before and
// after the failure.
func TestSealFailureKeepsSegment(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Now()}
	s := open(t, dir, DefaultMax, c)
	p := &fakePlatform{t: t, h: s}
	p.pull()
	s.Append("one")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err := s.sealCur()
	s.mu.Unlock()
	if err == nil {
		t.Fatal("目錄不能寫,封應該失敗")
	}
	s.Append("two")
	_ = os.Chmod(dir, 0o700)
	// also simulate "reopen fails too": the current segment has no open file, and the next line must append
	// to the original file without truncating
	s.mu.Lock()
	s.cur.Close()
	s.cur = nil
	s.mu.Unlock()
	s.Append("three")
	seal(t, s)
	p.pull()
	if fmt.Sprint(p.lines()) != "[one two three]" {
		t.Fatalf("收到 %v", p.lines())
	}
	last := p.recs[len(p.recs)-1]
	if p.cursor.Seq != last.S {
		t.Fatalf("區塊的 seqLast %d 應等於最後一筆 %d", p.cursor.Seq, last.S)
	}
}

// Blocks not matching their header at startup (.zst missing, truncated, or modified): dropped with a corrupt
// marker, later blocks still sent.
func TestCorruptBlockDroppedOnStart(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Now()}
	s := open(t, dir, DefaultMax, c)
	p := &fakePlatform{t: t, h: s}
	p.pull()
	for _, l := range []string{"a", "b", "c", "d"} {
		s.Append(l)
		seal(t, s)
	}
	bs := append([]meta(nil), s.blocks...)
	base := func(i int) string { return filepath.Join(dir, bs[i].name()) }
	_ = os.Remove(base(0) + ".zst")                             // missing
	_ = os.Truncate(base(1)+".zst", 3)                          // truncated
	_ = os.WriteFile(base(2)+".zst", []byte("tampered"), 0o600) // modified (size differs too)

	s2 := open(t, dir, DefaultMax, c)
	s2.Append("e")
	seal(t, s2)
	p.h = s2
	p.pull()
	if fmt.Sprint(p.lines()) != "[d e]" {
		t.Fatalf("收到 %v", p.lines())
	}
	if n := len(p.marks("corrupt")); n != 3 {
		t.Fatalf("應有 3 筆 corrupt 標記: %v", p.marks("corrupt"))
	}
}

// A corrupt line in the middle of the leftover segment: the head is sealed, the rest kept as .bad with a
// corrupt marker, not silently truncated.
func TestLeftoverMiddleCorruption(t *testing.T) {
	dir := t.TempDir()
	c := &clock{time.Now()}
	s := open(t, dir, DefaultMax, c)
	p := &fakePlatform{t: t, h: s}
	p.pull()
	s.Append("good")
	f, _ := os.OpenFile(s.cur.Name(), os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString("garbage\n" + `{"s":9,"t":"2026-10-14T00:00:00Z","l":"after"}` + "\n")
	f.Close()

	s2 := open(t, dir, DefaultMax, c)
	seal(t, s2)
	p.h = s2
	p.pull()
	if fmt.Sprint(p.lines()) != "[good]" {
		t.Fatalf("收到 %v", p.lines())
	}
	bad, _ := filepath.Glob(filepath.Join(dir, "*.bad"))
	if len(bad) != 1 {
		t.Fatalf("後段應留成 .bad: %v", bad)
	}
	if b, _ := os.ReadFile(bad[0]); !strings.Contains(string(b), `"after"`) {
		t.Fatalf(".bad 內容: %s", b)
	}
	if m := p.marks("corrupt"); len(m) != 1 || m[0]["seqAfter"].(float64) != 2 {
		t.Fatalf("corrupt 標記: %v", m)
	}
}
