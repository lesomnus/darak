# Production image: darak (the WEB tier), its helper, usersync, and the Samba
# CLIENTS darak drives — winbindd + ntlm_auth (login), smbpasswd (password
# change), pdbedit (the SSO gate reading `status: disabled`). It does NOT run
# smbd: the SMB server is a separate image (ghcr.io/lesomnus/usersync-smb, in the
# usersync repo) with its own `usersync watch --reload-smb` loop, so a darak or
# UI release redeploys only this pod and never bounces file serving.
#
# The two pods share one POSIX tree and one account database on the same node:
# this pod keeps only its own /etc/passwd in step (usersync --nss-only), the SMB
# pod owns tdbsam, the folders, and the smb.conf shares (nas-design.md ADR-9).
#
# Build context is the repository root.

# A commit on usersync's main, not a tag — because usersync has not published a
# tag yet, and @latest would resolve to whatever main is at build time rather
# than a reviewed point. This pin carries `usersync watch` and the --nss-only
# mode, which deploy/prod/entrypoint.sh depends on.
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

# darak runs no smbd, only the samba CLIENTS it drives:
# samba-common-bin — smbpasswd (password change) and smbcontrol
# winbind        — ntlm_auth + winbindd, the web password check (a winbind client
#                  even standalone), and the domain client when an on-prem AD arrives
# samba          — kept ONLY for pdbedit, the one binary that reads tdbsam account
#                  flags for the SSO gate's `status: disabled` check; smbd ships in
#                  it too but is never started
# acl, attr      — POSIX ACLs and xattrs on the data volume
# zfsutils-linux — the `zfs` CLI, for darak's per-uid usage accounting. Userspace
#                  tools only, NOT the kernel module: they talk to the HOST's
#                  already-loaded module over a mounted /dev/zfs, so trixie's 2.3.x
#                  tools drive the host's 2.4.x kernel (the safe direction). darak
#                  treats zfs as best-effort (falls back to du), so a missing
#                  /dev/zfs degrades rather than breaks.
#
# ZFS is under the CDDL, so Debian ships zfsutils-linux in `contrib`, not `main`
# — trixie-slim enables only `main`, so `contrib` is added to the deb822 sources
# first (the classic sources.list branch is a fallback for other bases).
RUN ( [ -f /etc/apt/sources.list.d/debian.sources ] \
		&& sed -i '/^Components:/ s/$/ contrib/' /etc/apt/sources.list.d/debian.sources \
		|| sed -i 's/ main$/ main contrib/' /etc/apt/sources.list ) \
	&& apt-get update -qq \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
		samba samba-common-bin winbind acl attr ca-certificates tzdata curl zfsutils-linux \
	&& rm -rf /var/lib/apt/lists/* \
	# Debian's samba package ships an /etc/samba/smb.conf with [homes],
	# [printers] and [print$] already in it. The entrypoint seeds its own global
	# section only when the file is ABSENT, so removing this makes "seed once"
	# mean what it says. An operator who mounts their own smb.conf still wins.
	&& rm -f /etc/samba/smb.conf

COPY --from=build /out/darak /out/darak-helper /usr/local/bin/
COPY --from=usersync /go/bin/usersync /usr/local/bin/usersync
COPY deploy/prod/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/darak /usr/local/bin/darak-helper \
	/usr/local/bin/usersync /usr/local/bin/entrypoint.sh

# 8080 web only. The SMB pod (usersync-smb) owns 445.
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
	CMD curl -fsS -o /dev/null -k "${DARAK_HEALTH_URL:-http://localhost:8080/}"

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
