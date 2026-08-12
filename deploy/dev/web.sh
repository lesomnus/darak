#!/bin/sh
# The web container's whole job: make sure node_modules matches the lockfile,
# then hand over to Vite.
#
# Run through `sh <script>` from the compose file rather than as an entrypoint
# of its own, so the bind mount does not need an exec bit that Windows and
# some checkouts will not carry.
set -eu

cd /app

# node_modules is a NAMED VOLUME, not part of the bind mount, and that is the
# whole trick. The host's copy is built for the host: a macOS or musl machine
# has different esbuild and rollup binaries than this image, and mounting it in
# fails at the first import with an error about an optional dependency that is
# in fact installed. Keeping the container's own tree in a volume means the two
# never meet -- and it survives restarts, so `npm ci` runs once, not per boot.
stamp=node_modules/.installed-from-lock
if [ "$(cat package-lock.json)" != "$(cat "$stamp" 2>/dev/null || true)" ]; then
	printf '\033[1;34m==>\033[0m installing dependencies (lockfile changed)\n'
	npm ci
	cp package-lock.json "$stamp"
else
	printf '\033[1;34m==>\033[0m dependencies are current\n'
fi

# --strictPort, because the alternative is worse than a crash: Vite would take
# 5174 instead, the compose file publishes 5173, and the browser would show
# nothing with no error anywhere saying why.
exec npm run dev -- --host 0.0.0.0 --port 5173 --strictPort
