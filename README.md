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
```

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
trash, and so is anything deleted.

## Tests

```sh
go test ./...                     # unit; no root, no containers
./scripts/verify-integration.sh   # real uids, real Samba, in a throwaway container
```

The integration suite is where the claims that cannot be checked in one process
live: that one user is refused another's `0700` home, that a teammate can edit
what you created in a shared folder, and that `ntlm_auth` accepts what this code
actually sends it.
