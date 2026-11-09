package server

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Config file: sends to 127.0.0.1:5170 by default, IP can be changed; output is valid XML; a sink
// that is not IP:port gets 400 (prevents injecting strings into the XML).
func TestBesuLog4jHandler(t *testing.T) {
	for _, tc := range []struct {
		query    string
		code     int
		wantHost string
	}{
		{"", 200, `host="127.0.0.1" port="5170"`},
		{"?sink=172.17.0.1:5170", 200, `host="172.17.0.1" port="5170"`},
		{"?sink=evil%22%3E:5170", 400, ""},
		{"?sink=host.docker.internal:5170", 400, ""},
		{"?sink=127.0.0.1:70000", 400, ""},
	} {
		rec := httptest.NewRecorder()
		BesuLog4jHandler()(rec, httptest.NewRequest(http.MethodGet, "/api/v1/platform/besu-log4j2.xml"+tc.query, nil))
		if rec.Code != tc.code {
			t.Fatalf("%q: code=%d want %d", tc.query, rec.Code, tc.code)
		}
		if tc.code != 200 {
			continue
		}
		body := rec.Body.String()
		if !strings.Contains(body, tc.wantHost) || !strings.Contains(body, `blocking="false"`) {
			t.Errorf("%q: 設定檔內容不對", tc.query)
		}
		if err := xml.Unmarshal([]byte(body), new(struct{})); err != nil {
			t.Errorf("%q: 不是合法 XML: %v", tc.query, err)
		}
	}
}

// parseSink: ports 1-65535 are accepted; both ends must be accepted and values just outside
// rejected.
func TestParseSinkPortRange(t *testing.T) {
	for in, ok := range map[string]bool{"127.0.0.1:0": false, "127.0.0.1:1": true, "127.0.0.1:65535": true, "127.0.0.1:65536": false} {
		if h, p, got := parseSink(in); got != ok || ok && (h != "127.0.0.1" || strconv.Itoa(p) != in[len("127.0.0.1:"):]) {
			t.Errorf("%s: host=%q port=%d ok=%v,want ok=%v", in, h, p, got, ok)
		}
	}
}
