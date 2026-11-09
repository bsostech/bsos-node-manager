# Third-Party Dependencies

This document describes which third-party code BSOS Node Manager distributes, the license obligations that come with
it, how dependencies are added and updated, and the exceptions currently accepted. The generated inventory is
`third_party/DEPENDENCIES.md`.

## What is distributed

| Artifact | Third-party content | License notices |
|---|---|---|
| Platform image (`backend/Dockerfile`) | Go modules compiled into `server` and `cactl`, the web UI bundle, fonts, the UBI 9 base image | `/usr/share/licenses/bsos-node-manager/` in the image (`LICENSE`, `NOTICE`, `third_party/`) |
| Agent binary | Go modules compiled into the agent | Embedded in the binary (`besu-agent licenses`); source text in `third_party/NOTICES-agent.txt` |
| Web UI (inside the platform image) | `dependencies` in `frontend/package.json`, `public/mockServiceWorker.js` | `third_party/NOTICES-frontend.txt` |
| Node onboarding runbook (HTML) | Font subsets | Comment at the top of the HTML file, `third_party/fonts/` |

Go modules for the backend binaries are listed in `third_party/NOTICES-backend.txt`. `devDependencies`, code
generators (`oapi-codegen`, `openapi-typescript`) and test images are used only at build time and are not
distributed. They are still part of the build supply chain, and their versions are pinned in the same way.

The documentation site (`site/`) produces static pages. Starlight, Astro and the plugins are MIT-licensed. The build
pulls in `sharp`, which ships LGPL-3.0 libvips binaries; they run only on the build machine and are not part of the
generated site, so they are not on the distribution list.

## Rules

### Licenses

Allowed licenses are MIT, BSD-2-Clause, BSD-3-Clause, Apache-2.0, ISC, 0BSD, MPL-2.0 and OFL-1.1 (`ALLOWED` in
`tools/deps/notices.py`). GPL, AGPL, LGPL or an unclear license needs discussion before the dependency is added.

### Pinned versions

| Ecosystem | How |
|---|---|
| Go | `go.sum` is committed |
| npm | `package-lock.json` is committed; builds use `npm ci` |
| GitHub Actions | Pinned to a commit SHA, with the version in a trailing comment |
| Container images | `tag@sha256:...`; the tag is for readers, the digest is what runs |
| Scanners | Pinned version, for example `govulncheck@v1.9.0` |

### Verified downloads

`install.sh` and the offline bundle verify SHA256 checksums (`backend/internal/server/assets/install.sh`).
`GOTOOLCHAIN=auto` in `backend/Dockerfile` downloads the Go toolchain from proxy.golang.org, verified by the Go
checksum database.

## Adding a dependency

1. Check whether the standard library or an existing dependency covers it. Code that fits in a few dozen lines does
   not get a new package.
2. Check that the license is on the allowed list.
3. Check that the project is maintained: recent releases and issue activity, or the OpenSSF Scorecard on
   [deps.dev](https://deps.dev).
4. Run `python3 tools/deps/notices.py` and commit the regenerated lists with the dependency.

## Updates and checks

```bash
tools/deps/check.sh                    # license lists in sync, licenses allowed, known vulnerabilities
python3 tools/deps/notices.py --check  # license lists only
```

`check.sh` runs `notices.py --check`, `govulncheck` on the backend (Core and, when present, Enterprise) and the
agent, and `npm audit --omit=dev --audit-level=high` on the web UI. Run it after changing `go.mod` or
`package.json`. New vulnerabilities are published over time, so run it once a month as well.

Container images are pinned by tag and digest: the Dockerfiles in `backend/` and `agent/`, the compose files in
`backend/` and `deploy/platform/docker-compose/`, the `docker run` in `deploy/agent/build-agent.sh` and the one in
`.github/workflows/prom-rules.yml`. To update one, look up the digest of the new tag in the registry and replace the
tag and digest together.

`frontend/.npmrc` sets `ignore-scripts=true`, so `npm ci` does not run dependency install scripts, a common
supply-chain attack path. A new package that needs an install script needs discussion first. `min-release-age=7`
keeps versions published less than 7 days ago from resolving; it takes effect from npm 11.10, and the npm 10 bundled
with Node 22 ignores it.

## Accepted exceptions

As of 2026-11-05.

| Item | Issue | Reason it is deferred |
|---|---|---|
| `golang.org/x/crypto` (GO-2026-5932, `openpgp`) | The `openpgp` package is unmaintained and unsafe by design | Not imported by our code (govulncheck reports nothing reachable); the fix exists only as an untagged pseudo-version |
| `github.com/joho/godotenv` | Scorecard "Maintained" score 0 | Small and stable; replaceable with a few dozen lines that read `.env` |
| `uplot` | Single maintainer, infrequent releases | Core of the charts, costly to replace; stable |
| No SBOM, image signing or provenance; no scheduled CVE scan of the UBI base image | Relies on digest pinning and upstream updates | Planned with the release process |
| Web UI third-party notices exist only in the image file system | Not visible in the browser | Meets the distribution requirement; a UI view can be added later |

TypeScript 7 is held back: `openapi-typescript` still requires TypeScript 5, and `gen:api` fails under 7.
