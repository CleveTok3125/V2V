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

want="$(id -u "$APP_USER"):$(id -g "$APP_GROUP")"
mkdir -p "$DATA_DIR"
cur="$(stat -c %u:%g "$DATA_DIR")"
if [ "$cur" = "$want" ]; then
	echo "entrypoint: $DATA_DIR ownership OK ($cur), skipping scan"
else
	echo "entrypoint: fixing $DATA_DIR ownership ($cur -> $want)"
	find "$DATA_DIR" -xdev \
		\( ! -user "$APP_USER" -o ! -group "$APP_GROUP" \) \
		-exec chown "$APP_USER:$APP_GROUP" {} +
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

exec "$SU_EXEC_BIN" "$APP_USER:$APP_GROUP" "$SERVER_BIN" "$@"
