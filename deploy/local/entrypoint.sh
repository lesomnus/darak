#!/usr/bin/env bash
# Bring the local stack up the way production does.
#
# The accounts, the group folders and the smb.conf share block all come from
# usersync reading roster.yaml -- the same tool, the same files, the same order
# as deploy/prod. This used to be a shell script reading a users.conf, which
# kept the stack free of a second repository but meant the account path
# exercised here was not the account path that runs there. Everything that only
# exists on the real path -- export, audit, shares --write, the audit-mode
# branch, disabled and reserved users -- went untested as a result.
#
# What differs from production is values, not mechanism:
#   - no TLS, so the session cookie is not marked Secure
#   - the seed lives in the state volume and the derived passwords are printed,
#     because a test stack you cannot log into is not one
#   - the operator group's members come from an env var rather than a .env file
set -euo pipefail

DATA_ROOT=${DARAK_ROOT:-/srv/data}
CONFIG_DIR=${DARAK_CONFIG:-/etc/darak}
STATE_DIR=${DARAK_STATE:-/var/lib/darak}
SEED_FILE=${DARAK_SEED:-$STATE_DIR/seed.secret}
ADMIN_GROUP=${DARAK_ADMIN_GROUP:-admin}
ADMIN_GID=${DARAK_ADMIN_GID:-2000}
ADMIN_MEMBERS=${DARAK_ADMIN_MEMBERS:-alice}

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
die() {
	printf '\033[1;31mfatal:\033[0m %s\n' "$*" >&2
	exit 1
}

[[ -f "$CONFIG_DIR/roster.yaml" ]] ||
	die "no roster at $CONFIG_DIR/roster.yaml -- is deploy/local/config mounted?"
[[ -f "$CONFIG_DIR/usersync.yaml" ]] ||
	die "no config at $CONFIG_DIR/usersync.yaml -- is deploy/local/config mounted?"

# --- layout -----------------------------------------------------------------
#
# BEFORE the accounts, not after. usersync creates a home with MkdirAll, and
# MkdirAll gives every intermediate directory the LEAF's mode -- so an absent
# /srv/data/homes would be created 0700 owned by root, and nobody could traverse
# into their own home. Creating the roots first means MkdirAll only ever makes
# the leaf.
log "layout under $DATA_ROOT"
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

# --- seed -------------------------------------------------------------------
#
# Initial SMB passwords are DERIVED from this seed, so it has to outlive a
# rebuild or every restart would hand out different ones. It is generated into
# the state volume rather than committed: a seed in git is a seed that has
# leaked, and this stack is meant to look like the real one.
if [[ ! -f $SEED_FILE ]]; then
	log "seed"
	(
		umask 077
		head -c 32 /dev/urandom | base64 >"$SEED_FILE"
	)
fi
chmod 0600 "$SEED_FILE"

# smb.conf comes BEFORE the accounts: `usersync apply` registers each SMB
# account with smbpasswd, and smbpasswd refuses to run without a loadable
# config. That used to appear to work only because Debian's samba package ships
# an smb.conf of its own -- which also meant the section seeded below never
# landed, and a second [homes] sat above the one usersync generates. The image
# removes the distro file now, so this dependency is real and explicit.

# --- samba ------------------------------------------------------------------

log "samba (config)"
install -d -m 0755 /var/lib/samba/private /var/log/samba /run/samba

if [[ ! -f /etc/samba/smb.conf ]]; then
	# usersync splices the [homes] and [<team>] shares into a marker block; the
	# global section is the operator's to own, so it is seeded once and never
	# again. The port and log level are the only local touches.
	cat >/etc/samba/smb.conf <<-EOF
		[global]
		   workgroup = WORKGROUP
		   server string = darak (local)
		   security = user
		   passdb backend = tdbsam
		   map to guest = never
		   # One file, named, because darak reads it for the audit records.
		   # `log level = 1` is what makes full_audit emit at all.
		   log level = 1
		   log file = /var/log/samba/audit.log
		   max log size = 5000
		   disable netbios = yes
		   smb ports = 445
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

# `mode: audit` means a directory service owns the accounts and usersync refuses
# to create any. Reading the mode rather than assuming is what keeps the boot
# from failing on the exact day of a cutover -- and having the branch here is
# part of the point of this stack, because it is where that day gets rehearsed.
mode=$(usersync config 2>/dev/null | sed -n 's/^mode:[[:space:]]*//p' | tr -d '"' | head -1)
case "${mode:-manage}" in
audit)
	log "mode: audit -- a directory owns the accounts; verifying instead of applying"
	usersync audit || echo "WARNING: the directory and the roster disagree; see above" >&2
	;;
*)
	# Show what will change before it changes. On a healthy restart this is
	# empty, which is what makes the one time it is not worth reading.
	usersync plan || die "usersync refused the roster; nothing has been changed"
	usersync apply
	;;
esac

# --- operator group ---------------------------------------------------------
#
# Not a roster group: a roster group also gets a team folder and an SMB share,
# and being allowed to manage accounts is not a reason to conjure a shared
# directory. gid 2000 is below the managed band (10000-19999) and above the
# system floor, so usersync neither creates it nor reports it in `audit`.
#
# Applied AFTER usersync, because `apply` sets each user's supplementary groups
# to exactly what the roster says -- adding the group first would be undone.
if [[ -n $ADMIN_GROUP ]]; then
	log "operator group ($ADMIN_GROUP, gid $ADMIN_GID)"
	getent group "$ADMIN_GROUP" >/dev/null || groupadd -g "$ADMIN_GID" "$ADMIN_GROUP"
	for u in ${ADMIN_MEMBERS//,/ }; do
		if ! id "$u" >/dev/null 2>&1; then
			echo "WARNING: $u is in DARAK_ADMIN_MEMBERS but is not an account; skipping" >&2
			continue
		fi
		usermod -aG "$ADMIN_GROUP" "$u"
		log "  $u is an operator"
	done
	[[ -n $ADMIN_MEMBERS ]] ||
		echo "note: DARAK_ADMIN_MEMBERS is empty, so nobody can reach the operator page" >&2
fi

# The share definitions come from the roster too, so a team added there appears
# over SMB without anyone editing smb.conf. testparm-validated before it
# replaces anything, with the previous file kept as .bak.
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

start_samba() {
	winbindd -D
	smbd -D
	# ntlm_auth talks to winbindd; without it every web login fails with a
	# helper error rather than a wrong password, which is worth waiting for
	# rather than discovering at the first login.
	for _ in $(seq 50); do
		wbinfo -p >/dev/null 2>&1 && return 0
		sleep 0.2
	done
	echo "winbindd did not become ready; web logins will fail" >&2
	return 1
}
check_audit_ops
start_samba

# --- banner -----------------------------------------------------------------
#
# The initial passwords are derived rather than fixed, so the only way to know
# them is to ask -- which also exercises `usersync passwd`, the command an
# operator actually runs when onboarding someone. A password the user has since
# changed is NOT reset by a restart (tdbsam is a volume), so what is printed is
# the initial value: after that point it is the value to reset TO, not the one
# in use.
cat <<EOF

  darak is starting.

    web    http://localhost:8080
    smb    //localhost/<username>   and   //localhost/<team>

  Accounts come from $CONFIG_DIR/roster.yaml, applied by usersync -- the same
  tool and the same boot sequence as production. Edit the roster and restart.

  Initial SMB passwords (derived from the seed in the state volume):

EOF
while read -r name; do
	printf '    %-8s %s\n' "$name" "$(usersync passwd "$name" 2>/dev/null | tail -1)"
done < <(usersync export --format csv 2>/dev/null | awk -F, '$1 == "user" { print $2 }')
cat <<EOF

  Operators (members of the "$ADMIN_GROUP" group): ${ADMIN_MEMBERS:-none}

  The accounts are recreated from the roster on every start with their numbers
  pinned, which is what keeps the files in the volume belonging to the right
  people. Destroy the container, bring it back, and stat(1) a file.

EOF

# Anonymous (public-folder) access, off unless DARAK_ANONYMOUS_USER names an
# account with no groups and no SMB (e.g. nobody); see the prod entrypoint.
anon_args=()
if [[ -n ${DARAK_ANONYMOUS_USER:-} ]]; then
	getent passwd "$DARAK_ANONYMOUS_USER" >/dev/null ||
		die "DARAK_ANONYMOUS_USER=$DARAK_ANONYMOUS_USER does not resolve to an account"
	anon_args+=(-anonymous-user "$DARAK_ANONYMOUS_USER")
fi

log "darak"
exec /usr/local/bin/darak \
	-root "$DATA_ROOT" \
	-helper /usr/local/bin/darak-helper \
	-shares "$STATE_DIR/shares.json" \
	-admin-group "$ADMIN_GROUP" \
	-secure-cookies=false \
	"${anon_args[@]}" \
	"$@"
