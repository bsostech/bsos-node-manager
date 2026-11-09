#!/usr/bin/env bash
# Check that every commit in <base>..<head> has a Signed-off-by (DCO), and that at least one such line is the commit author.
# Merge commits do not count (they are made by GitHub or a maintainer). The public repo's public-ci.yml runs this on every PR.
#   tools/check-dco.sh <base> <head>
set -euo pipefail

[ $# -eq 2 ] || { echo "usage: $0 <base> <head>" >&2; exit 2; }
# Store it in a variable first: inside process substitution set -e cannot catch a nonexistent rev, which would turn into "no commits, pass"
revs="$(git rev-list --no-merges "$1..$2")"
fail=0
while read -r sha; do
  [ -n "$sha" ] || continue
  author="$(git log -1 --format='%an <%ae>' "$sha")"
  if ! git log -1 --format='%(trailers:key=Signed-off-by,valueonly)' "$sha" | grep -qxF "$author"; then
    echo "::error::$(git log -1 --format='%h %s' "$sha"): no Signed-off-by from the author: $author"
    fail=1
  fi
done <<<"$revs"
if [ "$fail" -ne 0 ]; then
  echo "Sign off every commit with git commit -s. To add missing sign-offs: git rebase --signoff <base branch>, then force-push (see CONTRIBUTING.md)." >&2
fi
exit "$fail"
