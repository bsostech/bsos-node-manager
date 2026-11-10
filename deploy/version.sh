# Sourced by deploy/platform/docker-compose/up.sh and deploy/agent/build-agent.sh so the platform and the
# agent get the same version string from the same checkout.
#   resolve_version <repo root> [explicit version]
# Sets VERSION and RELEASE_DATE. Order: the explicit value (--version), git describe when the checkout has
# .git, the repo-root VERSION file, then "dev". VERSION is filled in by git archive and by GitHub's
# "Source code" downloads (export-subst in .gitattributes) with "<tag> <commit date>", so a download
# without .git still knows its release. RELEASE_DATE is the date of the release commit, set only for a
# build of an exact release tag.

# shellcheck shell=bash
# A release tag: v1.2.3, optionally with a pre-release suffix such as -rc.1
RELEASE_TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$'

resolve_version() {
  local root="$1" date=""
  VERSION="${2:-}" RELEASE_DATE=""
  # The value ends up inside the server's -ldflags and in file names, so only plain version characters
  if [ -n "$VERSION" ] && [[ ! "$VERSION" =~ ^[0-9A-Za-z._+-]+$ ]]; then
    echo "invalid version '$VERSION': use letters, digits and . _ + - only" >&2
    return 2
  fi
  if [ -z "$VERSION" ] && [ -e "$root/.git" ] && command -v git >/dev/null; then
    VERSION="$(git -C "$root" describe --tags --always --dirty 2>/dev/null || true)"
    date="$(git -C "$root" log -1 --format=%cs 2>/dev/null || true)"
    # e.g. the checkout belongs to another user and git refuses it (safe.directory) under sudo
    [ -n "$VERSION" ] || echo "warning: git cannot read the version of $root (try: git config --global --add safe.directory $root); falling back to VERSION or dev" >&2
  fi
  if [ -z "$VERSION" ] && [ -f "$root/VERSION" ]; then
    local line
    line="$(head -n 1 "$root/VERSION")"
    case "$line" in
      '$Format'*) ;;                       # a plain copy of the checkout: never substituted
      ' '*) ;;                             # an archive of a commit with no tag: only the date was filled in
      *) read -r VERSION date <<<"$line" ;;
    esac
  fi
  [ -n "$VERSION" ] || VERSION="dev"
  # describe output for a commit after a tag ends in -<n>-g<sha>, and -dirty marks local changes: neither is a release
  if [[ "$VERSION" =~ $RELEASE_TAG_RE && ! "$VERSION" =~ -[0-9]+-g[0-9a-f]+$ && ! "$VERSION" =~ -dirty$ ]]; then
    RELEASE_DATE="$date"
  fi
}
