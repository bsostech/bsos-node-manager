// Package version holds the platform server's build version.
// Both values are set via ldflags in backend/Dockerfile from what up.sh resolves (deploy/version.sh):
//
//	-X besu-manager/backend/internal/version.Version=<ver> -X besu-manager/backend/internal/version.ReleaseDate=<YYYY-MM-DD>
//
// The same string is built into the agent, so the platform and the agent of one checkout report the same version.
package version

// Version is the build version: a release tag (v1.2.3), git describe output for a commit after a tag, or "dev".
var Version = "dev"

// ReleaseDate is the date (YYYY-MM-DD) of the commit a release tag points to; empty for anything that is not an exact release.
var ReleaseDate = ""
