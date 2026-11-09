package server

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// install.sh is run on the node with hand-typed arguments: argument errors must be explained before
// asking for sudo (exit 2), otherwise someone with a typo would need root just to see the real
// error.
func TestInstallScriptArgValidation(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	ok := []string{"--platform", "p:8443", "--token", "BSOS-1-x"}
	type tc struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}
	cases := []tc{
		{"unknown flag", append(ok, "--node-id", "a"), 2, "unknown option: --node-id"},
		{"zh messages", append(ok, "--zh", "--node-id", "a"), 2, "未知參數: --node-id"},
		{"missing platform", []string{"--token", "t"}, 2, "--platform"},
		{"missing token", []string{"--platform", "p:8443"}, 2, "--token"},
		{"flag without value", []string{"--platform"}, 2, "needs a value"},
		{"bad runtime", append(ok, "--runtime", "native"), 2, "--runtime"},
		{"bad allow-ports", append(ok, "--allow-ports", "8545;9545"), 2, "--allow-ports"},
		{"allow-ports out of range", append(ok, "--allow-ports", "8545,70000"), 2, "70000"},
		{"relative state dir", append(ok, "--state-dir", "state"), 2, "absolute path"},
		{"disk-path in docker", append(ok, "--runtime", "docker", "--disk-path", "/"), 2, "--disk-path"},
		{"bad log-sink", append(ok, "--log-sink", "localhost:5170"), 2, "--log-sink"},
	}
	if os.Geteuid() != 0 {
		// The root check comes after argument checks: reaching "needs root" means all
		// arguments passed (this layer is invisible when tests run as root, so skip).
		cases = append(cases,
			tc{"systemd default reaches root check", append(ok, "--disk-path", "/data", "--state-dir", "/opt/agent"), 1, "needs root"},
			tc{"docker reaches root check", append(ok, "--runtime", "docker", "--allow-ports", "8545,9545", "--re-enroll", "--log-sink", "172.17.0.1:5170"), 1, "needs root"},
		)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command("bash", append([]string{"-s", "--"}, c.args...)...)
			cmd.Stdin = bytes.NewReader(installScript)
			cmd.Env = append(os.Environ(), "NO_COLOR=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			err := cmd.Run()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != c.wantCode {
				t.Fatalf("exit = %v, want %d; stderr=%s", err, c.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), c.wantErr) {
				t.Fatalf("stderr %q does not mention %q", stderr.String(), c.wantErr)
			}
		})
	}
}

// installFlow runs install.sh end to end inside a temporary INSTALL_ROOT, with shims for the
// commands that need root or a real platform: id, curl, systemctl, useradd, chown, timeout and a
// fake besu-agent whose join succeeds or fails per FAKE_JOIN.
type installFlow struct {
	t                         *testing.T
	root, state, sysLog, shim string
}

func newInstallFlow(t *testing.T) *installFlow {
	t.Helper()
	for _, c := range []string{"bash", "sha256sum"} {
		if _, err := exec.LookPath(c); err != nil {
			t.Skip("needs " + c)
		}
	}
	f := &installFlow{t: t, root: t.TempDir(), shim: t.TempDir()}
	f.state = f.root + "/var/lib/besu-agent"
	f.sysLog = f.root + "/systemctl.log"
	if err := os.MkdirAll(f.root+"/usr/local/bin", 0o755); err != nil {
		t.Fatal(err)
	}
	agent := `#!/usr/bin/env bash
case "$1" in
  version) echo 9.9.9;;
  join)
    [ "$FAKE_JOIN" = hang ] && sleep 30
    [ -z "$FAKE_JOIN_MSG" ] || { echo "$FAKE_JOIN_MSG"; exit 1; }
    [ "$FAKE_JOIN" = ok ] || { echo 'join: join rejected (401): {"error":"token not usable"}'; exit 1; }
    while [ $# -gt 0 ]; do [ "$1" = --state-dir ] && dir="$2"; shift; done
    mkdir -p "$dir" && for f in agent.key agent.crt ca.pem; do echo new > "$dir/$f"; done
    echo "join complete: certificate valid until 2026-11-09T08:00:00Z, material stored in $dir";;
esac
`
	shims := map[string]string{
		"besu-agent-fixture": agent,
		"id":                 "#!/usr/bin/env bash\n[ \"$1\" = -u ] && echo 0 || true\n",
		"curl":               "#!/usr/bin/env bash\nfor a; do url=$a; done\nprev=; out=; for a; do [ \"$prev\" = -o ] && out=$a; prev=$a; done\nfix=" + "SHIMDIR/besu-agent-fixture\ncase \"$*\" in *.sha256*) sha256sum \"$fix\";; *) cp \"$fix\" \"$out\";; esac\n",
		"systemctl":          "#!/usr/bin/env bash\necho \"$*\" >> \"$SYSLOG\"\nexit 0\n",
		"useradd":            "#!/usr/bin/env bash\nexit 0\n",
		"chown":              "#!/usr/bin/env bash\nexit 0\n",
		"timeout":            "#!/usr/bin/env bash\nshift; exec \"$@\"\n",
	}
	for name, body := range shims {
		body = strings.ReplaceAll(body, "SHIMDIR", f.shim)
		if err := os.WriteFile(f.shim+"/"+name, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *installFlow) run(join string, args ...string) (int, string) {
	f.t.Helper()
	base := []string{"-s", "--", "--platform", "p:8443", "--token", "BSOS-1-x", "--platform-http", "http://p", "--state-dir", f.state}
	cmd := exec.Command("bash", append(base, args...)...)
	cmd.Stdin = bytes.NewReader(installScript)
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "INSTALL_ROOT="+f.root, "SYSLOG="+f.sysLog, "FAKE_JOIN="+join,
		"PATH="+f.shim+":"+os.Getenv("PATH"))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), out.String()
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return 0, out.String()
}

func (f *installFlow) read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

// install.sh explains a failed join in the user's language by matching the agent's message; these are the
// messages it recognizes (agent/internal/join and the platform's join API). A reworded message shows up here.
func TestInstallScriptExplainsJoinFailures(t *testing.T) {
	cases := map[string]string{
		"2026/11/06 08:00:00 join: malformed token":                                                                                      "the join code is not complete",
		`join: join rejected (403): {"error":"source ip not allowed for this token"}`:                                                    "allowed source IPs",
		`join: Post "https://p:8443/pki/v1/join": platform CA digest mismatch (possible man-in-the-middle)`:                              "made by a different platform",
		`join: join rejected (401): {"error":"invalid token"}`:                                                                           "the join code is wrong",
		`join: join rejected (409): {"error":"node is still connected; re-enrollment is only for nodes that cannot reach the platform"}`: "still sees this node connected",
		`join: join rejected (409): {"error":"node id already exists; delete the node first to re-onboard"}`:                             "already registered",
		`join: join rejected (404): {"error":"node not found; use join to onboard a new node"}`:                                          "not registered on the platform",
		`join: Post "https://p:8443/pki/v1/join": dial tcp: lookup p: no such host`:                                                      "cannot be resolved",
		`join: Post "https://p:8443/pki/v1/join": dial tcp 10.0.0.1:8443: connect: network is unreachable`:                               "cannot reach the platform",
		`join: Post "https://p:8443/pki/v1/join": tls: first record does not look like a TLS handshake`:                                  "did not answer as this platform's agent port",
		`join: Post "https://p:80/pki/v1/join": http: server gave HTTP response to HTTPS client`:                                         "did not answer as this platform's agent port",
	}
	for msg, want := range cases {
		t.Setenv("FAKE_JOIN_MSG", msg)
		f := newInstallFlow(t)
		code, out := f.run("fail")
		if code == 0 || !strings.Contains(out, want) || !strings.Contains(out, "Nothing was changed") {
			t.Errorf("%s: want %q, got exit %d:\n%s", msg, want, code, out)
		}
	}
}

// A failed join must leave the host exactly as it was: no new binary, no certificate, no leftovers,
// and on a re-enroll the agent that was stopped is started again.
func TestInstallScriptFailedJoinLeavesNothing(t *testing.T) {
	t.Run("fresh install", func(t *testing.T) {
		f := newInstallFlow(t)
		code, out := f.run("fail")
		if code == 0 {
			t.Fatalf("install succeeded with a failing join:\n%s", out)
		}
		for _, p := range []string{"/usr/local/bin/besu-agent", "/usr/local/bin/besu-agent.new", "/var/lib/besu-agent", "/var/lib/besu-agent.new", "/etc/systemd/system/besu-agent.service"} {
			if _, err := os.Stat(f.root + p); err == nil {
				t.Errorf("%s exists after a failed join", p)
			}
		}
		if !strings.Contains(out, "Nothing was changed") {
			t.Errorf("output does not say nothing changed:\n%s", out)
		}
		if !strings.Contains(out, "the join code is wrong") || !strings.Contains(out, "details: join: join rejected (401)") {
			t.Errorf("output does not explain the 401 and keep the agent's detail:\n%s", out)
		}
	})
	t.Run("re-enroll keeps the old agent", func(t *testing.T) {
		f := newInstallFlow(t)
		if err := os.WriteFile(f.root+"/usr/local/bin/besu-agent", []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(f.state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.state+"/agent.crt", []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out := f.run("fail", "--re-enroll")
		if code == 0 {
			t.Fatalf("install succeeded with a failing join:\n%s", out)
		}
		if got := f.read(f.root + "/usr/local/bin/besu-agent"); got != "old" {
			t.Errorf("binary changed to %q", got)
		}
		if got := f.read(f.state + "/agent.crt"); got != "old" {
			t.Errorf("certificate changed to %q", got)
		}
		if log := f.read(f.sysLog); !strings.Contains(log, "stop besu-agent") || !strings.Contains(log, "start besu-agent") {
			t.Errorf("systemctl calls = %q, want a stop and a start", log)
		}
	})
}

func TestInstallScriptJoinInstallsAgent(t *testing.T) {
	f := newInstallFlow(t)
	code, out := f.run("ok")
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "valid until 2026-11-09T08:00:00Z") || strings.Contains(out, "material stored") {
		t.Errorf("success output should give the expiry but not the staging directory:\n%s", out)
	}
	for _, name := range []string{"agent.key", "agent.crt", "ca.pem"} {
		if got := f.read(f.state + "/" + name); got != "new\n" {
			t.Errorf("%s = %q", name, got)
		}
	}
	if got := f.read(f.root + "/usr/local/bin/besu-agent"); !strings.Contains(got, "FAKE_JOIN") {
		t.Errorf("binary not installed: %q", got)
	}
	for _, p := range []string{"/usr/local/bin/besu-agent.new", "/var/lib/besu-agent.new"} {
		if _, err := os.Stat(f.root + p); err == nil {
			t.Errorf("%s left behind", p)
		}
	}
	if _, err := os.Stat(f.root + "/etc/systemd/system/besu-agent.service"); err != nil {
		t.Errorf("unit not written: %v", err)
	}
}

// --help prints the usage without needing root or any other option.
func TestInstallScriptHelp(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	cmd := exec.Command("bash", "-s", "--", "--help")
	cmd.Stdin = bytes.NewReader(installScript)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--platform <host[:8443]>") {
		t.Fatalf("err=%v out=%s", err, out)
	}
}

// --platform without a port means the agent port 8443, not https' 443.
func TestInstallScriptPlatformDefaultPort(t *testing.T) {
	f := newInstallFlow(t)
	code, out := f.run("ok", "--platform", "p")
	if code != 0 || !strings.Contains(out, "(p:8443)") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// Re-join rewrites the agent configuration, and the platform's re-join command carries none of the
// install-time options: those left out keep the current values, those given replace them.
func TestInstallScriptReEnrollKeepsOptions(t *testing.T) {
	f := newInstallFlow(t)
	if code, out := f.run("ok", "--allow-ports", "8545,30303", "--log-sink", "172.17.0.1:5170", "--disk-path", "/data"); code != 0 {
		t.Fatalf("install: exit %d:\n%s", code, out)
	}
	if code, out := f.run("ok", "--re-enroll"); code != 0 {
		t.Fatalf("re-enroll: exit %d:\n%s", code, out)
	}
	env := f.read(f.root + "/etc/besu-agent/agent.env")
	for _, want := range []string{"ALLOW_PORTS=8545,30303", "LOG_SINK=172.17.0.1:5170", "DISK_PATH=/data"} {
		if !strings.Contains(env, want) {
			t.Errorf("agent.env lost %s after re-enroll:\n%s", want, env)
		}
	}
	if code, out := f.run("ok", "--re-enroll", "--allow-ports", "8545"); code != 0 {
		t.Fatalf("re-enroll with an option: exit %d:\n%s", code, out)
	}
	if env := f.read(f.root + "/etc/besu-agent/agent.env"); !strings.Contains(env, "ALLOW_PORTS=8545\n") || !strings.Contains(env, "LOG_SINK=172.17.0.1:5170") {
		t.Errorf("a given option should replace only itself:\n%s", env)
	}
}

// --dry-run checks and lists the changes but writes nothing, starts nothing and never runs the join.
func TestInstallScriptDryRunChangesNothing(t *testing.T) {
	f := newInstallFlow(t)
	code, out := f.run("ok", "--dry-run")
	if code != 0 || !strings.Contains(out, "Dry run finished") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, p := range []string{f.root + "/usr/local/bin/besu-agent", f.state, f.root + "/etc/besu-agent", f.sysLog} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created by a dry run", p)
		}
	}
	if !strings.Contains(out, "join p:8443") || !strings.Contains(out, "besu-agent.service") {
		t.Errorf("dry run should list the join and the service:\n%s", out)
	}
}
