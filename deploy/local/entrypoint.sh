#!/usr/bin/env bash
# Bring the local stack up: accounts, layout, Samba, then the server.
#
# Everything here is idempotent, because the container filesystem is discarded on
# every rebuild while the volumes are not. Accounts are recreated from
# users.conf with their numbers pinned, so the data — which knows its owners only
# as uid and gid — still belongs to the right people afterwards. That is the same
# property the AD migration depends on, exercised every time this starts.
#
# In production usersync owns accounts and generates the share definitions. This
# script stands in for it so the stack needs no second repository; where the two
# would disagree, usersync is right.
set -euo pipefail

DATA_ROOT=${DARAK_ROOT:-/srv/data}
USERS_CONF=${DARAK_USERS:-/etc/darak/users.conf}
DEFAULT_PASSWORD=${DARAK_DEFAULT_PASSWORD:-darak}
STATE_DIR=${DARAK_STATE:-/var/lib/darak}

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

# --- accounts ---------------------------------------------------------------

ensure_group() { # name gid
	if ! getent group "$1" >/dev/null; then
		groupadd -g "$2" "$1"
	fi
}

ensure_user() { # name uid groups
	local name=$1 uid=$2 groups=${3:-}
	if ! getent passwd "$name" >/dev/null; then
		# A user private group with gid == uid, then the account: the same shape
		# usersync creates, so a file made here and one made there look alike.
		getent group "$name" >/dev/null || groupadd -g "$uid" "$name"
		useradd -M -u "$uid" -g "$uid" \
			-d "$DATA_ROOT/homes/$name" \
			-s /usr/sbin/nologin \
			"$name"
		# No interactive login: the unix password stays locked and SMB carries
		# its own credential.
		usermod -L "$name"
	fi
	# Supplementary groups are replaced, not added to, so this file is the whole
	# truth about team membership.
	usermod -G "$groups" "$name"
}

ensure_smb_password() { # name
	# Only when there is no entry yet. Re-running must not undo a password the
	# user changed — that is why /var/lib/samba is a volume.
	if ! pdbedit -L 2>/dev/null | grep -q "^$1:"; then
		printf '%s\n%s\n' "$DEFAULT_PASSWORD" "$DEFAULT_PASSWORD" |
			smbpasswd -a -s "$1" >/dev/null
		smbpasswd -e "$1" >/dev/null
	fi
}

# --- layout -----------------------------------------------------------------

ensure_layout() {
	# The root has to be traversable by everyone; what is private is underneath.
	install -d -m 0755 "$DATA_ROOT" "$DATA_ROOT/homes" "$DATA_ROOT/teams"

	while read -r kind name id _; do
		case "$kind" in
		group)
			local dir="$DATA_ROOT/teams/$name"
			install -d "$dir"
			chgrp "$id" "$dir"
			# chmod after chgrp: changing the owner clears setgid. Without that
			# bit, files created inside take their author's own group and every
			# teammate is read-only on them.
			chmod 2770 "$dir"
			;;
		user)
			local dir="$DATA_ROOT/homes/$name"
			install -d "$dir"
			chown "$id:$id" "$dir"
			chmod 0700 "$dir"
			;;
		esac
	done < <(config_lines)
}

config_lines() {
	sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$USERS_CONF"
}

# --- samba ------------------------------------------------------------------

write_smb_conf() {
	# The mask directives are the ones measured against a real smbd, not the ones
	# they look like they should be: Samba derives the base mode from DOS
	# attributes (0666 for files, 0777 for directories), so the mask alone
	# already yields 0660. `force directory mode` is the one that earns its
	# place — a mask can only clear bits, so nothing else can put setgid back if
	# a parent ever loses it. See usersync's scripts/verify-samba-modes.sh.
	{
		cat <<-EOF
			[global]
			   workgroup = WORKGROUP
			   server string = darak (local)
			   security = user
			   passdb backend = tdbsam
			   map to guest = never
			   log level = 0
			   disable netbios = yes
			   smb ports = 445

			[homes]
			   comment = Home Directories
			   path = $DATA_ROOT/homes/%U
			   browseable = no
			   read only = no
			   valid users = %S
			   create mask = 0600
			   directory mask = 0700
		EOF
		while read -r kind name _ _; do
			[[ $kind == group ]] || continue
			cat <<-EOF

				[$name]
				   comment = $name shared
				   path = $DATA_ROOT/teams/$name
				   browseable = yes
				   read only = no
				   valid users = @$name
				   force group = $name
				   create mask = 0660
				   directory mask = 2770
				   force directory mode = 2770
			EOF
		done < <(config_lines)
	} >/etc/samba/smb.conf
	# testparm warns about these before smbd has had a chance to create them, and
	# a stack that greets you with warnings on every start teaches you to ignore
	# them.
	install -d -m 0755 /var/lib/samba/private /var/log/samba /run/samba
	testparm -s /etc/samba/smb.conf >/dev/null
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

# --- go ---------------------------------------------------------------------

log "accounts from $USERS_CONF"
while read -r kind name id extra; do
	case "$kind" in
	group) ensure_group "$name" "$id" ;;
	user) ensure_user "$name" "$id" "${extra:-}" ;;
	*) echo "ignoring unknown line: $kind $name" >&2 ;;
	esac
done < <(config_lines)

log "layout under $DATA_ROOT"
ensure_layout

log "samba"
write_smb_conf
start_samba
while read -r kind name _ _; do
	if [[ $kind == user ]]; then
		ensure_smb_password "$name"
	fi
done < <(config_lines)

install -d -m 0700 "$STATE_DIR"

cat <<EOF

  darak is starting.

    web    http://localhost:8080
    smb    //localhost/<username>   and   //localhost/<team>

  Accounts come from $USERS_CONF, with the password "$DEFAULT_PASSWORD"
  until someone changes it. Changed passwords survive a restart; the accounts
  themselves are recreated from the file every time, with their numbers pinned,
  which is what keeps the files in the volume belonging to the right people.

EOF

log "darak"
exec /usr/local/bin/darak \
	-root "$DATA_ROOT" \
	-helper /usr/local/bin/darak-helper \
	-shares "$STATE_DIR/shares.json" \
	-secure-cookies=false \
	"$@"
