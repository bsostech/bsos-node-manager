package server

import "testing"

// Ports 1 and 65535 are the inclusive ends of the TCP range; 0 and 65536 fall outside it.
func TestValidPort(t *testing.T) {
	for p, want := range map[int]bool{0: false, 1: true, 65535: true, 65536: false} {
		if got := validPort(p); got != want {
			t.Errorf("validPort(%d) = %v, want %v", p, got, want)
		}
	}
}
