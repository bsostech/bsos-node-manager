// Package logspool writes Besu logs to a spool in the agent state directory, sealed into zstd blocks, for an
// Enterprise platform to pull through the tunnel.
//
// Nothing touches disk until the platform first pulls: a Core platform never pulls, so the agent never writes
// files. On the first /logblocks it leaves a marker file in logspool/ and writes on every start after that.
// The after the platform sends in the request is the acknowledgement; blocks before after are deleted.
package logspool

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"besu-manager/agent/internal/api"
	"besu-manager/agent/internal/durable"
)

const (
	// DefaultMax: spool limit (total size of sealed blocks), about 50 days of INFO.
	DefaultMax = 512 << 20
	// sealBytes / sealAge: the current segment is sealed into a block at 1 MB or 60 seconds.
	sealBytes = 1 << 20
	sealAge   = 60 * time.Second
	// maxResponse: cap on total block size per /logblocks response (compressed; still within the platform's
	// 8 MB response limit after base64).
	maxResponse = 4 << 20

	activeFile = "active"
	epochFile  = "epoch"
)

// Pos is (epoch, seq), the platform's cursor.
type Pos struct{ Epoch, Seq int64 }

func (p Pos) after(q Pos) bool { return p.Epoch > q.Epoch || (p.Epoch == q.Epoch && p.Seq > q.Seq) }

// ParsePos parses "<epoch>.<seq>".
func ParsePos(s string) (Pos, error) {
	e, q, ok := strings.Cut(s, ".")
	if ok {
		epoch, err1 := strconv.ParseInt(e, 10, 64)
		seq, err2 := strconv.ParseInt(q, 10, 64)
		if err1 == nil && err2 == nil && epoch >= 0 && seq >= 0 {
			return Pos{epoch, seq}, nil
		}
	}
	return Pos{}, fmt.Errorf("after must be <epoch>.<seq>: %q", s)
}

// meta is a sealed block's header, the .json stored next to the .zst. The .json is written last; a block is
// sealed only once its .json exists.
type meta struct {
	Epoch     int64     `json:"epoch"`
	SeqFirst  int64     `json:"seqFirst"`
	SeqLast   int64     `json:"seqLast"`
	RecvFirst time.Time `json:"recvFirst"`
	RecvLast  time.Time `json:"recvLast"`
	Sha256    string    `json:"sha256"`
	Size      int64     `json:"size"`
}

func (m meta) name() string { return fmt.Sprintf("%020d-%020d", m.Epoch, m.SeqFirst) }

// record is one line in the spool. l is the original text; mark is a marker the agent writes itself (gap,
// Besu connection, epoch start).
type record struct {
	S    int64          `json:"s"`
	T    string         `json:"t"`
	L    *string        `json:"l,omitempty"`
	Mark map[string]any `json:"mark,omitempty"`
}

// Spool: the zero value is not usable; use Open.
type Spool struct {
	dir string
	max int64
	now func() time.Time
	enc *zstd.Encoder

	mu        sync.Mutex
	active    bool
	epoch     int64
	seq       int64
	cur       *os.File // current segment; nil = no content yet
	curMeta   meta     // seq and time of the current segment (Sha256, Size computed at seal time)
	curBytes  int64
	curOpened time.Time
	blocks    []meta // sealed, ascending by (epoch, seqFirst)
	total     int64
	lost      []map[string]any // ranges dropped as corrupt during activation; one corrupt marker each is written after the epoch marker
}

// Open starts writing if dir has the marker file (the platform has pulled); otherwise it waits for the first
// /logblocks. It creates no directories.
func Open(dir string, max int64) (*Spool, error) {
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, err
	}
	s := &Spool{dir: dir, max: max, now: time.Now, enc: enc}
	if _, err := os.Stat(filepath.Join(dir, activeFile)); err == nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.activate(); err != nil {
			enc.Close()
			return nil, err
		}
	}
	return s, nil
}

// activate creates the directory, leaves the marker, reloads sealed blocks, seals the segment left unsealed
// last time, increments epoch and writes the epoch marker. The caller holds the lock.
func (s *Spool) activate() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return err
	}
	if err := durable.WriteFile(filepath.Join(s.dir, activeFile), nil); err != nil {
		return err
	}
	if err := s.loadBlocks(); err != nil {
		return err
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.dir, "cur-*.jsonl"))
	for _, f := range leftovers {
		if err := s.sealLeftover(f); err != nil {
			return err
		}
	}
	epoch, err := s.bumpEpoch()
	if err != nil {
		return err
	}
	s.active, s.epoch, s.seq = true, epoch, 0
	if err := s.write(record{Mark: map[string]any{"kind": "epoch", "epoch": epoch}}); err != nil {
		return err
	}
	for _, m := range s.lost {
		if err := s.write(record{Mark: m}); err != nil {
			return err
		}
	}
	s.lost = nil
	s.enforceMax()
	return nil
}

// loadBlocks reloads sealed blocks, checking that each .zst exists and its size and hash match the header.
// Mismatched blocks are dropped as a whole and their range recorded (corrupt marker): if kept, every platform
// pull would fail to read the file or verify the hash, and later blocks could never be sent.
func (s *Spool) loadBlocks() error {
	s.blocks, s.total, s.lost = nil, 0, nil // a retry after activate fails reads again
	metas, _ := filepath.Glob(filepath.Join(s.dir, "*.json"))
	have := map[string]bool{}
	for _, f := range metas {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		zst := strings.TrimSuffix(f, ".json") + ".zst"
		var m meta
		if err := json.Unmarshal(b, &m); err != nil {
			log.Printf("logspool: corrupt block header, dropping %s: %v", f, err)
			_ = os.Remove(f)
			_ = os.Remove(zst)
			s.lost = append(s.lost, map[string]any{"kind": "corrupt", "file": filepath.Base(f)})
			continue
		}
		data, err := os.ReadFile(zst)
		sum := sha256.Sum256(data)
		if err != nil || int64(len(data)) != m.Size || hex.EncodeToString(sum[:]) != m.Sha256 {
			log.Printf("logspool: block %s does not match its header, dropping (%v)", zst, err)
			_ = os.Remove(f)
			_ = os.Remove(zst)
			s.lost = append(s.lost, map[string]any{"kind": "corrupt", "epoch": m.Epoch, "seqFrom": m.SeqFirst, "seqTo": m.SeqLast})
			continue
		}
		s.blocks = append(s.blocks, m)
		s.total += m.Size
		have[m.name()] = true
	}
	// .zst without .json = power lost mid-seal; the content is still in the matching cur file (cur is deleted
	// only after sealing), so just drop it.
	zs, _ := filepath.Glob(filepath.Join(s.dir, "*.zst"))
	for _, f := range zs {
		if !have[strings.TrimSuffix(filepath.Base(f), ".zst")] {
			_ = os.Remove(f)
		}
	}
	s.sortBlocks()
	return nil
}

func (s *Spool) sortBlocks() {
	sort.Slice(s.blocks, func(i, j int) bool {
		return Pos{s.blocks[j].Epoch, s.blocks[j].SeqFirst}.after(Pos{s.blocks[i].Epoch, s.blocks[i].SeqFirst})
	})
}

// sealLeftover seals the segment the previous run left unsealed (file name carries the epoch) after reading
// its seq and time; an empty one is deleted.
// An incomplete last line (power lost mid-write) is truncated. If the middle cannot be read, the readable
// head is still sealed and the rest is renamed to .bad and left on disk for inspection, with a corrupt marker;
// complete records after it must not be silently truncated. If reading the file itself fails, it returns the
// error and leaves the file untouched to retry on the next activation.
func (s *Spool) sealLeftover(path string) error {
	epoch, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "cur-"), ".jsonl"), 10, 64)
	if err != nil {
		return os.Remove(path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	m := meta{Epoch: epoch}
	var n int64
	good := 0 // where the readable head ends
	for good < len(raw) {
		end := bytes.IndexByte(raw[good:], '\n')
		if end < 0 {
			break // last line has no newline = half-written
		}
		var r record
		if json.Unmarshal(raw[good:good+end], &r) != nil {
			break
		}
		t, _ := time.Parse(time.RFC3339Nano, r.T)
		if m.SeqFirst == 0 {
			m.SeqFirst, m.RecvFirst = r.S, t
		}
		m.SeqLast, m.RecvLast = r.S, t
		n++
		good += end + 1
	}
	if rest := raw[good:]; bytes.IndexByte(rest, '\n') >= 0 {
		// more is broken than just the last partial line
		bad := path + ".bad"
		if err := durable.WriteFile(bad, rest); err != nil {
			return err
		}
		log.Printf("logspool: %s has unreadable records in the middle, %d bytes kept in %s", path, len(rest), bad)
		s.lost = append(s.lost, map[string]any{"kind": "corrupt", "epoch": epoch, "seqAfter": m.SeqLast, "file": filepath.Base(bad)})
	}
	if n == 0 {
		return os.Remove(path)
	}
	for _, b := range s.blocks {
		if b.Epoch == m.Epoch && b.SeqFirst == m.SeqFirst {
			return os.Remove(path) // already sealed; power was lost before the source was deleted
		}
	}
	if err := os.Truncate(path, int64(good)); err != nil {
		return err
	}
	return s.sealFile(path, m)
}

// bumpEpoch increments a persistent counter (write a temp file then rename, so a power loss never leaves half a
// number). Time is not used: a clock rollback would make a new epoch smaller than an old one.
// If the counter file has no readable number, the result is at least larger than the largest epoch in the
// spool, so same-named blocks are never overwritten. The caller has already run loadBlocks.
func (s *Spool) bumpEpoch() (int64, error) {
	path := filepath.Join(s.dir, epochFile)
	var n int64
	if b, err := os.ReadFile(path); err == nil {
		n, _ = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	for _, b := range s.blocks {
		n = max(n, b.Epoch)
	}
	n++
	tmp := path + ".tmp"
	if err := durable.WriteFile(tmp, []byte(strconv.FormatInt(n, 10))); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, err
	}
	return n, durable.SyncDir(s.dir)
}

// Append takes one line sent by Besu. Nothing is written until activated (the platform has not pulled yet).
// Write failures are only logged: logs are optional and must not break ingestion.
func (s *Spool) Append(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return
	}
	if err := s.write(record{L: &text}); err != nil {
		log.Printf("logspool: write to spool: %v", err)
	}
}

// Mark writes an agent marker (sink_connected, sink_disconnected), which takes a seq like a log line.
func (s *Spool) Mark(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return
	}
	if err := s.write(record{Mark: map[string]any{"kind": kind}}); err != nil {
		log.Printf("logspool: write to spool: %v", err)
	}
}

// write assigns a seq, stamps the receive time (UTC, microseconds), and appends to the current segment,
// sealing it when full. The caller holds the lock.
func (s *Spool) write(r record) error {
	now := s.now().UTC().Truncate(time.Microsecond)
	s.seq++
	r.S, r.T = s.seq, now.Format(time.RFC3339Nano)
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if s.cur == nil {
		// do not truncate: if sealing and even reopening failed, the unsealed segment is still in the file and the
		// header (curMeta etc.) still belongs to it, so keep appending
		f, err := os.OpenFile(filepath.Join(s.dir, fmt.Sprintf("cur-%d.jsonl", s.epoch)), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		s.cur = f
		if st, err := f.Stat(); err != nil || st.Size() == 0 {
			s.curBytes, s.curOpened = 0, now
			s.curMeta = meta{Epoch: s.epoch, SeqFirst: s.seq, RecvFirst: now}
		}
	}
	if _, err := s.cur.Write(append(b, '\n')); err != nil {
		return err
	}
	s.curMeta.SeqLast, s.curMeta.RecvLast = s.seq, now
	s.curBytes += int64(len(b)) + 1
	if s.curBytes >= sealBytes {
		return s.sealCur()
	}
	return nil
}

// Run checks every few seconds and seals the current segment once it is 60 seconds old. It lives as long as
// the agent.
func (s *Spool) Run() {
	for range time.Tick(5 * time.Second) {
		s.mu.Lock()
		if s.cur != nil && s.now().Sub(s.curOpened) >= sealAge {
			if err := s.sealCur(); err != nil {
				log.Printf("logspool: seal block: %v", err)
			}
		}
		s.mu.Unlock()
	}
}

func (s *Spool) sealCur() error {
	path := s.cur.Name()
	if err := s.cur.Close(); err != nil {
		return err
	}
	s.cur = nil
	if err := s.sealFile(path, s.curMeta); err != nil {
		// sealing failed (disk full etc.): keep writing the same segment and retry on the next line. curMeta,
		// curBytes, curOpened are untouched and still describe this segment
		if f, oerr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600); oerr == nil {
			s.cur = f
		}
		return err
	}
	s.enforceMax()
	return nil
}

// sealFile compresses, hashes, writes .zst then .json (both fsynced), and finally deletes the source segment.
func (s *Spool) sealFile(path string, m meta) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data := s.enc.EncodeAll(raw, nil)
	sum := sha256.Sum256(data)
	m.Sha256, m.Size = hex.EncodeToString(sum[:]), int64(len(data))
	base := filepath.Join(s.dir, m.name())
	if err := durable.WriteFile(base+".zst", data); err != nil {
		return err
	}
	hb, _ := json.Marshal(m)
	if err := durable.WriteFile(base+".json", hb); err != nil {
		return err
	}
	if err := durable.SyncDir(s.dir); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	s.blocks = append(s.blocks, m)
	s.total += m.Size
	s.sortBlocks()
	return nil
}

// enforceMax drops the oldest sealed blocks past the limit (keeping at least one), writing one overflow marker
// per epoch for the dropped range. The caller holds the lock.
func (s *Spool) enforceMax() {
	var dropped []meta
	for s.total > s.max && len(s.blocks) > 1 {
		dropped = append(dropped, s.blocks[0])
		s.remove(0)
	}
	for i := 0; i < len(dropped); {
		j := i
		for j+1 < len(dropped) && dropped[j+1].Epoch == dropped[i].Epoch {
			j++
		}
		mark := map[string]any{"kind": "overflow", "epoch": dropped[i].Epoch, "seqFrom": dropped[i].SeqFirst, "seqTo": dropped[j].SeqLast}
		if err := s.write(record{Mark: mark}); err != nil {
			log.Printf("logspool: write overflow marker: %v", err)
		}
		i = j + 1
	}
}

func (s *Spool) remove(i int) {
	m := s.blocks[i]
	base := filepath.Join(s.dir, m.name())
	_ = os.Remove(base + ".json") // delete the header first: if interrupted, the leftover .zst is cleaned up as an orphan on next start
	_ = os.Remove(base + ".zst")
	s.total -= m.Size
	s.blocks = append(s.blocks[:i], s.blocks[i+1:]...)
}

// Blocks deletes blocks before after (the platform has stored them) and returns sealed blocks after after, up
// to the total size cap (at least one).
// after == nil means the platform has stored nothing for this node yet, so it starts from the oldest. An after
// newer than anything this agent wrote (state directory wiped, epoch restarted) is a previous agent's cursor:
// nothing is deleted and it starts from the oldest; seeing blocks before its cursor tells the platform there
// was a reset.
// The first call activates the spool.
func (s *Spool) Blocks(after *Pos) (api.NodeLogBlocks, error) {
	pick, more, err := s.pick(after)
	if err != nil {
		return api.NodeLogBlocks{}, err
	}
	// read files without the lock: the Besu log path (Append) need not wait for several MB of reads here. If a
	// block is evicted by the spool limit mid-read, return an error and the platform pulls again next round.
	out := api.NodeLogBlocks{Blocks: make([]api.NodeLogBlock, 0, len(pick)), More: more}
	for _, m := range pick {
		data, err := os.ReadFile(filepath.Join(s.dir, m.name()+".zst"))
		if err != nil {
			return api.NodeLogBlocks{}, err
		}
		out.Blocks = append(out.Blocks, api.NodeLogBlock{
			Epoch: m.Epoch, SeqFirst: m.SeqFirst, SeqLast: m.SeqLast,
			RecvFirst: m.RecvFirst, RecvLast: m.RecvLast, Sha256: m.Sha256, Data: data,
		})
	}
	out.AgentNow = s.now().UTC()
	return out, nil
}

// pick decides under the lock what to delete and what to return (headers only).
func (s *Spool) pick(after *Pos) ([]meta, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		if err := s.activate(); err != nil {
			return nil, false, err
		}
	}
	if after != nil && after.after(Pos{s.epoch, s.seq}) {
		after = nil
	}
	if after != nil {
		for len(s.blocks) > 0 && !(Pos{s.blocks[0].Epoch, s.blocks[0].SeqLast}).after(*after) {
			s.remove(0)
		}
	}
	var pick []meta
	var size int64
	for _, m := range s.blocks {
		if after != nil && !(Pos{m.Epoch, m.SeqLast}).after(*after) {
			continue
		}
		if len(pick) > 0 && size+m.Size > maxResponse {
			return pick, true, nil
		}
		pick = append(pick, m)
		size += m.Size
	}
	return pick, false, nil
}

// ServeHTTP handles GET /logblocks?after=<epoch>.<seq>. The platform calls it through the tunnel (without
// X-Target).
func (s *Spool) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var after *Pos
	if v := r.URL.Query().Get("after"); v != "" {
		p, err := ParsePos(v)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		after = &p
	}
	out, err := s.Blocks(after)
	if err != nil {
		log.Printf("logspool: %v", err)
		http.Error(w, "log spool I/O error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// ParseSize parses LOG_SPOOL_MAX, a byte count with optional K / M / G (or KB / MB / GB).
func ParseSize(v string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(v))
	t = strings.TrimSuffix(t, "B")
	mult := int64(1)
	switch {
	case strings.HasSuffix(t, "K"):
		mult, t = 1<<10, strings.TrimSuffix(t, "K")
	case strings.HasSuffix(t, "M"):
		mult, t = 1<<20, strings.TrimSuffix(t, "M")
	case strings.HasSuffix(t, "G"):
		mult, t = 1<<30, strings.TrimSuffix(t, "G")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("LOG_SPOOL_MAX must be a positive size (for example 512MB): %q", v)
	}
	return n * mult, nil
}
