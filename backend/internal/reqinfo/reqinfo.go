// Package reqinfo: the request's source IP and user agent, put into the context for auditing (store.Audit attaches them uniformly).
//
// The source IP defaults to the connection address. Anyone can fill X-Forwarded-For, so it is trusted only when the connection comes from a configured trusted proxy range,
// and then the first address from the right that is not in a trusted range is taken (entries further left are filled by the client and can be forged).
package reqinfo

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

type Info struct {
	IP string
	UA string
}

type ctxKey struct{}

func With(ctx context.Context, i Info) context.Context { return context.WithValue(ctx, ctxKey{}, i) }

// From returns ok=false for a context that did not pass through Middleware (background jobs, startup).
func From(ctx context.Context) (Info, bool) {
	i, ok := ctx.Value(ctxKey{}).(Info)
	return i, ok
}

// ParseCIDRs: "10.0.0.0/8, 192.168.1.10" → list of ranges; a single IP is treated as /32 (/128). Empty string = trust no proxy.
func ParseCIDRs(v string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			ip := net.ParseIP(s)
			if ip == nil {
				return nil, fmt.Errorf("not an IP or CIDR: %q", s)
			}
			bits := 128
			if ip.To4() != nil {
				bits = 32
			}
			s = fmt.Sprintf("%s/%d", ip, bits)
		}
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("not an IP or CIDR: %q", s)
		}
		out = append(out, n)
	}
	return out, nil
}

func Middleware(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua := r.UserAgent()
			if len(ua) > 512 { // user-supplied field; don't let one request bloat the audit log
				ua = ua[:512]
			}
			next.ServeHTTP(w, r.WithContext(With(r.Context(), Info{IP: clientIP(r, trusted), UA: ua})))
		})
	}
}

func clientIP(r *http.Request, trusted []*net.IPNet) string {
	host := hopIP(r.RemoteAddr) // net/http always fills IP:port; if unrecognized (only hand-built requests do that), record nothing rather than treating the raw string as an IP
	if host == "" {
		return ""
	}
	if !in(host, trusted) {
		return host
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h := hopIP(strings.TrimSpace(hops[i]))
		if h == "" {
			break // stop trusting further left on a malformed entry
		}
		if !in(h, trusted) {
			return h
		}
		host = h
	}
	return host
}

// hopIP turns one XFF entry into an IP. Some proxies add a port (198.51.100.7:4711, [2001:db8::1]:443) or only brackets; returns an empty string if unrecognized.
func hopIP(h string) string {
	if net.ParseIP(h) != nil {
		return h
	}
	if host, _, err := net.SplitHostPort(h); err == nil && net.ParseIP(host) != nil {
		return host
	}
	if t := strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"); net.ParseIP(t) != nil {
		return t
	}
	return ""
}

func in(ip string, nets []*net.IPNet) bool {
	p := net.ParseIP(ip)
	if p == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(p) {
			return true
		}
	}
	return false
}
