#!/bin/sh
# Tests for docker/entrypoint.sh. Needs root (chown) and a `nobody`
# user; skips otherwise. No frameworks: plain asserts, fail fast.
# Covers the accepted trade-off explicitly: top-dir OK + inner file
# wrong owner takes the fast path (see T-blindspot).
set -eu

HERE=$(dirname "$0")
ENTRY="$HERE/entrypoint.sh"

pass=0
fail=0
ok() { pass=$((pass + 1)); echo "ok: $1"; }
bad() { fail=$((fail + 1)); echo "FAIL: $1"; }

if [ "$(id -u)" != "0" ]; then
	echo "SKIP: entrypoint tests need root"
	exit 0
fi
if ! id nobody >/dev/null 2>&1; then
	echo "SKIP: no nobody user"
	exit 0
fi
NOBODY_GROUP=$(id -gn nobody)
NOBODY_IDS="$(id -u nobody):$(id -g nobody)"
echo "env: uid=$(id -u):$(id -g) $(uname -sm)"

sh -n "$ENTRY" || { echo "FAIL: syntax"; exit 1; }

# Sandbox with test doubles: fake su-exec logs the user spec then
# runs the command; fake server records invocation. Called directly
# (never in $()), so exports survive; path comes back in SANDBOX_ROOT.
sandbox() {
	SANDBOX_ROOT=$(mktemp -d)
	mkdir -p "$SANDBOX_ROOT/app/data" "$SANDBOX_ROOT/app/config" "$SANDBOX_ROOT/bin"
	CALL_LOG="$SANDBOX_ROOT/calls.log"
	touch "$CALL_LOG"
	cat > "$SANDBOX_ROOT/bin/su-exec" <<EOF
#!/bin/sh
echo "SU_EXEC_USER=\$1" >> "$CALL_LOG"
shift
exec "\$@"
EOF
	chmod +x "$SANDBOX_ROOT/bin/su-exec"
	cat > "$SANDBOX_ROOT/bin/server" <<EOF
#!/bin/sh
echo "SERVER_INVOKED \$*" >> "$CALL_LOG"
exit 0
EOF
	chmod +x "$SANDBOX_ROOT/bin/server"
	export APP_ROOT="$SANDBOX_ROOT/app" CALL_LOG
	export APP_USER=nobody APP_GROUP="$NOBODY_GROUP" \
		SU_EXEC_BIN="$SANDBOX_ROOT/bin/su-exec" SERVER_BIN="$SANDBOX_ROOT/bin/server"
}
readable_mounts() {
	echo "x=1" > "$APP_ROOT/.env"
	echo '{}' > "$APP_ROOT/config/roles.json"
	chmod 644 "$APP_ROOT/.env" "$APP_ROOT/config/roles.json"
}

# run_entry runs the entrypoint and returns its status with output in
# RUN_OUT. A nonzero exit also echoes the output: without this, set -e
# would kill the suite on `out=$(...)` leaving CI with zero evidence.
run_entry() {
	RUN_OUT=$(sh "$ENTRY" "$@" 2>&1)
	code=$?
	if [ "$code" != "0" ]; then
		echo "entrypoint exited $code: $RUN_OUT"
	fi
	return $code
}

# T-fresh: empty data dir gets owned, args pass through.
sandbox; ROOT=$SANDBOX_ROOT
readable_mounts
run_entry arg1 arg2 || bad "fresh: entrypoint failed"
[ "$(stat -c %u:%g "$APP_ROOT/data")" = "$NOBODY_IDS" ] && ok "fresh: data owned" || bad "fresh: data owner $(stat -c %u:%g "$APP_ROOT/data")"
grep -q "SERVER_INVOKED arg1 arg2" "$CALL_LOG" && ok "fresh: passthrough" || bad "fresh: passthrough"
grep -q "SU_EXEC_USER=nobody:" "$CALL_LOG" && ok "fresh: drop-priv user" || bad "fresh: drop-priv user"
rm -rf "$ROOT"

# T-mismatch: only wrong-owned files change hands.
sandbox; ROOT=$SANDBOX_ROOT
readable_mounts
echo root > "$APP_ROOT/data/a.log"
echo keep > "$APP_ROOT/data/b.log"
chown nobody:"$NOBODY_GROUP" "$APP_ROOT/data/b.log"
ctime_before=$(stat -c %z "$APP_ROOT/data/b.log")
run_entry || bad "mismatch: entrypoint failed"
out=$RUN_OUT
[ "$(stat -c %u "$APP_ROOT/data/a.log")" = "$(id -u nobody)" ] && ok "mismatch: fixed" || bad "mismatch: a.log owner"
[ "$(stat -c %z "$APP_ROOT/data/b.log")" = "$ctime_before" ] && ok "mismatch: correct untouched" || bad "mismatch: b.log touched"
echo "$out" | grep -q "fixing" && ok "mismatch: logged" || bad "mismatch: log branch"
rm -rf "$ROOT"

# T-blindspot: top dir OK + inner wrong owner takes the fast path by
# design (pinned trade-off): skips scan, leaves the file, still execs.
# The server fails closed loudly on its first write to it instead.
sandbox; ROOT=$SANDBOX_ROOT
readable_mounts
chown -R nobody:"$NOBODY_GROUP" "$APP_ROOT/data"
echo root > "$APP_ROOT/data/inner.log"
run_entry || bad "blindspot: entrypoint failed"
out=$RUN_OUT
echo "$out" | grep -q "skipping scan" && ok "blindspot: fast path logged" || bad "blindspot: branch"
[ "$(stat -c %u "$APP_ROOT/data/inner.log")" = "0" ] && ok "blindspot: left as-is" || bad "blindspot: inner changed"
grep -q "SERVER_INVOKED" "$CALL_LOG" && ok "blindspot: still execs" || bad "blindspot: exec"
rm -rf "$ROOT"

# T-prefail-env: missing .env fails closed with the host fix.
sandbox; ROOT=$SANDBOX_ROOT
echo '{}' > "$APP_ROOT/config/roles.json"
chmod 644 "$APP_ROOT/config/roles.json"
if run_entry; then
	bad "prefail-env: must exit nonzero"
else
	echo "$RUN_OUT" | grep -q "chmod o+r .env" && ok "prefail-env: actionable msg" || bad "prefail-env: msg"
fi
rm -rf "$ROOT"

# T-prefail-roles: same for roles.json.
sandbox; ROOT=$SANDBOX_ROOT
echo "x=1" > "$APP_ROOT/.env"
chmod 644 "$APP_ROOT/.env"
if run_entry; then
	bad "prefail-roles: must exit nonzero"
else
	echo "$RUN_OUT" | grep -q "chmod o+r config/roles.json" && ok "prefail-roles: actionable msg" || bad "prefail-roles: msg"
fi
rm -rf "$ROOT"

echo "pass=$pass fail=$fail"
[ "$fail" = "0" ]
