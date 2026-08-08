#!/usr/bin/env bash
# Run the integration tests in a throwaway container.
#
# They assert the one property nothing else can: that the kernel refuses one user
# access to another's files because of who the helper process is running as. That
# needs real uids, real groups and real modes — so it needs root, and root on a
# developer's machine is the wrong place to create accounts and chown /srv.
#
# The image is built from a tar on stdin, so this works when the docker daemon is
# a sidecar and cannot see the local filesystem.
set -euo pipefail

cd "$(dirname "$0")/.."

tag="${1:-darak-test}"

echo "==> building $tag"
tar -c --exclude=.git --exclude=dist . | docker build -q -f Dockerfile.test -t "$tag" -

echo "==> running"
docker run --rm "$tag"
