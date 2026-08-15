#!/usr/bin/env bash
# Regenerate the control-plane gRPC code from proto/ into internal/control/controlpb,
# which is committed and built by `go build` alone — buf is a dependency of
# CHANGING the contract, never of building or running the server, the same
# property scripts/build-ui.sh keeps for the interface.
#
#   scripts/gen-proto.sh          regenerate
#   scripts/gen-proto.sh --check  regenerate and fail if the committed output is
#                                 stale (for CI; leaves the tree as it was)
set -euo pipefail

cd "$(dirname "$0")/.."

if ! command -v buf >/dev/null; then
	echo "buf is required to regenerate the control-plane code; the committed" >&2
	echo "output in internal/control/controlpb is what 'go build' uses, so this" >&2
	echo "is only needed when proto/ changes." >&2
	exit 1
fi

check=false
[[ "${1:-}" == "--check" ]] && check=true

buf lint
buf generate

if ! $check; then
	echo "==> internal/control/controlpb regenerated; commit it alongside the proto change"
	exit 0
fi

if ! git diff --quiet -- internal/control/controlpb; then
	echo >&2
	echo "internal/control/controlpb is stale: regenerating from proto/ produced" >&2
	echo "something different from what is committed. Run scripts/gen-proto.sh and" >&2
	echo "commit the result." >&2
	echo >&2
	git --no-pager diff --stat -- internal/control/controlpb >&2
	git checkout -- internal/control/controlpb
	exit 1
fi
echo "==> internal/control/controlpb is current"
