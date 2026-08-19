#!/usr/bin/env bash
# Bring up darak's WEB tier only — the serving half of the split lives in the
# usersync-smb image (smbd + winbindd + usersync watch --reload-smb).
#
# This pod runs darak, and the samba pieces darak itself needs: winbindd (so
# ntlm_auth can check a password), smbpasswd (so a password can be changed), and
# pdbedit (so the SSO gate can read `status: disabled` from tdbsam). It does NOT
# run smbd and does NOT own the shared state: the SMB pod is authoritative for
# tdbsam, the group/home folders, and the smb.conf shares. This pod keeps only
# its own /etc/passwd in step with the roster, so uids resolve for file I/O.
#
# Accounts here means NSS accounts: `usersync ... --nss-only` writes /etc/passwd,
# /etc/group and memberships and leaves tdbsam, folders, ACLs and quota to the
# SMB pod. Both pods watch the same roster and converge independently; a new
# user's tdbsam account (SMB pod) and passwd entry (this pod) appear within a
# poll of each other.
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
# and only then discovers it has no TLS has already done work that has to be
# reasoned about; refusing first means a bad start left the system as it was.
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
# source. When set, group changes (and onboarding) go through it instead of
# running usersync on this host — which is what a read-only roster (a ConfigMap)
# needs. A loopback sidecar; the transport is insecure.
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
	[[ -r ${DARAK_BRAND_LOGO} ]] ||
		die "cannot read $DARAK_BRAND_LOGO — put the file inside DARAK_CONFIG, which is mounted at /etc/darak"
	args+=(-brand-logo "$DARAK_BRAND_LOGO")
fi

# Signing in with the company account. Off unless an issuer is given.
#
# It adds a way to prove WHO somebody is and nothing else: which account that is
# comes from the mapping in the state directory, and whether that account may
# sign in is still answered by tdbsam on every attempt (the gate reads it with
# pdbedit). So `status: disabled` in the roster keeps closing SMB and the web
# together.
if [[ -n ${DARAK_OIDC_ISSUER:-} ]]; then
	[[ -n ${DARAK_OIDC_CLIENT_ID:-} ]] || die "DARAK_OIDC_ISSUER is set but DARAK_OIDC_CLIENT_ID is not"

	args+=(
		-oidc-issuer "$DARAK_OIDC_ISSUER"
		-oidc-client-id "$DARAK_OIDC_CLIENT_ID"
		-identities "$STATE_DIR/identities.json"
		-identity-requests "$STATE_DIR/identity-requests.json"
		-identity-journal "$STATE_DIR/identity-journal.jsonl"
	)

	if [[ ${DARAK_SSO_FORWARD_AUTH:-} == "1" ]]; then
		args+=(-sso-forward-auth)
	else
		[[ -n ${DARAK_OIDC_REDIRECT_URL:-} ]] ||
			die "DARAK_OIDC_ISSUER is set but DARAK_OIDC_REDIRECT_URL is not — it must be the address a browser actually reaches this server on, e.g. https://darak.example.com/api/sso/callback (or set DARAK_SSO_FORWARD_AUTH=1 to let a reverse proxy do the flow)"
		args+=(-oidc-redirect-url "$DARAK_OIDC_REDIRECT_URL")
	fi
	if [[ -n ${DARAK_OIDC_TENANT:-} ]]; then
		args+=(-oidc-tenant "$DARAK_OIDC_TENANT")
	fi
	if [[ -n ${DARAK_OIDC_EMAIL_DOMAINS:-} ]]; then
		args+=(-oidc-email-domains "$DARAK_OIDC_EMAIL_DOMAINS")
	fi
	if [[ ${DARAK_SSO_TRUST_EMAIL:-} == "1" ]]; then
		[[ -n ${DARAK_OIDC_EMAIL_DOMAINS:-} ]] ||
			die "DARAK_SSO_TRUST_EMAIL=1 needs DARAK_OIDC_EMAIL_DOMAINS — trusting an address to name an account is only safe within domains you control"
		args+=(-sso-trust-email)
	fi

	if [[ -n ${DARAK_PROVISION_CONFIG:-} ]]; then
		[[ -r ${DARAK_PROVISION_CONFIG} ]] ||
			die "cannot read $DARAK_PROVISION_CONFIG — put it inside DARAK_CONFIG, which is mounted at /etc/darak"
		args+=(-provision-config "$DARAK_PROVISION_CONFIG")
	fi

	# The secret goes in as a FILE, never as an argument and never as an
	# environment variable. argv is world-readable through /proc, and helpers are
	# exec'd from the server and inherit its environment.
	if [[ -n ${DARAK_OIDC_CLIENT_SECRET:-} ]]; then
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

# --- layout -----------------------------------------------------------------
#
# The SMB pod's usersync owns the per-user and per-group folders; this pod only
# ensures the shared ROOTS exist so darak can serve straight away on a cold
# start, and it sets them to the SAME modes the SMB pod does — so the two agree
# rather than fight. State is this pod's own.
install -d -m 0755 "$DATA_ROOT" "$DATA_ROOT/teams"
install -d -m 0711 "$DATA_ROOT/homes"
install -d -m 0700 "$STATE_DIR"

# --- samba (client only) ----------------------------------------------------
#
# No smbd runs here, so this is a GLOBAL-ONLY smb.conf: enough for the three
# clients darak drives — ntlm_auth (through winbindd), smbpasswd, and pdbedit —
# to read the shared tdbsam standalone. No shares and no full_audit: the SMB pod
# serves the shares and writes the audit log, which darak reads off the shared
# volume via -smb-log. An operator who mounts their own smb.conf still wins.
log "samba (client config)"
install -d -m 0755 /var/lib/samba/private /var/log/samba /run/samba

if [[ ! -f /etc/samba/smb.conf ]]; then
	cat >/etc/samba/smb.conf <<-EOF
		[global]
		   workgroup = ${SMB_WORKGROUP:-WORKGROUP}
		   server string = ${SMB_SERVER_STRING:-darak}
		   security = user
		   passdb backend = tdbsam
		   map to guest = never
		   disable netbios = yes
		   log level = 1
	EOF
fi

# --- accounts (NSS only) ----------------------------------------------------

log "accounts (nss-only)"
cd "$CONFIG_DIR"

# Static check first, with no system access: a typo in the roster should be a
# refusal to start, not a half-applied set of accounts.
usersync validate

# `mode: audit` means a directory service owns the accounts; usersync refuses to
# create any. Read the mode rather than assuming this pod still makes accounts.
mode=$(usersync config 2>/dev/null | sed -n 's/^mode:[[:space:]]*//p' | tr -d '"' | head -1)
case "${mode:-manage}" in
audit)
	log "mode: audit — a directory owns the accounts; verifying instead of applying"
	usersync audit || echo "WARNING: the directory and the roster disagree; see above" >&2
	;;
*)
	# --nss-only: /etc/passwd, groups and memberships only. The SMB pod applies
	# the tdbsam accounts and the folders. Foreground so /etc/passwd is populated
	# before darak serves and before the operator group is added on top.
	usersync apply --nss-only
	;;
esac

# --- operator group ---------------------------------------------------------
#
# Membership in this POSIX group is what opens the operator page. It is created
# here rather than declared in roster.yaml because it is not a team. gid 2000
# sits BELOW usersync's managed window, so usersync neither creates it nor
# strips it: `usermod -G` from the nss-only reconcile preserves a below-window
# supplementary group, so the admin group survives every hot-reload. Membership
# is reapplied on every start from DARAK_ADMIN_MEMBERS — derived state, like the
# accounts.
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
		usermod -aG "$ADMIN_GROUP" "$u"
		log "  $u is an operator"
	done
	if [[ -z $ADMIN_MEMBERS ]]; then
		echo "note: DARAK_ADMIN_MEMBERS is empty, so nobody can reach the operator page" >&2
	fi
fi

# --- winbind (for ntlm_auth) ------------------------------------------------
#
# ntlm_auth is a winbind client even on a standalone server, so a password login
# fails as a helper error without winbindd. smbd is NOT started — the SMB pod
# serves. Wait for winbindd here, where a not-ready is one clear line, instead of
# at the first login.
log "winbindd"
winbindd -D
for _ in $(seq 100); do
	wbinfo -p >/dev/null 2>&1 && break
	sleep 0.2
done
wbinfo -p >/dev/null 2>&1 || die "winbindd did not become ready; web logins would all fail"

# --- hot-reload (nss-only) --------------------------------------------------
#
# Keep /etc/passwd in step with the roster without a restart: sessions live in
# process memory, so a restart logs everyone out. `usersync watch --nss-only`
# re-applies the NSS accounts on every roster change (the SMB pod's own watch
# reconciles tdbsam and the folders). In `audit` mode usersync must not apply and
# watch refuses it, so the watcher is only started in manage mode.
if [[ ${mode:-manage} != audit ]]; then
	usersync watch --nss-only &
	log "nss hot-reload watcher (pid $!)"
else
	log "nss hot-reload watcher off (mode=audit)"
fi

# --- go ---------------------------------------------------------------------

log "darak"
exec /usr/local/bin/darak "${args[@]}" "$@"
