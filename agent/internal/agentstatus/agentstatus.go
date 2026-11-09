// Package agentstatus serves GET /agent-status: the agent's version and whether anything is listening on
// this Besu's P2P port right now.
// The platform parses the port from the enode and passes it in; the agent does not know where Besu is. This
// returns only the raw result of this one probe; "has ever connected" is tracked by the platform.
package agentstatus

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"besu-manager/agent/internal/api"
	"besu-manager/agent/internal/version"
)

const dialTimeout = 500 * time.Millisecond

// Handler returns p2pOpen as null when p2pPort is absent, and 400 for an invalid port.
// When allowPorts is non-empty (ops set ALLOW_PORTS), only listed ports are probed and the rest return null,
// so the platform cannot bypass the range ops restricted and probe whether other ports are open.
func Handler(allowPorts map[string]bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt := detectRuntime()
		st := api.AgentStatus{Version: version.Version, Runtime: &rt}
		if v := r.URL.Query().Get("p2pPort"); v != "" {
			port, err := strconv.Atoi(v)
			if err != nil || port < 1 || port > 65535 {
				http.Error(w, "invalid p2pPort", http.StatusBadRequest)
				return
			}
			if len(allowPorts) == 0 || allowPorts[v] {
				open := probe(r.Context(), hosts(), v)
				st.P2pOpen = &open
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
}

// detectRuntime reports docker when the agent runs in a container (Docker creates /.dockerenv, Podman
// /run/.containerenv), so the platform can preselect the same runtime when it generates a re-join command.
func detectRuntime() api.AgentRuntime {
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return api.AgentRuntimeDocker
		}
	}
	return api.AgentRuntimeSystemd
}

// hosts returns 127.0.0.1 plus the IP of every host interface. Besu may bind only one interface; the agent
// runs on the host network in both modes, so it can reach whichever one is bound.
// Link-local is skipped: fe80:: cannot be dialed without a zone.
func hosts() []string {
	out := []string{"127.0.0.1"}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLinkLocalUnicast() || n.IP.Equal(net.IPv4(127, 0, 0, 1)) {
			continue
		}
		out = append(out, n.IP.String())
	}
	return out
}

// probe dials TCP to port on every address in parallel, disconnecting on connect without exchanging data;
// true if any one connects.
func probe(ctx context.Context, hosts []string, port string) bool {
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	ok := make(chan bool, len(hosts))
	var d net.Dialer
	for _, h := range hosts {
		go func() {
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(h, port))
			if err == nil {
				c.Close()
			}
			ok <- err == nil
		}()
	}
	for range hosts {
		if <-ok {
			return true
		}
	}
	return false
}
