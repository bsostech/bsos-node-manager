#!/usr/bin/env bash
# Dependency checks: third-party license list in sync, licenses allowed, no known vulnerabilities in Go and frontend production deps.
# Run it after changing go.mod / package.json, or periodically (see tools/deps/README.md).
#   tools/deps/check.sh
set -euo pipefail
cd "$(dirname "$0")/../.."

GOVULNCHECK=golang.org/x/vuln/cmd/govulncheck@v1.9.0 # pinned: the scanner itself is part of the supply chain
fail=0

echo "== Third-party license list"
python3 tools/deps/notices.py --check || fail=1

# govulncheck only reports vulnerabilities "the code can actually reach"; Core and Enterprise build different programs, so scan both
echo "== Go known vulnerabilities: backend (Core)"
(cd backend && go run "$GOVULNCHECK" ./...) || fail=1
if [ -d backend/enterprise ]; then # the open edition export has no Enterprise source
  echo "== Go known vulnerabilities: backend (Enterprise)"
  (cd backend && go run "$GOVULNCHECK" -tags enterprise ./...) || fail=1
fi
echo "== Go known vulnerabilities: agent"
(cd agent && go run "$GOVULNCHECK" ./...) || fail=1

# Only production deps that go into the bundle, high and above; exceptions for dev tools and moderate ones are recorded in tools/deps/README.md
echo "== Known vulnerabilities in frontend production dependencies"
(cd frontend && npm audit --omit=dev --audit-level=high) || fail=1

exit $fail
