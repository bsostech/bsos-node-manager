package hostmetrics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"besu-manager/agent/internal/api"
)

// DISK_PATH is used when set; only empty falls back to /.
func TestNewDiskPath(t *testing.T) {
	if got := New("/host").diskPath; got != "/host" {
		t.Errorf("New(\"/host\").diskPath = %q", got)
	}
	if got := New("").diskPath; got != "/" {
		t.Errorf("New(\"\").diskPath = %q", got)
	}
}

func TestServeHTTP(t *testing.T) {
	s := New("")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/host-metrics", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("取樣前應回 503,得 %d", rec.Code)
	}

	cpu, used, total := float32(12.5), int64(1), int64(4)
	s.last = &api.HardwareMetrics{CpuPercent: &cpu, MemUsedBytes: &used, MemTotalBytes: &total}

	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/host-metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{"bsos_node_cpu_percent 12.5", "bsos_node_mem_percent 25\n", "# TYPE bsos_node_cpu_percent gauge"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Prometheus 格式缺 %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "disk") {
		t.Fatalf("沒取到的值不該輸出:\n%s", body)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/host-metrics", nil)
	req.Header.Set("Accept", "application/json")
	s.ServeHTTP(rec, req)
	var hw api.HardwareMetrics
	if err := json.Unmarshal(rec.Body.Bytes(), &hw); err != nil || hw.CpuPercent == nil || *hw.CpuPercent != 12.5 {
		t.Fatalf("JSON 回應不對: %s (%v)", rec.Body.String(), err)
	}
}
