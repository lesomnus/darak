#!/usr/bin/env bash
# Bring the server up: accounts from the roster, Samba, then darak.
#
# The accounts are rebuilt from roster.yaml on every start. That is deliberate
# and it is the whole reason this container holds them rather than the host:
# what the data knows about its owners is a NUMBER, and the roster pins the
# numbers, so the record naming them is derived state that can always be made
# again. See nas-design.md ADR-9.
#
# Everything below is idempotent, and usersync refuses rather than guesses — a
# uid that disagrees with the roster stops the start instead of quietly writing
# files for the wrong person.
set -euo pipefail

DATA_ROOT=${DARAK_ROOT:-/srv/data}
CONFIG_DIR=${DARAK_CONFIG:-/etc/darak}
STATE_DIR=${DARAK_STATE:-/var/lib/darak}
ADMIN_GROUP=${DARAK_ADMIN_GROUP:-admin}
ADMIN_GID=${DARAK_ADMIN_GID:-2000}
ADMIN_MEMBERS=${DARAK_ADMIN_MEMBERS:-}

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() {
	printf '\033[1;31mfatal:\033[0m %s\n' "$*" >&2
	exit 1
}

[[ -f "$CONFIG_DIR/roster.yaml" ]] || die "no roster at $CONFIG_DIR/roster.yaml — mount it"
[[ -f "$CONFIG_DIR/usersync.yaml" ]] || die "no config at $CONFIG_DIR/usersync.yaml — mount it"

# Decided here, before anything is changed. A deployment that creates accounts
# and rewrites smb.conf and only then discovers it has no TLS has already done
# work that has to be reasoned about; refusing first means a bad start left the
# system exactly as it was.
args=(
	-root "$DATA_ROOT"
	-helper /usr/local/bin/darak-helper
	-shares "$STATE_DIR/shares.json"
	-admin-group "$ADMIN_GROUP"
)

# Anonymous (public-folder) access. When set, an unauthenticated web request is
# served as this OS account instead of refused, so the folders the roster marks
# `anonymous:` can be read (and, where the mode allows, written) without signing
# in. The account MUST exist, belong to no group, and have no SMB credential:
# the kernel then confines it to world-accessible folders and it can never mount
# over SMB. `nobody` satisfies all three on this image. Unset (the default)
# disables anonymous access entirely.
if [[ -n ${DARAK_ANONYMOUS_USER:-} ]]; then
	getent passwd "$DARAK_ANONYMOUS_USER" >/dev/null ||
		die "DARAK_ANONYMOUS_USER=$DARAK_ANONYMOUS_USER does not resolve to an account (use an existing one with no groups and no SMB, e.g. nobody)"
	args+=(-anonymous-user "$DARAK_ANONYMOUS_USER")
fi

# The control plane: the gRPC address of the service that edits the roster's
# source. When set, group changes (and onboarding, once wired) go through it
# instead of running usersync on this host — which is what a read-only roster
# (a ConfigMap) needs. A loopback sidecar; the transport is insecure.
if [[ -n ${DARAK_CONTROL_ADDR:-} ]]; then
	args+=(-control-addr "$DARAK_CONTROL_ADDR")
fi
if [[ -n ${DARAK_TLS_CERT:-} ]]; then
	[[ -n ${DARAK_TLS_KEY:-} ]] || die "DARAK_TLS_CERT is set but DARAK_TLS_KEY is not"
	[[ -r ${DARAK_TLS_CERT} ]] || die "cannot read $DARAK_TLS_CERT — is the TLS directory mounted?"
	[[ -r ${DARAK_TLS_KEY} ]] || die "cannot read $DARAK_TLS_KEY"
	args+=(-tls-cert "$DARAK_TLS_CERT" -tls-key "$DARAK_TLS_KEY")
elif [[ ${DARAK_BEHIND_PROXY:-} != "1" ]]; then
	# Session cookies are Secure, so over plain HTTP the browser never sends one
	# back: logins appear to work and nothing happens. Either terminate TLS here
	# or say explicitly that something in front does.
	die "no TLS: set DARAK_TLS_CERT and DARAK_TLS_KEY, or DARAK_BEHIND_PROXY=1 if a reverse proxy terminates it"
fi

# The mark in the corner and in the tab title. Both optional; without them the
# interface uses its own glyph and calls itself "파일 서버".
if [[ -n ${DARAK_BRAND_NAME:-} ]]; then
	args+=(-brand-name "$DARAK_BRAND_NAME")
fi
if [[ -n ${DARAK_BRAND_LOGO:-} ]]; then
	# Checked here for the same reason TLS is, one paragraph up: a path that is
	# not there should stop the start, not produce a page with a broken image in
	# the corner of it that nobody reports for a week.
	[[ -r ${DARAK_BRAND_LOGO} ]] ||
		die "cannot read $DARAK_BRAND_LOGO — put the file inside DARAK_CONFIG, which is mounted at /etc/darak"
	args+=(-brand-logo "$DARAK_BRAND_LOGO")
fi

# Signing in with the company account. Off unless an issuer is given.
#
# It adds a way to prove WHO somebody is and nothing else: which account that
# is comes from the mapping in the state directory, and whether that account
# may sign in is still answered by tdbsam on every attempt. So `status:
# disabled` in the roster keeps closing SMB and the web together.
if [[ -n ${DARAK_OIDC_ISSUER:-} ]]; then
	[[ -n ${DARAK_OIDC_CLIENT_ID:-} ]] || die "DARAK_OIDC_ISSUER is set but DARAK_OIDC_CLIENT_ID is not"

	args+=(
		-oidc-issuer "$DARAK_OIDC_ISSUER"
		-oidc-client-id "$DARAK_OIDC_CLIENT_ID"
		-identities "$STATE_DIR/identities.json"
		-identity-requests "$STATE_DIR/identity-requests.json"
		-identity-journal "$STATE_DIR/identity-journal.jsonl"
	)

	# Two ways to sign in with the company account:
	#
	#   forward-auth  A reverse proxy (oauth2-proxy behind Traefik's ForwardAuth)
	#                 authenticates and hands the verified id_token over on the
	#                 Authorization header. darak still verifies it, but runs no
	#                 code flow — so no redirect URL and no client secret, and
	#                 -oidc-client-id is the AUDIENCE the token must carry (the
	#                 proxy's client id).
	#   code flow     darak is the OIDC client itself and needs a redirect URL and
	#                 (for a confidential client) a secret.
	if [[ ${DARAK_SSO_FORWARD_AUTH:-} == "1" ]]; then
		args+=(-sso-forward-auth)
	else
		[[ -n ${DARAK_OIDC_REDIRECT_URL:-} ]] ||
			die "DARAK_OIDC_ISSUER is set but DARAK_OIDC_REDIRECT_URL is not — it must be the address a browser actually reaches this server on, e.g. https://darak.example.com/api/sso/callback (or set DARAK_SSO_FORWARD_AUTH=1 to let a reverse proxy do the flow)"
		args+=(-oidc-redirect-url "$DARAK_OIDC_REDIRECT_URL")
	fi
	# `if`, not `[[ ... ]] && ...`: under `set -e` the one-liner form exits the
	# script when the test is false, so an unset optional variable would stop the
	# container from starting.
	if [[ -n ${DARAK_OIDC_TENANT:-} ]]; then
		args+=(-oidc-tenant "$DARAK_OIDC_TENANT")
	fi
	if [[ -n ${DARAK_OIDC_EMAIL_DOMAINS:-} ]]; then
		args+=(-oidc-email-domains "$DARAK_OIDC_EMAIL_DOMAINS")
	fi
	# Trust-email: an existing member's first SSO binds without an operator
	# approval when a trusted-domain address names their account. darak refuses
	# this without an email allow-list; catch it here with a clearer message than
	# a startup abort halfway through boot.
	if [[ ${DARAK_SSO_TRUST_EMAIL:-} == "1" ]]; then
		[[ -n ${DARAK_OIDC_EMAIL_DOMAINS:-} ]] ||
			die "DARAK_SSO_TRUST_EMAIL=1 needs DARAK_OIDC_EMAIL_DOMAINS — trusting an address to name an account is only safe within domains you control"
		args+=(-sso-trust-email)
	fi

	# Auto-provisioning. Off unless a rules file is named, and it is read from
	# the config mount because it is a reviewed artifact like the roster: it can
	# turn a sign-in into an account with nobody clicking approve.
	if [[ -n ${DARAK_PROVISION_CONFIG:-} ]]; then
		[[ -r ${DARAK_PROVISION_CONFIG} ]] ||
			die "cannot read $DARAK_PROVISION_CONFIG — put it inside DARAK_CONFIG, which is mounted at /etc/darak"
		args+=(-provision-config "$DARAK_PROVISION_CONFIG")
	fi

	# The secret goes in as a FILE, never as an argument and never as an
	# environment variable. argv is world-readable through /proc, and helpers
	# are exec'd from the server and inherit its environment — so a secret kept
	# in either is readable by the very users this server's permissions exist to
	# separate. A deployment that only has it as a variable writes it out here,
	# where the shell holds it and darak never sees the variable at all.
	if [[ -n ${DARAK_OIDC_CLIENT_SECRET:-} ]]; then
		# The umask is set in a SUBSHELL. Setting it here would apply to every
		# file this script creates afterwards -- including the generated smb.conf,
		# which several things below have to be able to read.
		(
			umask 077
			printf '%s' "$DARAK_OIDC_CLIENT_SECRET" >"$STATE_DIR/oidc.secret"
		)
		unset DARAK_OIDC_CLIENT_SECRET
		args+=(-oidc-client-secret-file "$STATE_DIR/oidc.secret")
	elif [[ -n ${DARAK_OIDC_CLIENT_SECRET_FILE:-} ]]; then
		[[ -r ${DARAK_OIDC_CLIENT_SECRET_FILE} ]] ||
			die "cannot read $DARAK_OIDC_CLIENT_SECRET_FILE"
		args+=(-oidc-client-secret-file "$DARAK_OIDC_CLIENT_SECRET_FILE")
	fi
fi

# Order matters here, and it is not the obvious one.
#
# The layout roots come FIRST because usersync creates a home with MkdirAll,
# and MkdirAll gives every intermediate directory the LEAF's mode -- an absent
# /srv/data/homes would be created 0700 owned by root and nobody could traverse
# into their own home.
#
# smb.conf comes before the accounts because `usersync apply` registers each
# SMB account with smbpasswd, and smbpasswd refuses to run without a loadable
# config. That used to work only because Debian's samba package ships an
# smb.conf; with that removed (see the Dockerfile) the dependency is real.

# --- layout -----------------------------------------------------------------

# The root has to be traversable by everyone; what is private is underneath.
install -d -m 0755 "$DATA_ROOT" "$DATA_ROOT/teams"

# homes is 0711, NOT 0755: traversal needs only the x bit, and the r bit would
# only ever be used to list everybody's username. Each person still reaches
# their own home (openat2 from the helper, and Samba's [homes] with
# `path = .../%U`, both only traverse), and nothing needs to enumerate the
# parent -- the interface navigates straight to homes/<user>.
#
# teams stays 0755 on purpose: a team NAME is not personal, seeing which teams
# exist is useful, and the folders themselves are 2770 so a non-member still
# cannot enter one.
install -d -m 0711 "$DATA_ROOT/homes"
install -d -m 0700 "$STATE_DIR"

# --- samba ------------------------------------------------------------------

log "samba (config)"
install -d -m 0755 /var/lib/samba/private /var/log/samba /run/samba

if [[ ! -f /etc/samba/smb.conf ]]; then
	# The marker block usersync manages needs a file to be spliced into, and the
	# global section is the operator's to own — so seed it once and never again.
	cat >/etc/samba/smb.conf <<-EOF
		[global]
		   workgroup = ${SMB_WORKGROUP:-WORKGROUP}
		   server string = ${SMB_SERVER_STRING:-darak}
		   security = user
		   passdb backend = tdbsam
		   map to guest = never
		   # One file, named, because darak reads it for the audit records.
		   # `log level = 1` is what makes full_audit emit at all.
		   log level = 1
		   log file = /var/log/samba/audit.log
		   max log size = 5000
		   disable netbios = yes
		   # --- who changed what -------------------------------------------
		   #
		   # full_audit reports the AUTHENTICATED SMB username for every
		   # operation, including from a mounted share -- a kernel cifs mount is
		   # still SMB underneath, so `rm` through a mountpoint arrives here the
		   # same as any other client. That is why this and not the kernel audit
		   # subsystem, which is not namespaced and which a container cannot
		   # register with at all.
		   #
		   # In [global] so it covers every share, including the ones usersync
		   # generates in its marker block -- which therefore needs no knowledge
		   # of any of this.
		   #
		   # The operation names are the *at() forms in Samba 4.x. Getting one
		   # wrong does NOT quietly disable auditing: smb_full_audit_connect
		   # fails the CONNECT, and the share goes offline. Hence the check
		   # below, before smbd is ever started.
		   vfs objects = full_audit
		   full_audit:prefix = %u|%I|%S
		   full_audit:success = create_file mkdirat unlinkat renameat
		   full_audit:failure = none
		   full_audit:syslog = no
		   # Everything below the marker is generated from the roster; edit the
		   # roster, not this file.
	EOF
fi

# --- accounts ---------------------------------------------------------------

log "accounts"
cd "$CONFIG_DIR"

# Static check first, with no system access at all: a typo in the roster should
# be a refusal to start, not a half-applied set of accounts.
usersync validate

# `mode: audit` means a directory service owns the accounts now, and usersync
# refuses to create any. Running apply anyway would fail the boot on the exact
# day of the cutover — so read the mode rather than assuming this container is
# still the thing that makes accounts.
mode=$(usersync config 2>/dev/null | sed -n 's/^mode:[[:space:]]*//p' | tr -d '"' | head -1)
case "${mode:-manage}" in
audit)
	log "mode: audit — a directory owns the accounts; verifying instead of applying"
	# A disagreement is worth shouting about but not worth refusing to serve
	# over: the files are still there and still owned by the right numbers.
	usersync audit || echo "WARNING: the directory and the roster disagree; see above" >&2
	;;
*)
	# Show what will change before it changes. On a healthy restart this is
	# empty, which makes the one time it is not worth reading.
	usersync plan || die "usersync refused the roster; nothing has been changed"
	usersync apply
	;;
esac

# --- operator group ---------------------------------------------------------
#
# Membership in this POSIX group is what opens the operator page. It is created
# here rather than declared in roster.yaml because it is not a team: a roster
# group gets a group folder and an SMB share, and an administrator being able to
# manage accounts should not also create a shared directory nobody asked for.
#
# gid 2000 sits BELOW usersync's managed window (10000-19999) and above the
# system floor, so usersync neither creates it nor complains about it, and
# `usersync audit` stays quiet. Change DARAK_ADMIN_GID only before the first
# start: it ends up on nothing, but a group whose number moves stops naming the
# people who were in it.
#
# Membership is reapplied on every start from DARAK_ADMIN_MEMBERS, for the same
# reason the accounts are: this container's /etc is derived state.
if [[ -n $ADMIN_GROUP ]]; then
	log "operator group ($ADMIN_GROUP, gid $ADMIN_GID)"
	if ! getent group "$ADMIN_GROUP" >/dev/null; then
		groupadd -g "$ADMIN_GID" "$ADMIN_GROUP" ||
			die "could not create the $ADMIN_GROUP group at gid $ADMIN_GID"
	fi
	for u in ${ADMIN_MEMBERS//,/ }; do
		if ! id "$u" >/dev/null 2>&1; then
			echo "WARNING: $u is in DARAK_ADMIN_MEMBERS but is not an account; skipping" >&2
			continue
		fi
		# usermod -aG is additive, and this runs after usersync has already set
		# each user's supplementary groups from the roster -- so the admin group
		# survives the reconcile instead of racing it.
		usermod -aG "$ADMIN_GROUP" "$u"
		log "  $u is an operator"
	done
	if [[ -z $ADMIN_MEMBERS ]]; then
		echo "note: DARAK_ADMIN_MEMBERS is empty, so nobody can reach the operator page" >&2
	fi
fi

# Shares come from the same roster the accounts do, so a new team appears on
# both paths from one edit.
usersync shares --write

# A wrong operation name in full_audit:success does not degrade to "no
# auditing" -- it makes every share REFUSE TO CONNECT, and testparm does not
# catch it (verified: a config naming `mkdir` passes testparm and then breaks
# every share). So ask the module itself, before smbd is serving anything.
#
# The names live NUL-terminated in the .so, so tr does what strings would
# without pulling binutils into the image.
check_audit_ops() {
	local so missing=()
	so=$(ls /usr/lib/*/samba/vfs/full_audit.so 2>/dev/null | head -1)
	if [[ -z $so ]]; then
		missing=(full_audit.so)
	else
		for op in create_file mkdirat unlinkat renameat; do
			tr '\0' '\n' <"$so" | grep -qx "$op" || missing+=("$op")
		done
	fi
	if ((${#missing[@]})); then
		echo "WARNING: this Samba does not know: ${missing[*]}" >&2
		echo "  Removing the audit block rather than serving shares that refuse to connect." >&2
		echo "  The activity page will show web events only." >&2
		sed -i '/vfs objects = full_audit/d; /full_audit:/d' /etc/samba/smb.conf
	fi
}
check_audit_ops

winbindd -D
smbd -D

# ntlm_auth is a winbind client even on a standalone server. Without winbindd
# every web login fails as a helper error rather than a wrong password, so it is
# worth waiting for here instead of discovering at the first login.
for _ in $(seq 100); do
	if wbinfo -p >/dev/null 2>&1; then
		break
	fi
	sleep 0.2
done
wbinfo -p >/dev/null 2>&1 || die "winbindd did not become ready; web logins would all fail"

# --- hot-reload -------------------------------------------------------------
#
# Watch the config directory and re-apply the roster when it changes, so an
# account add / rename / team change lands WITHOUT a restart. Sessions live in
# process memory, so a restart logs everyone out; that cost is what makes people
# route account changes through a redeploy and then not do them. GitOps stays
# the source of truth: something updates roster.yaml in place -- a fixed-name
# ConfigMap whose directory mount kubelet refreshes, or a writable mount an
# operator edits -- and this reconciles the running system to it.
#
# Watch the DIRECTORY, not the file. A ConfigMap update does not rewrite the
# file in place; it writes a new timestamped dir and swaps the `..data` symlink,
# so a watch pinned to the file's inode sees the first change and never another.
# Watching the mount dir catches the symlink swap (create/move) as well as a
# plain in-place edit (close_write).
#
# `manage` mode only. In `audit` a directory service owns the accounts and
# usersync must not apply -- honor the same gate the boot path does.
if [[ ${mode:-manage} != audit ]] && command -v inotifywait >/dev/null 2>&1; then
	(
		while inotifywait -qq -e close_write,create,delete,move "$CONFIG_DIR" >/dev/null 2>&1; do
			sleep 2   # let the ConfigMap symlink swap settle before reading
			log "config changed -> hot-reload"
			# validate first: a bad roster must leave the running system untouched,
			# exactly as it does at boot. apply is idempotent and does no
			# destructive deletes, so re-running it on any change is safe.
			if ( cd "$CONFIG_DIR" && usersync validate >/dev/null 2>&1 && usersync apply && usersync shares --write >/dev/null 2>&1 ); then
				smbcontrol smbd reload-config >/dev/null 2>&1 || true
				log "hot-reload: applied"
			else
				echo "hot-reload: roster invalid or apply failed -- system left unchanged" >&2
			fi
		done
	) &
	log "hot-reload watcher on $CONFIG_DIR (pid $!)"
else
	log "hot-reload watcher off (mode=${mode:-manage}, inotifywait $(command -v inotifywait >/dev/null 2>&1 && echo present || echo missing))"
fi

# --- go ---------------------------------------------------------------------

log "darak"
exec /usr/local/bin/darak "${args[@]}" "$@"
