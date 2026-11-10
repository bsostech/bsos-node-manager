#!/usr/bin/env bash
# Checks for deploy/version.sh: the version resolution order (--version > git describe > VERSION file > dev), the release date
# rule, and that a source download made with git archive (as GitHub's "Source code" zip is) knows its tag without .git.
#   tools/test-version.sh
set -euo pipefail

SRC="$(cd "$(dirname "$0")/.." && pwd)"
T="$(mktemp -d "${TMPDIR:-/tmp}/test-version.XXXXXX")"
trap 'rm -rf "$T"' EXIT
export GIT_CONFIG_GLOBAL="$T/gitconfig" GIT_CONFIG_NOSYSTEM=1
git config --global user.name tester
git config --global user.email tester@example.com
git config --global init.defaultBranch main

fail() { echo "FAIL: $*" >&2; exit 1; }
# shellcheck source=../deploy/version.sh
. "$SRC/deploy/version.sh"
check() {  # <dir> <explicit> <want version> <want date>
  resolve_version "$1" "$2"
  [ "$VERSION" = "$3" ] && [ "$RELEASE_DATE" = "$4" ] || fail "$1 ($2): got '$VERSION' '$RELEASE_DATE', want '$3' '$4'"
}

R="$T/repo"
git init -q "$R"
cp "$SRC/VERSION" "$SRC/.gitattributes" "$R/"
echo a > "$R/file" && git -C "$R" add -A && GIT_COMMITTER_DATE=2026-11-10T08:00:00Z git -C "$R" commit -qm one
sha="$(git -C "$R" rev-parse --short HEAD)"

check "$R" "" "$sha" ""                          # no tag yet: the commit hash, no date
check "$R" "v2.0.0" "v2.0.0" ""                  # explicit --version wins, but carries no date of its own
( resolve_version "$R" 'v1 $(id)' ) 2>/dev/null && fail "a --version with a space or \$( ) must be refused"
# A source download before any tag: only the date is filled in, so dev rather than the date
mkdir "$T/untagged" && git -C "$R" archive HEAD | tar -x -C "$T/untagged"
check "$T/untagged" "" "dev" ""
git -C "$R" tag v1.0.0
check "$R" "" "v1.0.0" "2026-11-10"              # exact release tag
echo b > "$R/file"
check "$R" "" "v1.0.0-dirty" ""                  # local changes
git -C "$R" commit -qam two
check "$R" "" "v1.0.0-1-g$(git -C "$R" rev-parse --short HEAD)" ""  # a commit after the tag

# A source download of the tag, without .git: VERSION is filled in by export-subst
mkdir "$T/zip" && git -C "$R" archive v1.0.0 | tar -x -C "$T/zip"
check "$T/zip" "" "v1.0.0" "2026-11-10"
# A plain copy of the checkout without .git: the placeholder is still there, so dev
mkdir "$T/copy" && cp "$R/VERSION" "$T/copy/"
check "$T/copy" "" "dev" ""
check "$T/empty-$$" "" "dev" ""                  # nothing at all

echo "version: all checks passed"
