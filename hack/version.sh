#!/bin/sh
# Prints the build identity for internal/version. At an exact release tag this
# is the tag without its leading "v" (matching the chart/image version); on
# any other commit it is "<branch>-<12-char sha>", so an untagged build names
# itself as one. release.yml passes VERSION explicitly and does not use this.
set -eu
tag=$(git describe --tags --exact-match 2>/dev/null || true)
if [ -n "$tag" ]; then
  echo "${tag#v}"
  exit 0
fi
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo dev)
branch=$(printf "%s" "$branch" | tr "/" "-")
sha=$(git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
echo "${branch}-${sha}"
