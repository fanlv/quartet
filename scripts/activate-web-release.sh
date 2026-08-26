#!/bin/bash
# Promote an already-built, startup-checked Web release and restart the backend.
# If the new process does not bind the expected port, restore the previous
# binary/static assets and start the previous release again.
set -u

REPO="${1:?usage: $0 <repo_root> <stage_dir> <port> <sudo> <proto>}"
STAGE="${2:?usage: $0 <repo_root> <stage_dir> <port> <sudo> <proto>}"
PORT="${3:?usage: $0 <repo_root> <stage_dir> <port> <sudo> <proto>}"
SUDO="${4:-}"
PROTO="${5:-http}"

BACKEND_LOG=/tmp/quartet-backend.log
RESTART_LOG=/tmp/quartet-web-restart.log
ACTIVE_BINARY="$REPO/bin/quartet-web"
ACTIVE_STATIC="$REPO/static"
NEW_BINARY="$STAGE/quartet-web"
NEW_STATIC="$STAGE/static"
PREVIOUS_BINARY="$STAGE/previous-quartet-web"
RELEASE_ID="$$"
BACKUP_BINARY="$STAGE/active-path-quartet-web.$RELEASE_ID"
BACKUP_STATIC="$STAGE/previous-static.$RELEASE_ID"
RUN_PREFIX="$SUDO"
[ -n "$RUN_PREFIX" ] && RUN_PREFIX="$SUDO -E"

exec >> "$RESTART_LOG" 2>&1
echo "==== activate validated release: pid=$$ ppid=$PPID time=$(date '+%F %T') stage=$STAGE port=$PORT ===="

fail() {
    echo "FATAL: $*"
    rm -rf -- "$STAGE"
    exit 1
}

port_pid() {
    $SUDO lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | head -n1
}

start_binary() {
    local label="$1"
    printf '\n----- %s at %s -----\n' "$label" "$(date '+%F %T')" >> "$BACKEND_LOG"
    # shellcheck disable=SC2086
    nohup $RUN_PREFIX "$ACTIVE_BINARY" >> "$BACKEND_LOG" 2>&1 < /dev/null &
    echo $!
}

wait_ready() {
    local expected_pid="${1:?expected backend pid is required}" current=""
    for _i in $(seq 1 120); do
        current=$(port_pid)
        if [ "$current" = "$expected_pid" ]; then
            printf '%s' "$current"
            return 0
        fi
        $SUDO kill -0 "$expected_pid" 2>/dev/null || return 1
        sleep 0.5
    done
    return 1
}

kill_one() {
    local pid="$1"
    [ -n "$pid" ] || return 0
    $SUDO kill "$pid" 2>/dev/null || true
    for _i in 1 2 3 4 5; do
        $SUDO kill -0 "$pid" 2>/dev/null || return 0
        sleep 1
    done
    $SUDO kill -9 "$pid" 2>/dev/null || true
}

case "$STAGE" in
    "$REPO"/bin/.web-release-*) ;;
    *) echo "FATAL: refusing unsafe Web release stage path: $STAGE"; exit 1 ;;
esac
[ -x "$NEW_BINARY" ] || fail "candidate binary is missing or not executable: $NEW_BINARY"
[ -x "$PREVIOUS_BINARY" ] || fail "rollback binary is missing or not executable: $PREVIOUS_BINARY"
[ -f "$NEW_STATIC/index.html" ] || fail "candidate static build is missing index.html: $NEW_STATIC"
cd "$REPO" || fail "cannot enter repository root: $REPO"

old_pid=$(port_pid)
had_binary=0
had_static=0
[ -e "$ACTIVE_BINARY" ] && had_binary=1
[ -e "$ACTIVE_STATIC" ] && had_static=1

if [ "$had_binary" = "1" ]; then
    mv "$ACTIVE_BINARY" "$BACKUP_BINARY" || fail "cannot back up active binary"
fi
if ! mv "$NEW_BINARY" "$ACTIVE_BINARY"; then
    [ "$had_binary" = "1" ] && mv "$BACKUP_BINARY" "$ACTIVE_BINARY"
    fail "cannot promote candidate binary"
fi
if [ "$had_static" = "1" ]; then
    if ! mv "$ACTIVE_STATIC" "$BACKUP_STATIC"; then
        mv "$ACTIVE_BINARY" "$NEW_BINARY" 2>/dev/null || true
        [ "$had_binary" = "1" ] && mv "$BACKUP_BINARY" "$ACTIVE_BINARY"
        fail "cannot back up active static directory"
    fi
fi
if ! mv "$NEW_STATIC" "$ACTIVE_STATIC"; then
    [ "$had_static" = "1" ] && mv "$BACKUP_STATIC" "$ACTIVE_STATIC"
    mv "$ACTIVE_BINARY" "$NEW_BINARY" 2>/dev/null || true
    [ "$had_binary" = "1" ] && mv "$BACKUP_BINARY" "$ACTIVE_BINARY"
    fail "cannot promote candidate static directory"
fi

echo "release promoted; stopping old backend pid=${old_pid:-none}"
kill_one "$old_pid"
for _i in 1 2 3 4 5 6 7 8 9 10; do
    [ -z "$(port_pid)" ] && break
    sleep 1
done

new_pid=$(start_binary "activate candidate Web release")
if ready_pid=$(wait_ready "$new_pid"); then
    echo "SUCCESS: candidate Web release ready on ${PROTO}://0.0.0.0:$PORT pid=$ready_pid"
    if [ "$had_binary" = "1" ]; then rm -f "$BACKUP_BINARY"; fi
    if [ "$had_static" = "1" ]; then rm -rf -- "$BACKUP_STATIC"; fi
    rm -rf -- "$STAGE"
    exit 0
fi

echo "ERROR: candidate pid=$new_pid did not become ready on :$PORT; rolling back"
kill_one "$new_pid"
rm -f "$ACTIVE_BINARY"
rm -rf -- "$ACTIVE_STATIC"
if ! mv "$PREVIOUS_BINARY" "$ACTIVE_BINARY"; then
    [ "$had_binary" = "1" ] && mv "$BACKUP_BINARY" "$ACTIVE_BINARY"
    fail "cannot restore the previously running Web binary"
fi
if [ "$had_binary" = "1" ]; then rm -f "$BACKUP_BINARY"; fi
[ "$had_static" = "1" ] && mv "$BACKUP_STATIC" "$ACTIVE_STATIC"

rollback_pid=$(start_binary "rollback previous Web release")
if ready_pid=$(wait_ready "$rollback_pid"); then
    echo "ROLLBACK SUCCESS: previous Web release restored on :$PORT pid=$ready_pid (spawned=$rollback_pid)"
    rm -rf -- "$STAGE"
    exit 1
fi

echo "ROLLBACK FAILED: previous Web release pid=$rollback_pid did not become ready on :$PORT"
echo "Last 80 backend log lines:"
tail -n 80 "$BACKEND_LOG" 2>/dev/null || true
exit 1
