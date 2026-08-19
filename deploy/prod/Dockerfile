# Production image: darak, its helper, usersync, and Samba — in one container.
#
# Samba is INSIDE, not on the host, and that is the point (nas-design.md ADR-9).
# Both paths resolve users through the same /etc/passwd, so there is exactly one
# account database and it is rebuilt from the roster on every start. Splitting
# them would mean the same roster applied in two places, which is two copies to
# keep in step.
#
# Build context is the repository root.

# A commit on usersync's main, not a tag — because usersync has not published a
# tag yet, and @latest would resolve to whatever main is at build time rather
# than a reviewed point. This pin carries `usersync watch` and the --nss-only
# mode, which deploy/web/entrypoint.sh depends on.
#
# Replace this with a tag the moment usersync publishes one.
ARG USERSYNC_VERSION=537b61bc7f447a9f0b6cf09ee4afadb816c75438

FROM golang:1.26-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/darak ./cmd/darak \
	&& CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/darak-helper ./cmd/darak-helper

# The tests, as a build stage, so CI gates on the same layers the image is made
# of rather than on a separate checkout that could differ.
#
# `internal/lint` runs here too: it is the check that the server never resolves
# a path itself, and it is a plain Go test precisely so it cannot be skipped by
# forgetting a step in a pipeline.
FROM build AS test
RUN --mount=type=cache,target=/root/.cache/go-build \
	go vet ./... && go test ./...

# usersync owns the accounts. It is a separate repository, pinned above so an
# image is reproducible.
FROM golang:1.26-trixie AS usersync
ARG USERSYNC_VERSION
RUN CGO_ENABLED=0 go install -trimpath \
	github.com/lesomnus/usersync@${USERSYNC_VERSION} \
	|| { \
		echo; \
		echo "Could not fetch usersync@${USERSYNC_VERSION}."; \
		echo; \
		echo "  A branch name will not work: go install refuses a version string"; \
		echo "  containing a slash. Use a tag, or a commit hash:"; \
		echo "    --build-arg USERSYNC_VERSION=\$(git rev-parse HEAD)   # in a usersync checkout"; \
		echo; \
		echo "  If this said the module declares a different path, the ref predates"; \
		echo "  the module rename — usersync's default branch is still the template."; \
		echo; \
		exit 1; \
	} >&2

FROM debian:trixie-slim AS app

# samba     — smbd, the other access path
# winbind   — ntlm_auth, which the web path uses to check passwords, and the
#             domain client when an on-prem AD arrives
# acl, attr     — POSIX ACLs and xattrs on the data volume
# inotify-tools — inotifywait, the entrypoint's roster hot-reload watcher
# zfsutils-linux — the `zfs` CLI, for per-uid usage accounting (darak) and quota
#             enforcement (usersync). Userspace tools only, NOT the kernel module:
#             they talk to the HOST's already-loaded module over a mounted
#             /dev/zfs, so trixie's 2.3.x tools drive the host's 2.4.x kernel (the
#             safe direction — older userspace, newer kernel). Both callers treat
#             zfs as best-effort (darak falls back to du, usersync warns and skips
#             enforcement), so a missing /dev/zfs degrades rather than breaks.
#
# ZFS is under the CDDL, so Debian ships zfsutils-linux in `contrib`, not `main`
# — trixie-slim enables only `main`, so `contrib` is added to the deb822 sources
# first (the classic sources.list branch is a fallback for other bases).
RUN ( [ -f /etc/apt/sources.list.d/debian.sources ] \
		&& sed -i '/^Components:/ s/$/ contrib/' /etc/apt/sources.list.d/debian.sources \
		|| sed -i 's/ main$/ main contrib/' /etc/apt/sources.list ) \
	&& apt-get update -qq \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
		samba samba-common-bin winbind acl attr ca-certificates tzdata curl inotify-tools zfsutils-linux \
	&& rm -rf /var/lib/apt/lists/* \
	# Debian's samba package ships an /etc/samba/smb.conf with [homes],
	# [printers] and [print$] already in it. The entrypoint seeds its own global
	# section only when the file is ABSENT, so leaving this here meant the seeded
	# section never landed, print shares were exposed on a file server, and a
	# SECOND [homes] sat above the one usersync generates -- with Samba merging
	# the two by last-wins per parameter. Removing it makes "seed once" mean what
	# it says. An operator who mounts their own smb.conf still wins.
	&& rm -f /etc/samba/smb.conf

COPY --from=build /out/darak /out/darak-helper /usr/local/bin/
COPY --from=usersync /go/bin/usersync /usr/local/bin/usersync
COPY deploy/prod/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/darak /usr/local/bin/darak-helper \
	/usr/local/bin/usersync /usr/local/bin/entrypoint.sh

# 8080 web, 445 SMB. With host networking neither is published; both are the
# host's own ports.
EXPOSE 8080 445

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
	CMD curl -fsS -o /dev/null -k "${DARAK_HEALTH_URL:-http://localhost:8080/}"

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]



# The WEB-ONLY image (deploy/web): darak plus the samba CLIENTS it drives, and
# NO smbd. It reuses the same build/test/usersync stages as `app` above — the
# same binaries and the same pinned usersync — and differs only in what it
# installs and runs. Published as a SEPARATE image (docker-bake.hcl `web`) so a
# darak or UI change redeploys only this pod and never bounces the SMB server,
# which now lives in the usersync-smb image with its own reconcile loop.
FROM debian:trixie-slim AS app-web

# Same recipe as `app` MINUS inotify-tools — the bash roster watcher, replaced
# here by `usersync watch --nss-only`. `samba` is kept even though smbd never
# starts: it is the only package that ships pdbedit, which darak's SSO gate reads
# to enforce `status: disabled` from tdbsam (winbind gives ntlm_auth + winbindd,
# samba-common-bin gives smbpasswd). See `app` above for the contrib/zfs notes
# and why the distro's own smb.conf is removed.
RUN ( [ -f /etc/apt/sources.list.d/debian.sources ] \
		&& sed -i '/^Components:/ s/$/ contrib/' /etc/apt/sources.list.d/debian.sources \
		|| sed -i 's/ main$/ main contrib/' /etc/apt/sources.list ) \
	&& apt-get update -qq \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
		samba samba-common-bin winbind acl attr ca-certificates tzdata curl zfsutils-linux \
	&& rm -rf /var/lib/apt/lists/* \
	&& rm -f /etc/samba/smb.conf

COPY --from=build /out/darak /out/darak-helper /usr/local/bin/
COPY --from=usersync /go/bin/usersync /usr/local/bin/usersync
COPY deploy/web/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/darak /usr/local/bin/darak-helper \
	/usr/local/bin/usersync /usr/local/bin/entrypoint.sh

# 8080 web only. The SMB pod owns 445.
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
	CMD curl -fsS -o /dev/null -k "${DARAK_HEALTH_URL:-http://localhost:8080/}"

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
