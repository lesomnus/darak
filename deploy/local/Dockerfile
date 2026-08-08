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

FROM debian:trixie-slim

# samba      — smbd, so the SMB path is real and not just described
# winbind    — ntlm_auth, which the web path uses to check passwords; it is a
#              winbind client even on a standalone server, so winbindd must run
# smbclient  — for poking at the share from inside the container
RUN apt-get update -qq \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
		samba samba-common-bin winbind smbclient ca-certificates curl \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=build /out/darak /out/darak-helper /usr/local/bin/
COPY deploy/local/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh /usr/local/bin/darak /usr/local/bin/darak-helper

EXPOSE 8080 445

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
