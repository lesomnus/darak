# darak

An internal file server where the web UI and SMB serve the same files under the
same permissions — because the permissions are the filesystem's, not the
application's.

Design: [`nas-design.md`](./nas-design.md). Helper protocol:
[`docs/helper-protocol.md`](./docs/helper-protocol.md). Accounts are managed
separately by [usersync](https://github.com/lesomnus/usersync).

| | |
| --- | --- |
| [`docs/using.md`](docs/using.md) | for the people who will use it — web, SMB, the trash, what is not recoverable |
| [`docs/access-control.md`](docs/access-control.md) | who can read/write/delete what, as an O/X matrix — measured |
| [`deploy/prod/README.md`](deploy/prod/README.md) | deploying it (docker compose) |
| [`docs/kubernetes.md`](docs/kubernetes.md) | deploying it on Kubernetes, and what breaks there |
| [`docs/http-api.md`](docs/http-api.md) | the HTTP contract |

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
| `internal/auth` | passwords, via `ntlm_auth` against Samba's passdb, and the sign-in gate |
| `internal/sso` | signing in with a company account; it asserts who, never what |
| `internal/identity` | which account an asserted identity belongs to, and who is waiting to be told |
| `internal/server` | HTTP |
| `internal/share` | capability links |
| `web` | the browser interface (TypeScript, React, Vite) |
| `internal/ui` | `darak ui`: serves its build, unprivileged |
| `internal/uiproc` | how the server reaches that without serving it |
| `internal/lint` | the invariant above |
| `internal/integration` | real users, real modes, real Samba — in a container |

## Running

```sh
go build -o darak ./cmd/darak
go build -o darak-helper ./cmd/darak-helper

sudo ./darak -root /srv/data -helper ./darak-helper -secure-cookies=false
```

Or as the image CI publishes:

```sh
docker buildx bake              # tests: unit, vet, internal/lint, integration
docker buildx bake app          # the image itself
docker pull ghcr.io/lesomnus/darak:edge
```

`docker-bake.hcl` is the build definition; `.github/workflows/ci.yaml` runs it.
The tests are stages of the production Dockerfile, so CI gates on the same
layers the shipped image is made of rather than on a separate checkout.

Root is required: helpers are started as the requesting user. `winbindd` must be
running even on a standalone Samba, because `ntlm_auth` is a winbind client.

```
POST   /api/login              {"user":..., "password":...}
POST   /api/logout
GET    /api/whoami
POST   /api/password           {"current":..., "new":...} — asks for the current
                               one even though a session exists, and closes this
                               person's other sessions

GET    /api/sso/login          off unless -oidc-issuer is set, 404 otherwise
GET    /api/sso/callback       identity in, account name out; the gate decides
GET    /api/sso/forward        forward-auth: a trusted proxy's verified id_token
GET    /api/sso/notice         one-off message for the page it redirects to

GET    /api/branding           no session — the login page carries the mark too
GET    /api/branding/logo      the -brand-logo image, or 404

GET    /api/search/<path>?q=   walks below <path>, streams matching names as NDJSON

GET    /api/files/<path>       directory -> JSON listing; file -> content (Range, ETag)
PUT    /api/files/<path>       upload
DELETE /api/files/<path>       move to the trash — NOT a delete
POST   /api/dirs/<path>        mkdir, not mkdir -p
GET    /api/mode/<path>        {"mode":"0640","dir":bool,"acl":bool} — acl says the
                               file carries a POSIX ACL the mode bits do not show
POST   /api/mode/<path>        {"mode":"0640"} — octal as a STRING; the kernel
                               decides, except that dropping setgid from a team
                               folder is refused

POST   /api/shares             {"path":..., "password":..., "ttl_hours":...}
GET    /api/shares             your own links
DELETE /api/shares/<token>     revoke
GET    /s/<token>              the public side; no session — the URL is the credential
POST   /s/<token>              password=... for a protected link

GET    /api/teams              teams, owners, members
GET    /api/teams/whoami       which teams you own
POST   /api/teams/<team>/members   {"user":..., "member":...}

GET    /api/admin/whoami       every signed-in user may ask
GET    /api/admin/users        admin group only, below here
GET    /api/admin/disk
GET    /api/admin/audit
GET    /api/admin/activity
POST   /api/admin/users/<user>/smb        {"enabled":...}
POST   /api/admin/users/<user>/password   {"password":...}
```

Anything not matching a route is the browser interface — including a request
that matches a path but not its method, which gets 200 and index.html rather
than 405. [`docs/http-api.md`](docs/http-api.md) is the spec: exact shapes,
status codes, and the several places where what the code does is not what the
method name suggests.

An SSO sign-in resolves to an account by, in order: the directory subject if it
has been seen before; an address an operator has approved (which pins the
subject); then provisioning for a genuinely new person. `-sso-trust-email`
inserts one more step before provisioning — an EXISTING account whose name a
trusted-domain address derives is bound on the spot, no approval — so a roster a
directory already lists does not need an operator to rubber-stamp each member's
first sign-in. It is opt-in and refuses to start without `-oidc-email-domains`,
because trusting an address to name an account is only sound within domains you
control; the subject still pins on first use, so a later reassignment of a
departed member's address cannot ride in on it (and `status: disabled` closes
the account regardless).

## The interface

It lives in `web/` — TypeScript and React, built by Vite into `web/dist`. The
build is **not committed and not in the Go binary**: the image builds it in a
stage of its own and puts it at `/usr/share/darak/ui`.

It is served by `darak ui`, a separate process that refuses to run as root. The
API server is root — it has to start helpers as each user — and a root process
that resolves a request's path does so with every permission check passing, so
it does not serve files at all. It stays the one origin the browser talks to,
and reaches the interface over HTTP, with the session stripped:

| | |
|---|---|
| `-ui-dir DIR` | starts `darak ui -dir DIR` as its own child, as `-ui-user` (default `65533:65533`; never root, nor `-anonymous-user`'s uid), restarting it with backoff if it dies |
| `-ui-upstream URL` | proxies to a `darak ui` deployed on its own |
| neither | serves only the API |

The image does both halves. Run as its default, unprivileged user it is the
interface alone; run with `user: root` it is the API server, with the interface
behind it as a child (or `DARAK_UI_UPSTREAM`).

```sh
cd web && npm run build                       # → web/dist
go run ./cmd/darak ui -dir web/dist -addr :8081   # as yourself, not root
cd web && npm run dev                         # live reload, proxying /api/ and /s/ to :8080
```

Or with nothing installed but Docker — a built server plus Vite over a bind
mount of `web/`, hot reload included:

```sh
docker compose -f deploy/dev/docker-compose.yaml up --build   # then :5173
```

[`deploy/dev/README.md`](deploy/dev/README.md) says what it is and how it
differs from [`deploy/local`](deploy/local/README.md), which serves the
image's build the way a deployment does.

`darak ui` refuses to start on a directory with no `index.html`, or one that
does not reference a built bundle, rather than serving blank pages.

The mark in the corner — and on the login page, and in the tab title — is the
operator's, set with `-brand-name` and `-brand-logo`. The image is read once at
startup and held in memory; a bad path stops the process rather than putting a
broken image on every page. See [`deploy/prod/README.md`](deploy/prod/README.md).

The search box does two things at once, and they are not the same thing.

**The directory you are looking at** is filtered in the browser, on every
keystroke, with no round trip — the listing is already in memory. Fuzzy and
ranked, with the matched characters marked so a result can be argued with.
`ㅎㅇㄹ` finds `회의록`: Korean lead-consonant search is how people here
actually look for a file. Measured at 10–52ms per keystroke over 50,000 entries.

**Everything below it** is `GET /api/search/`, three hundred milliseconds after
you stop typing. The server walks the tree as you — the existing READDIR, one
directory at a time, so the helper protocol did not have to grow an operation on
its security boundary — matches as it goes, and streams the hits as NDJSON.
Bounded at depth 8, 20,000 entries examined, 1,000 results and two seconds, and
the last line of the stream says whether a bound stopped it. Typing again aborts
the request, which drops the walk.

Matching happens on the server there so it can send thirty names instead of
twenty thousand. The price is that this matcher exists twice, in
`web/src/lib/fuzzy.ts` and `internal/fuzzy` — and what stops the two drifting is
`internal/fuzzy/testdata/vectors.json`, a corpus both test suites read. Change
how anything scores on one side and the other side's tests fail.

```sh
cd web && npm test               # node --test, no test runner to install
cd web && npm run gen:fuzzy-vectors   # only when the scoring is meant to change
```

The start page remembers **starred folders and the last ten you were in**, in
localStorage, keyed by username so a shared machine does not show one person's
team folders to the next (`lib/usePlaces.ts`). Nothing is stored server-side;
there is no route that would hold it.

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
