// Package hostmetrics is the only thing the agent computes itself: the node host's CPU / memory / disk /
// network (gopsutil).
// A background loop samples once per round and caches the latest values; GET /host-metrics returns JSON
// (the HardwareMetrics the platform collector wants) or Prometheus text format (bsos_node_* gauges, which
// Prometheus scrapes via the platform proxy) depending on Accept. Chain metrics are not here: the platform
// builds live values over RPC, and Prometheus derives time series from Besu's native metrics.
package hostmetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"

	"besu-manager/agent/internal/api"
)

type netSample struct {
	rx, tx uint64
	at     time.Time
}

type Sampler struct {
	diskPath string // path used to measure disk usage; a docker sidecar points it at the mounted host rootfs (DISK_PATH)

	mu   sync.RWMutex
	last *api.HardwareMetrics
	prev *netSample // touched only by the Run goroutine
}

func New(diskPath string) *Sampler {
	if diskPath == "" {
		diskPath = "/"
	}
	return &Sampler{diskPath: diskPath}
}

// Run samples at a fixed interval until ctx is done.
func (s *Sampler) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		s.sample()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Sampler) sample() {
	hw := &api.HardwareMetrics{}
	active := true
	hw.Active = &active
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) > 0 {
		v := float32(pct[0])
		hw.CpuPercent = &v
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		used, total := int64(vm.Used), int64(vm.Total)
		hw.MemUsedBytes, hw.MemTotalBytes = &used, &total
	}
	if du, err := disk.Usage(s.diskPath); err == nil {
		v := float32(du.UsedPercent)
		hw.DiskUsedPercent = &v
	}
	if io, err := gnet.IOCounters(false); err == nil && len(io) > 0 {
		now := time.Now()
		if s.prev != nil {
			if dt := now.Sub(s.prev.at).Seconds(); dt > 0 {
				rx := int64(float64(io[0].BytesRecv-s.prev.rx) / dt)
				tx := int64(float64(io[0].BytesSent-s.prev.tx) / dt)
				hw.NetRxBytesPerSec, hw.NetTxBytesPerSec = &rx, &tx
			}
		}
		s.prev = &netSample{rx: io[0].BytesRecv, tx: io[0].BytesSent, at: now}
	} else if err != nil {
		log.Printf("hostmetrics: net: %v", err)
	}
	s.mu.Lock()
	s.last = hw
	s.mu.Unlock()
}

// ServeHTTP handles GET /host-metrics. If Accept contains application/json it returns HardwareMetrics,
// otherwise Prometheus text format.
func (s *Sampler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	hw := s.last
	s.mu.RUnlock()
	if hw == nil {
		http.Error(w, "first sample not taken yet", http.StatusServiceUnavailable)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(hw)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(promText(hw)))
}

// promText emits five host gauges whose names match the mapping in backend/internal/prom; values not
// collected are omitted (never emit a fake 0).
func promText(hw *api.HardwareMetrics) string {
	var b strings.Builder
	gauge := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
	}
	if hw.CpuPercent != nil {
		gauge("bsos_node_cpu_percent", "CPU usage (%)", float64(*hw.CpuPercent))
	}
	if hw.MemUsedBytes != nil && hw.MemTotalBytes != nil && *hw.MemTotalBytes > 0 {
		gauge("bsos_node_mem_percent", "memory usage (%)", float64(*hw.MemUsedBytes)/float64(*hw.MemTotalBytes)*100)
	}
	if hw.DiskUsedPercent != nil {
		gauge("bsos_node_disk_percent", "disk usage (%)", float64(*hw.DiskUsedPercent))
	}
	if hw.NetRxBytesPerSec != nil {
		gauge("bsos_node_net_rx_bytes_per_sec", "network receive (bytes/s)", float64(*hw.NetRxBytesPerSec))
	}
	if hw.NetTxBytesPerSec != nil {
		gauge("bsos_node_net_tx_bytes_per_sec", "network transmit (bytes/s)", float64(*hw.NetTxBytesPerSec))
	}
	return b.String()
}
