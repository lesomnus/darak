# Production image: darak, its helper, usersync, and Samba — in one container.
#
# Samba is INSIDE, not on the host, and that is the point (nas-design.md ADR-9).
# Both paths resolve users through the same /etc/passwd, so there is exactly one
# account database and it is rebuilt from the roster on every start. Splitting
# them would mean the same roster applied in two places, which is two copies to
# keep in step.
#
# Build context is the repository root.

# A commit, not a tag, and not @latest — because usersync has neither published
# yet. Its default branch on GitHub is still the template scaffold (it declares
# a different module path, so @latest does not even resolve), and the branch
# carrying the real code cannot be named here: go install refuses a version
# string containing a slash.
#
# Replace this with a tag the moment usersync publishes one. Pinning an image to
# a commit on an unmerged branch is a thing to fix, not a thing to keep.
ARG USERSYNC_VERSION=eaa3a83f50fd50eb987dcc2b01c608d89d045d7d

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
RUN apt-get update -qq \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
		samba samba-common-bin winbind acl attr ca-certificates tzdata curl inotify-tools \
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
