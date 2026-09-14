#!/bin/sh
# Container entrypoint: start as root, ensure the writable data dir
# belongs to the runtime user, preflight the read-only mounts, then
# drop privileges and exec the server. The server process itself is
# never root.
#
# Test hooks (defaults are the production values): APP_ROOT, APP_USER,
# APP_GROUP, SU_EXEC_BIN, SERVER_BIN.
set -eu

APP_ROOT="${APP_ROOT:-/app}"
APP_USER="${APP_USER:-app}"
APP_GROUP="${APP_GROUP:-app}"
SU_EXEC_BIN="${SU_EXEC_BIN:-su-exec}"
SERVER_BIN="${SERVER_BIN:-$APP_ROOT/server.bin}"
DATA_DIR="$APP_ROOT/data"

# uid_of resolves a username (or passes a numeric uid through).
# gid_of resolves a group name via /etc/group (or passes a numeric
# gid through). Note: `id -g` takes a USERNAME, not a group name, so
# it must never be used for groups. Unresolvable input fails: the
# caller turns it into a FATAL with the offending value.
uid_of() {
	case $1 in '' | *[!0-9]*) ;;
		*) printf '%s' "$1"; return 0 ;;
	esac
	id -u "$1" 2>/dev/null || return 1
}
gid_of() {
	case $1 in '' | *[!0-9]*) ;;
		*) printf '%s' "$1"; return 0 ;;
	esac
	gid=$(awk -F: -v g="$1" '$1==g{print $3; exit}' /etc/group 2>/dev/null)
	[ -n "$gid" ] && printf '%s' "$gid" || return 1
}

want="$(uid_of "$APP_USER"):$(gid_of "$APP_GROUP")" || {
	echo "FATAL: unknown user/group $APP_USER/$APP_GROUP"
	exit 1
}
mkdir -p "$DATA_DIR"
cur="$(stat -c %u:%g "$DATA_DIR")"
if [ "$cur" = "$want" ]; then
	echo "entrypoint: $DATA_DIR ownership OK ($cur), skipping scan"
else
	echo "entrypoint: fixing $DATA_DIR ownership ($cur -> $want)"
	# A failed chown must say why, not die silent inside set -e and
	# spam restarts: usually a missing CAP_CHOWN (check cap_add),
	# rootless docker, or a filesystem that forbids chown.
	if ! find "$DATA_DIR" -xdev \
		\( ! -user "$APP_USER" -o ! -group "$APP_GROUP" \) \
		-exec chown "$APP_USER:$APP_GROUP" {} +; then
		echo "FATAL: cannot chown $DATA_DIR (need CAP_CHOWN: check cap_add in compose; rootless docker maps uids differently). Host-side fix: chown -R $want ./data"
		exit 1
	fi
fi

# Read-only mounts cannot be fixed from here: fail closed with the
# exact host-side command instead of booting half-blind.
"$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" test -r "$APP_ROOT/.env" || {
	echo "FATAL: $APP_ROOT/.env unreadable by $APP_USER; on the host run: chmod o+r .env (or chown it to the data owner)"
	exit 1
}
"$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" test -r "$APP_ROOT/config/roles.json" || {
	echo "FATAL: $APP_ROOT/config/roles.json unreadable by $APP_USER; on the host run: chmod o+r config/roles.json (or chown it to the data owner)"
	exit 1
}

# Writability probe: without it the server dies on its first write
# with a bare "permission denied". Fail here instead, with the fix.
if ! "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" sh -c 'touch "$1/.wtest" && rm "$1/.wtest"' _ "$DATA_DIR"; then
	echo "FATAL: $DATA_DIR not writable by $APP_USER; on the host run: chown -R $want ./data"
	exit 1
fi

exec "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" "$SERVER_BIN" "$@"
