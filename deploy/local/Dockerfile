# Local test image: the server, its helper, and the Samba pieces both paths need.
#
# Build context is the repository root, so that internal/ui/dist — the committed
# interface build — is present and `go build` needs no Node.
FROM golang:1.26-trixie AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/darak ./cmd/darak \
	&& CGO_ENABLED=0 go build -trimpath -o /out/darak-helper ./cmd/darak-helper

# usersync owns the accounts here exactly as it does in production. Pinned to a
# commit so the image is reproducible, and so a local stack and a prod stack can
# be put on the same version deliberately rather than by accident.
FROM golang:1.26-trixie AS usersync
ARG USERSYNC_VERSION=fffc22d6b4939bf08d0d6017ca5e39cacf70e2b2
RUN CGO_ENABLED=0 go install -trimpath github.com/lesomnus/usersync@${USERSYNC_VERSION}

FROM debian:trixie-slim

# samba      — smbd, so the SMB path is real and not just described
# winbind    — ntlm_auth, which the web path uses to check passwords; it is a
#              winbind client even on a standalone server, so winbindd must run
# smbclient  — for poking at the share from inside the container
RUN apt-get update -qq \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
		samba samba-common-bin winbind smbclient ca-certificates curl \
		passwd \
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
COPY deploy/local/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh /usr/local/bin/darak /usr/local/bin/darak-helper /usr/local/bin/usersync

EXPOSE 8080 445

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
