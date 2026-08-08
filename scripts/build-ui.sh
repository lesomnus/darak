#!/usr/bin/env bash
# Rebuild the browser interface into internal/ui/dist, which is committed and
# embedded into the binary.
#
# The output is committed so that `go build` alone produces a working server:
# Node is a dependency of CHANGING the interface, never of running or deploying
# it, which is the property the rest of this project is arranged around.
#
#   scripts/build-ui.sh          rebuild
#   scripts/build-ui.sh --check  rebuild and fail if the committed output is
#                                stale (for CI; leaves the tree as it was)
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v npm >/dev/null; then
	echo "npm is required to rebuild the interface; the committed build in" >&2
	echo "internal/ui/dist is what 'go build' uses, so this is only needed" >&2
	echo "when web/ changes." >&2
	exit 1
fi

check=false
[[ "${1:-}" == "--check" ]] && check=true

(cd web && npm ci --silent 2>/dev/null || npm install --silent)
(cd web && npm run build)

if ! $check; then
	echo "==> internal/ui/dist rebuilt; commit it alongside the web/ change"
	exit 0
fi

if ! git diff --quiet -- internal/ui/dist; then
	echo >&2
	echo "internal/ui/dist is stale: rebuilding it from web/ produced something" >&2
	echo "different from what is committed. Run scripts/build-ui.sh and commit" >&2
	echo "the result." >&2
	echo >&2
	git --no-pager diff --stat -- internal/ui/dist >&2
	git checkout -- internal/ui/dist
	exit 1
fi
echo "==> internal/ui/dist is current"
