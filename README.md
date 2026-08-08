# darak

An internal file server where the web UI and SMB serve the same files under the
same permissions — because the permissions are the filesystem's, not the
application's.

Design: [`nas-design.md`](./nas-design.md). Helper protocol:
[`docs/helper-protocol.md`](./docs/helper-protocol.md). Accounts are managed
separately by [usersync](https://github.com/lesomnus/usersync).

## The one idea

The server runs as root and must act as the requesting user. Rather than
reimplementing mode bits, supplementary groups, ACLs and the sticky bit — and
being wrong somewhere — it starts a helper process as that user and asks it to do
the work. The kernel decides, with the right credentials, and an errno comes
back.

That only holds if the server never resolves a path itself:

> **The server issues no path-based syscall.** It touches only descriptors the
> helper passed it.

Passing a descriptor carries exactly one permission decision — read or write on
that open file. `openat`, `renameat`, `unlinkat` and `mkdirat` re-check against
the *calling* process, which is root, so a single one of them in server code
silently removes the check from whatever it touches, and it looks completely
ordinary at the call site. `internal/lint` fails the build if one appears.

## Layout

| | |
| --- | --- |
| `cmd/darak` | the server |
| `cmd/darak-helper` | one per user, started already dropped to them |
| `internal/wire` | the protocol between them |
| `internal/helper` | every operation, resolved with `openat2(RESOLVE_BENEATH)` |
| `internal/helperpool` | one helper per user; replaced when their groups change |
| `internal/vfs` | the write protocol and the trash |
| `internal/auth` | passwords, via `ntlm_auth` against Samba's passdb |
| `internal/server` | HTTP |
| `internal/share` | capability links |
| `web` | the browser interface (TypeScript, React, Vite) |
| `internal/ui` | its build output, embedded |
| `internal/lint` | the invariant above |
| `internal/integration` | real users, real modes, real Samba — in a container |

## Running

```sh
go build -o darak ./cmd/darak
go build -o darak-helper ./cmd/darak-helper

sudo ./darak -root /srv/data -helper ./darak-helper -secure-cookies=false
```

Root is required: helpers are started as the requesting user. `winbindd` must be
running even on a standalone Samba, because `ntlm_auth` is a winbind client.

```
POST   /api/login              {"user":..., "password":...}
POST   /api/logout
GET    /api/whoami
GET    /api/files/<path>       directory -> JSON listing; file -> content (Range, ETag)
PUT    /api/files/<path>       upload
DELETE /api/files/<path>       move to the trash
POST   /api/dirs/<path>        mkdir

POST   /api/shares             {"path":..., "password":..., "ttl_hours":...}
GET    /api/shares             your own links
DELETE /api/shares/<token>     revoke
GET    /s/<token>              the public side; no session — the URL is the credential
```

Anything not matching a route is the browser interface.

## The interface

It lives in `web/` — TypeScript and React, built by Vite into
`internal/ui/dist`, which is **committed** and embedded into the binary.

That split is the point: Node is a dependency of *changing* the interface, never
of running or deploying it. `go build` on a clean clone still produces one static
binary with nothing to install beside it, which is what the deployment target —
one machine an administrator maintains by hand — actually needs.

```sh
scripts/build-ui.sh            # rebuild after changing web/; commit the output
scripts/build-ui.sh --check    # CI: fail if the committed output is stale
cd web && npm run dev          # live reload, proxying /api and /s to :8080
```

A Go test fails if the embedded build is missing or empty. Whether it is
*current* can only be checked by rebuilding, which is what `--check` is for —
nothing inside a Go test can know what the sources would have produced.

Paths are relative to the served root and are laid out as
`homes/<user>/…` and `teams/<team>/…`. That is not cosmetic: it is what decides
where an overwritten file's previous version is kept, and the mode a new file
gets — 0600/0700 in a home, 0660/2770 in a team folder, matching the smb.conf
masks so a file uploaded through the web is indistinguishable from one dropped
into the share.

## Writes

Every write is:

```
temp file  ->  fsync  ->  link the old version into the trash  ->  one rename  ->  fsync the parent
```

The link is the part that is easy to get wrong. Renaming the old file out of the
way first — the obvious way to keep a copy — leaves a window where the name does
not exist at all, and a reader landing in it gets `ENOENT` rather than either
version.

There is no lock manager. Concurrent writers are last-write-wins, which is only
reasonable because losing work is always undoable: the replaced version is in the
trash, and so is anything deleted. That guarantee covers the web path only —
SMB writes in place, and the people who mount it are the ones who chose to.

## Share links

A link is a capability: the URL grants a fetch of one file until it expires or is
revoked, and changes nothing on disk. The server opens the file **as the person
who made the link**, so the kernel decides on every fetch — losing access to the
file, or having it deleted, closes the link with no bookkeeping to remember.

Unlike an S3 presigned URL the token is stored rather than signed, so it can be
revoked before it expires. That is deliberate: everything else here closes
immediately (a logout ends a session at once; disabling an account shuts both
paths), and a signature the server could not take back would be the one thing
that outlived all of it.

Links live outside the data volume, because
[nas-design.md](./nas-design.md) §7 requires that volume to stay free of
application state before it becomes a shared filesystem.

## Trying it

```sh
docker compose -f deploy/local/docker-compose.yaml up --build
```

Web on :8080, SMB on :1445, a few accounts, and volumes that keep the data and
the passwords across a rebuild. `/etc/passwd` is deliberately NOT one of them:
accounts are recreated from a file with their numbers pinned, because what the
data knows about its owners is the number. That is the same property the AD
migration rests on, exercised on every start — see
[deploy/local/README.md](./deploy/local/README.md).

Production is the same shape: [`deploy/prod`](./deploy/prod/README.md) runs the
server, Samba and usersync in one container, with the accounts rebuilt from
`roster.yaml` on every start. What differs is TLS, real passwords, and usersync
rather than a stand-in script.

## Tests

```sh
go test ./...                     # unit; no root, no containers
./scripts/verify-integration.sh   # real uids, real Samba, in a throwaway container
```

The integration suite is where the claims that cannot be checked in one process
live: that one user is refused another's `0700` home, that a teammate can edit
what you created in a shared folder, and that `ntlm_auth` accepts what this code
actually sends it.
