// Package logsink receives logs Besu sends via the log4j2 Socket appender and keeps them in memory for the
// platform to read through the tunnel; once an Enterprise platform has pulled, it also writes a copy to the
// spool (logspool).
//
// The agent does not parse content: it stores whatever arrives line by line and drops the oldest past the
// limit. The intake binds only 127.0.0.1 by default; to let containers on a bridge network send, it can bind
// the IP of a virtual bridge interface on this machine (docker0 and the like), but never 0.0.0.0 or a
// physical interface IP: this intake has no authentication and must not be reachable from other machines.
package logsink

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"besu-manager/agent/internal/api"
)

const (
	// DefaultAddr is the default intake address. When Besu runs directly on the host or in a container on the
	// host network, its config sends here.
	DefaultAddr = "127.0.0.1:5170"
	// maxBytes: the buffer keeps only this many of the newest bytes (about 2 MB) and drops older ones. This is
	// for viewing recent logs, not for retention.
	maxBytes = 2 << 20
	// maxLine: per-line limit; the excess is truncated (Java stack traces are multi-line and do not hit it;
	// what hits it is abnormal input).
	maxLine = 64 << 10
	// lineOverhead: per-line accounting cost besides the text. Empty lines must cost something too, otherwise a
	// run of newlines could grow the line count without bound.
	lineOverhead = 64
	// defaultLimit / maxLimit: how many lines GET /logs returns at once.
	defaultLimit = 500
	maxLimit     = 1000
)

type line struct {
	seq  int64
	text string
}

// Sink is the log buffer plus intake. The zero value is not usable; use New.
type Sink struct {
	mu    sync.RWMutex
	epoch int64  // identifies this run; when the frontend sees it change, it clears and refetches
	lines []line // ascending by seq; lines[head:] are live, head advances past maxBytes
	head  int
	bytes int
	seq   int64
	conn  net.Conn // the connection currently sending (a newer one replaces an older one)

	// Spool: also writes a copy to the spool for an Enterprise platform to pull; nil = memory only. Set before
	// Listen and never changed afterwards.
	Spool interface {
		Append(text string)
		Mark(kind string)
	}
}

func New() *Sink { return &Sink{epoch: time.Now().UnixNano()} }

// ValidateAddr allows the intake address to be only loopback or the IP of a virtual bridge interface on this
// machine. A bridge is detected by whether /sys/class/net/<iface>/bridge exists (docker0, br-*, podman0 are
// Linux bridges; physical interfaces are not). But some hosts attach a physical interface to a bridge as the
// external network (br0 + eth0, common on virtualization hosts), and that bridge's IP is reachable from other
// machines, so a bridge with a physical member (one that has a device) is also rejected; likewise the
// interface carrying the default route (in some virtualized environments the external interface is itself a
// bridge with no members).
// 0.0.0.0, hostnames, and physical interface IPs are always rejected.
func ValidateAddr(addr string) error {
	return validateAddr(addr, bridgeIPs)
}

func validateAddr(addr string, bridges func() (map[string]bool, error)) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("log sink address %q must be IP:port: %w", addr, err)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("log sink port %q is not in 1-65535", port)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("log sink address must be an IP, not a hostname (%q)", host)
	}
	if ip.IsLoopback() {
		return nil
	}
	if ip.IsUnspecified() {
		return errors.New("log sink cannot bind 0.0.0.0 / :: (the sink has no authentication and must not be reachable from other machines)")
	}
	ips, err := bridges()
	if err != nil {
		return fmt.Errorf("list network interfaces: %w", err)
	}
	if !ips[ip.String()] {
		return fmt.Errorf("log sink can only bind an IP on loopback or a local virtual bridge interface (such as docker0); %s is neither", ip)
	}
	return nil
}

// sysNet / procRoute: locations of the interface and routing tables (tests swap in fake files).
var (
	sysNet    = "/sys/class/net"
	procRoute = "/proc/net/route"
)

// internalBridge reports a Linux bridge that does not carry the default route and has no physical member
// (physical interfaces have a device under sysfs). Members of docker0 / br-* / podman0 are veths with no
// device; an external bridge like br0 + eth0 is excluded.
func internalBridge(name string) bool {
	if _, err := os.Stat(filepath.Join(sysNet, name, "bridge")); err != nil {
		return false
	}
	if defaultRouteIfaces()[name] {
		return false
	}
	members, _ := os.ReadDir(filepath.Join(sysNet, name, "brif"))
	for _, m := range members {
		if _, err := os.Stat(filepath.Join(sysNet, m.Name(), "device")); err == nil {
			return false
		}
	}
	return true
}

// defaultRouteIfaces returns the interfaces carrying the IPv4 default route (Destination 00000000), i.e. the
// external ones. If the table cannot be read, it returns none.
func defaultRouteIfaces() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(procRoute)
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(ln)
		if len(f) > 1 && f[1] == "00000000" {
			out[f[0]] = true
		}
	}
	return out
}

// bridgeIPs returns the IPs on this host's internal bridge interfaces (see internalBridge).
func bridgeIPs() (map[string]bool, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, ifc := range ifs {
		if !internalBridge(ifc.Name) {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				out[n.IP.String()] = true
			}
		}
	}
	return out, nil
}

// Listen opens the intake on addr until ctx is done. It accepts one connection at a time (Besu only opens
// one): a new connection closes the old one. Newer replacing older keeps another local program from
// connecting first and holding the slot to lock Besu out; Besu reconnects on its own after a disconnect, so
// it always wins the slot back. addr must pass ValidateAddr first.
func (s *Sink) Listen(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.mu.Lock()
		if s.conn != nil {
			s.conn.Close()
		}
		s.conn = c
		s.mu.Unlock()
		s.mark("sink_connected")
		go s.read(c)
	}
}

// read reads one connection until it drops, storing lines into the buffer. When replaced by a new
// connection, Close makes the read here fail and return.
func (s *Sink) read(c net.Conn) {
	defer func() {
		c.Close()
		s.mu.Lock()
		gone := s.conn == c // being replaced by a new connection does not count as a disconnect
		if gone {
			s.conn = nil
		}
		s.mu.Unlock()
		if gone {
			s.mark("sink_disconnected")
		}
	}()
	r := bufio.NewReaderSize(c, maxLine)
	for {
		b, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// line over the limit: keep the head, discard the rest up to the newline.
			s.add(string(b))
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
			if err != nil {
				return
			}
			continue
		}
		if len(b) > 0 {
			s.add(string(trimEOL(b)))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("logsink: %v", err)
			}
			return
		}
	}
}

func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

func (s *Sink) mark(kind string) {
	if s.Spool != nil {
		s.Spool.Mark(kind)
	}
}

func (s *Sink) add(text string) {
	if s.Spool != nil {
		s.Spool.Append(text)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	s.lines = append(s.lines, line{seq: s.seq, text: text})
	s.bytes += len(text) + lineOverhead
	for s.bytes > maxBytes && s.head < len(s.lines)-1 {
		s.bytes -= len(s.lines[s.head].text) + lineOverhead
		s.lines[s.head] = line{} // release the string for GC
		s.head++
	}
	// compact only once more than half is dropped: amortized O(1) per line, instead of copying the whole slice
	// on every append.
	if s.head > len(s.lines)/2 {
		s.lines = append(s.lines[:0:0], s.lines[s.head:]...)
		s.head = 0
	}
}

// Read returns up to limit lines after after; after < 0 returns the newest limit lines.
// Agent restarts are detected via epoch (when the frontend sees epoch change it refetches without after);
// this does not guess.
func (s *Sink) Read(after int64, limit int) api.NodeLogs {
	switch {
	case limit < 1:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	live := s.lines[s.head:]
	var pick []line
	var skipped int64
	if after < 0 {
		pick = live[max(len(live)-limit, 0):]
	} else {
		// cursor falls in the evicted range: return from the oldest remaining line and report how many lines are
		// missing, so the view does not pretend to be continuous.
		if len(live) > 0 && live[0].seq > after+1 {
			skipped = live[0].seq - after - 1
		}
		// seq increases contiguously, so compute the position directly instead of scanning.
		i := 0
		if len(live) > 0 {
			i = int(min(max(after-live[0].seq+1, 0), int64(len(live))))
		}
		pick = live[i:min(i+limit, len(live))]
	}
	out := api.NodeLogs{Epoch: s.epoch, Connected: s.conn != nil, Lines: make([]api.NodeLogLine, 0, len(pick)), Next: max(after, 0), Skipped: skipped}
	for _, l := range pick {
		out.Lines = append(out.Lines, api.NodeLogLine{Seq: l.seq, Text: l.text})
	}
	if len(pick) > 0 {
		out.Next = pick[len(pick)-1].seq
	}
	return out
}

// ServeHTTP handles GET /logs?after=<seq>&limit=<n>. The platform calls it through the tunnel (without X-Target).
func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	after := int64(-1)
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			http.Error(w, "after must be a non-negative integer", http.StatusBadRequest)
			return
		}
		after = n
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.Read(after, limit))
}
