package auth

import (
	"context"
	"sync"
	"time"
)

// deniedWindow: when the same account is denied for the same missing permission, only one entry is recorded per window.
// Frontend polling that hits a 403 repeats every few seconds, and each audit entry locks the whole audit_log, so they cannot be written one by one.
const deniedWindow = time.Minute

// deniedMax is the hard cap on the throttle table. Normal usage is "active accounts x permissions", far below this.
const deniedMax = 4096

// deniedLog throttles permission-denied audits in memory, before anything is written to the database.
// ponytail: the count merged within a window is only recorded on the next denial (suppressed); if no further denial comes, those are undercounted.
// Exact counts would need a background flush, which is not worth it for now.
type deniedLog struct {
	mu   sync.Mutex
	seen map[string]*deniedEntry
}

type deniedEntry struct {
	last    time.Time
	skipped int
}

// admit reports whether this entry should be written; when it should, it also returns how many were merged since the last write.
func (d *deniedLog) admit(key string, now time.Time) (write bool, suppressed int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string]*deniedEntry{}
	}
	if e := d.seen[key]; e != nil && now.Sub(e.last) < deniedWindow {
		e.skipped++
		return false, 0
	}
	if len(d.seen) >= deniedMax { // account x permission combinations are bounded; this just sweeps expired entries along the way
		for k, e := range d.seen {
			if now.Sub(e.last) >= deniedWindow {
				delete(d.seen, k)
			}
		}
		if len(d.seen) >= deniedMax { // still full: clear everything. Worst case the throttling restarts and a few extra entries get written; memory stays bounded
			clear(d.seen)
		}
	}
	if e := d.seen[key]; e != nil {
		suppressed = e.skipped
	}
	d.seen[key] = &deniedEntry{last: now}
	return true, suppressed
}

// auditDenied records access_denied for a missing permission. Best effort: it records a denied request that had no effect, so if the write fails the request still gets its 403.
func (s *Service) auditDenied(ctx context.Context, p *Principal, perm Permission, method, path string) {
	user := ""
	if p.Username != nil {
		user = *p.Username
	}
	// The key excludes the path: paths carry variables like node ids, so changing one would bypass the throttle and lock the audit table on every request. The recorded path is the first one denied in this window.
	write, suppressed := s.denied.admit(user+"\x00"+string(perm), time.Now())
	if !write {
		return
	}
	if len(path) > 512 {
		path = path[:512]
	}
	d := map[string]any{"actor": user, "role": p.Role, "permission": string(perm), "http_method": method, "path": path}
	if suppressed > 0 {
		d["suppressed"] = suppressed
	}
	_ = s.store.Audit(ctx, "access_denied", d)
}
