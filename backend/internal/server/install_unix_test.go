//go:build unix

package server

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Ctrl-C or a dropped SSH session during the join must still start the stopped agent again: bash runs
// the EXIT trap with status 0 after a signal unless the script turns the signal into a failing exit.
func TestInstallScriptInterruptedReEnrollRestartsAgent(t *testing.T) {
	f := newInstallFlow(t)
	if err := os.WriteFile(f.root+"/usr/local/bin/besu-agent", []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := []string{"-s", "--", "--platform", "p:8443", "--token", "BSOS-1-x", "--platform-http", "http://p", "--state-dir", f.state, "--re-enroll"}
	cmd := exec.Command("bash", base...)
	cmd.Stdin = bytes.NewReader(installScript)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "INSTALL_ROOT="+f.root, "SYSLOG="+f.sysLog, "FAKE_JOIN=hang",
		"PATH="+f.shim+":"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // signal the whole group, as a terminal does
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(f.read(f.sysLog), "stop besu-agent") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("agent was never stopped")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let the join start
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
	_ = cmd.Wait()
	if log := f.read(f.sysLog); !strings.Contains(log, "start besu-agent") {
		t.Errorf("systemctl calls = %q, want a start after the interrupt", log)
	}
	if got := f.read(f.root + "/usr/local/bin/besu-agent"); got != "old" {
		t.Errorf("binary changed to %q", got)
	}
}
