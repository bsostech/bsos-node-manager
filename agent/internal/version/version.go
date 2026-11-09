// Package version provides the agent's build version string.
// Defaults to "dev"; release builds override it via ldflags in build-agent.sh:
//
//	-X besu-manager/agent/internal/version.Version=<ver>
//
// The platform's version endpoint and upgrade-agent.sh use it to decide "already up to date", avoiding a
// needless binary swap and restart.
package version

// Version is the build version (overridden via ldflags; not overridden means a dev build).
var Version = "dev"
