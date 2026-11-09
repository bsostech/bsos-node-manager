package reqinfo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted, err := ParseCIDRs("10.0.0.0/8, 192.168.1.5, 2001:db8::5")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, remote, xff, want string
		trusted                 bool
	}{
		{"沒設信任網段就不看 XFF", "203.0.113.9:5000", "1.2.3.4", "203.0.113.9", false},
		{"連線不是信任的轉送機,XFF 不採信", "203.0.113.9:5000", "1.2.3.4", "203.0.113.9", true},
		{"信任的轉送機:取 XFF 最右邊的", "10.1.1.1:5000", "198.51.100.7", "198.51.100.7", true},
		{"多層信任轉送機:跳過信任的,左邊偽造的不採信", "192.168.1.5:5000", "6.6.6.6, 198.51.100.7, 10.2.2.2", "198.51.100.7", true},
		{"信任的轉送機但沒帶 XFF", "10.1.1.1:5000", "", "10.1.1.1", true},
		{"XFF 格式不對就停", "10.1.1.1:5000", "garbage", "10.1.1.1", true},
		{"XFF 帶埠", "10.1.1.1:5000", "198.51.100.7:4711", "198.51.100.7", true},
		{"XFF 是帶方括號與埠的 IPv6", "10.1.1.1:5000", "[2001:db8::1]:443", "2001:db8::1", true},
		{"XFF 是只帶方括號的 IPv6", "10.1.1.1:5000", "[2001:db8::1]", "2001:db8::1", true},
		{"IPv6 的信任轉送機", "[2001:db8::5]:443", "198.51.100.7", "198.51.100.7", true},
		{"RemoteAddr 格式不對就不記", "not-an-addr", "198.51.100.7", "", true},
		{"XFF 是空的", "10.1.1.1:5000", " , ", "10.1.1.1", true},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		nets := trusted
		if !c.trusted {
			nets = nil
		}
		if got := clientIP(r, nets); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestParseCIDRsRejectsGarbage(t *testing.T) {
	if _, err := ParseCIDRs("10.0.0.0/8, not-an-ip"); err == nil {
		t.Fatal("應拒絕非 IP")
	}
}

// Through Middleware: the handler gets the IP and UA, and an overlong UA is truncated.
func TestMiddleware(t *testing.T) {
	var got Info
	h := Middleware(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got, _ = From(r.Context()) }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5000"
	r.Header.Set("User-Agent", string(make([]byte, 600)))
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got.IP != "203.0.113.9" || len(got.UA) != 512 {
		t.Fatalf("got ip=%s ua=%d", got.IP, len(got.UA))
	}
	if _, ok := From(context.Background()); ok {
		t.Fatal("沒經過 Middleware 不該有值")
	}
}
