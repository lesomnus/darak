# The control plane

darak decides **who** may change the roster — an admin, a team's owner — and
nothing more. It owns no roster, no database, no account list; the roster stays
the single source of truth and the system's accounts live in NSS and tdbsam,
put there by usersync. So when a decision darak is allowed to make requires the
roster to change, darak asks something else to change it. That seam is the
control plane.

The contract is [`proto/darak/control/v1`](../proto/darak/control/v1/control.proto),
generated into [`control/controlpb`](../control/controlpb). The design follows
[payday](https://github.com/lesomnus/payday): a **resource** per concept, each
with the standard method vocabulary — `Add` / `Get` / `List` / `Watch` / `Erase`
— rather than a verb per operation. There is deliberately no `Patch` or `Apply`:
those are primitives, so every mutation that means something (join a group,
leave it, re-grade a membership) is its own named method.

## Two layers

It is payday's shape without payday's storage. darak is the **gate**; the server
behind the contract is the **sink** that "just does what it is told":

| | |
| --- | --- |
| the caller (a browser, an operator) | reaches **darak** |
| **darak** | decides who may (`IsAdmin`, `MayManageTeam`), then calls the control plane |
| the **control plane** (a service) | edits the roster's SOURCE — a commit, a pull request |
| **usersync** | unchanged and downstream: reconciles the system to the new roster |

darak never creates an account or edits a group itself. It holds a
[`control.Controller`](../internal/control/control.go) — just the generated
resource clients side by side — and writes `ctrl.Enrollment.Add(...)`,
`ctrl.Membership.Erase(...)`. `-control-addr` (env `DARAK_CONTROL_ADDR`) is where
it dials; unset, darak falls back to what it did before the control plane
existed (the pending-approval queue for onboarding, `usersync member` for group
changes), so a deployment without one is exactly as it was.

## The resources

### Enrollment — onboarding, followed live

An unmapped SSO identity used to hit a static "waiting for approval" line, with
no way to tell whether anything was happening. Now it is a resource:

1. On the first sign-in that resolves to no account, darak derives the username
   from the address (the same rule [trust-email](../internal/server/sso.go)
   matches an existing member by) and calls `Enrollment.Add(username, …)`. The
   control plane commits a roster entry and answers with a `Stage`.
2. darak hands the login page an id, which it follows over Server-Sent Events
   (`GET /api/sso/enrollment`). The page renders a live stepper: 요청됨 · 생성
   중 · 승인 대기 · 준비됨.
3. **`READY` is darak's answer, not the control plane's.** The control plane
   edits the roster's source and cannot see whether usersync has applied it —
   but darak's gate asks exactly that. So darak polls its own gate and streams
   `READY` the moment the account can sign in. The person signs in again, and
   trust-email binds them.

`Enrollment.Add` begins the pipeline; `Stage` is where it has got to
(`REQUESTED` → `CREATING` → `AWAITING_APPROVAL` → `READY` / `DENIED` / `FAILED`).
`Watch` streams those transitions; `Get`/`List` are the one-shot reads.

### Membership — a group place as a resource

An operator adding or removing a team member is `Membership.Add` /
`Membership.Erase`; `Grade` re-grades one (reader ↔ member ↔ owner). Because the
control plane edits the roster's source, this works even where the roster is
mounted read-only (a ConfigMap) — which the old in-place `usersync member` could
not. darak decides the caller may manage the group; the control plane records
the change.

`Role` unifies what the roster spells three ways — a writing member, a read-only
reader group, an owner. Only the member role is backed today (the reader is a
group-level concept and the owner a separate list); the rest answer
`Unimplemented` rather than silently doing nothing.

## The server

The control-plane server is the `nas-provisioner` sidecar (in the deployment
repo), which already edited the roster over the GitHub Contents API for SSO
signup. It grows the gRPC services beside its HTTP webhook, editing the same
roster the same way. It runs on the pod's loopback address, so the contract is
reachable only from darak beside it; the transport is insecure for that reason.

## What is NOT here

- **No database.** darak keeps none; the roster is the ledger.
- **No `pdid`/UUIDs, no tenancy wall.** payday's storage machinery is for an app
  that owns its rows. darak owns none, and is a single organisation.
- **No account creation in darak.** The invariant from nas-design survives: the
  roster and usersync own accounts, and the control plane only edits the roster
  the roster's operators already review.
