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
# Instance root: .env, config/ and data/ live under it. Defaults to the
# app root so a bare run keeps the classic /app layout; multi-instance
# deployments mount an env dir and set V2V_ROOT to it.
V2V_ROOT="${V2V_ROOT:-$APP_ROOT}"
DATA_DIR="$V2V_ROOT/data"

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
if [ "$cur" != "$want" ]; then
	echo "entrypoint: fixing $DATA_DIR ownership ($cur -> $want)"
	# A failed chown must say why, not die silent inside set -e and
	# spam restarts: usually a missing CAP_CHOWN (check cap_add),
	# rootless docker, or a filesystem that forbids chown.
	if ! find "$DATA_DIR" -xdev \
		\( ! -user "$APP_USER" -o ! -group "$APP_GROUP" \) \
		-exec chown "$APP_USER:$APP_GROUP" {} +; then
		echo "FATAL: cannot chown $DATA_DIR (need CAP_CHOWN: check cap_add in compose; rootless docker maps uids differently). Host-side fix: chown -R $want <instance>/data"
		exit 1
	fi
else
	# A correct directory does not mean the files in it are correct. An
	# operator running v2vctl as root leaves the identity stores
	# root-owned, and the server then cannot read its own store while this
	# top-dir check keeps reporting that everything is fine. The recursive
	# scan stays off the fast path, so the two files that fail silently are
	# named instead: everything else in data/ is history and logs.
	fixed=""
	for name in webauthn.json server_identity.json; do
		path="$DATA_DIR/$name"
		if [ ! -e "$path" ]; then
			continue
		fi
		if [ "$(stat -c %u:%g "$path")" = "$want" ]; then
			continue
		fi
		if ! chown "$APP_USER:$APP_GROUP" "$path"; then
			echo "FATAL: cannot chown $path (need CAP_CHOWN: check cap_add in compose; rootless docker maps uids differently). Host-side fix: chown $want <instance>/data/$name"
			exit 1
		fi
		fixed="$fixed $name"
	done
	if [ -n "$fixed" ]; then
		echo "entrypoint: $DATA_DIR OK ($cur), fixed inside:$fixed"
	else
		echo "entrypoint: $DATA_DIR ownership OK ($cur), skipping scan"
	fi
fi

# Read-only mounts cannot be fixed from here: fail closed with the
# exact host-side command instead of booting half-blind. Missing and
# unreadable are different failures: a missing file needs the
# bootstrap, a present but unreadable one needs a permission fix.
# $1 = container path, $2 = bootstrap command for a missing file.
require_readable() {
	path=$1
	bootstrap=$2
	if ! "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" test -e "$path"; then
		echo "FATAL: $path missing; on the host run: $bootstrap"
		exit 1
	fi
	rel=${path#"$V2V_ROOT"/}
	if ! "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" test -r "$path"; then
		echo "FATAL: $path unreadable by $APP_USER; on the host run: chmod o+r $rel (or chown it to the data owner)"
		exit 1
	fi
}
# roles.json carries the hmac_shield and is written 0600, so it is
# intentionally not world-readable: the fix is ownership, never chmod
# o+r. A missing file is still the bootstrap case.
require_readable_owned() {
	path=$1
	bootstrap=$2
	if ! "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" test -e "$path"; then
		echo "FATAL: $path missing; on the host run: $bootstrap"
		exit 1
	fi
	if ! "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" test -r "$path"; then
		echo "FATAL: $path unreadable by $APP_USER; on the host run: chown $want <instance>/config/roles.json (it stays 0600 on purpose)"
		exit 1
	fi
}
require_readable "$V2V_ROOT/.env" "make v2vctl && v2vctl config sync --dir . --to <instance dir>"
require_readable_owned "$V2V_ROOT/config/roles.json" "make v2vctl && v2vctl config sync --dir . --to <instance dir>"

# The identity stores are written by v2vctl on the host and read by the
# server, so a correct owner is not enough: an odd mode or an ACL still
# denies the read, and the server then dies at boot holding a bare path.
# Ownership was already handled above; this proves the read itself.
# Absent files are legitimate, the first boot creates them.
require_readable_if_present() {
	path=$1
	if [ ! -e "$path" ]; then
		return 0
	fi
	if ! "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" test -r "$path"; then
		name=$(basename "$path")
		echo "FATAL: $path unreadable by $APP_USER (owner $(stat -c %u:%g "$path"), mode $(stat -c %a "$path")); on the host run: chown $want <instance>/data/$name && chmod 600 <instance>/data/$name"
		exit 1
	fi
}
require_readable_if_present "$DATA_DIR/webauthn.json"
require_readable_if_present "$DATA_DIR/server_identity.json"

# Writability probe: without it the server dies on its first write
# with a bare "permission denied". Fail here instead, with the fix.
if ! "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" sh -c 'touch "$1/.wtest" && rm "$1/.wtest"' _ "$DATA_DIR"; then
	echo "FATAL: $DATA_DIR not writable by $APP_USER; on the host run: chown -R $want <instance>/data"
	exit 1
fi

exec "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" "$SERVER_BIN" "$@"
