#!/bin/bash
# Restart quartet-web backend in a fully detached process.
# Called by `make web` when an existing backend is running.
#
# Args:
#   $1 = old_backend_pid (the quartet-web pid currently bound to the port)
#   $2 = backend_port
#   $3 = repo_root
#
# This script must be invoked via double-fork + setsid so it is reparented to
# init before kill_tree walks the old backend's process tree — otherwise it
# would be a descendant (via the make recipe shell) and SIGTERM itself.

set -u

OLD_PID="${1:?usage: $0 <old_pid> <port> <repo_root>}"
PORT="${2:?usage: $0 <old_pid> <port> <repo_root>}"
REPO="${3:?usage: $0 <old_pid> <port> <repo_root>}"

LOG=/tmp/quartet-restart.log
BACKEND_LOG=/tmp/quartet-backend.log

exec >> "$LOG" 2>&1
echo "==== restart start: pid=$$ ppid=$PPID time=$(date '+%F %T') old_pid=$OLD_PID port=$PORT repo=$REPO ===="

_self=$$

_kill_tree() {
    local _root="$1"
    local _all="$_root"
    local _q="$_root"
    local _next _ch _c
    while [ -n "$_q" ]; do
        _next=""
        for _c in $_q; do
            _ch=$(pgrep -P "$_c" 2>/dev/null || true)
            if [ -n "$_ch" ]; then
                _all="$_all $_ch"
                _next="$_next $_ch"
            fi
        done
        _q="$_next"
    done

    local _filtered=""
    for _c in $_all; do
        [ "$_c" = "$_self" ] && continue
        _filtered="$_filtered $_c"
    done

    echo "kill_tree: target=$_root collected=[$_all] filtered=[$_filtered] self=$_self"
    [ -z "$_filtered" ] && return 0
    kill $_filtered 2>/dev/null || true
    sleep 2
    for _c in $_filtered; do
        if kill -0 "$_c" 2>/dev/null; then
            kill -9 "$_c" 2>/dev/null || true
        fi
    done
}

_kill_tree "$OLD_PID"

for _i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 1
    lsof -tiTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1 || break
done

if lsof -tiTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "WARN: port $PORT still bound after kill_tree; SIGKILL old_pid=$OLD_PID"
    kill -9 "$OLD_PID" 2>/dev/null || true
    sleep 1
fi

cd "$REPO" || { echo "FATAL: cd $REPO failed"; exit 1; }

printf "\n----- restart at %s -----\n" "$(date '+%F %T')" >> "$BACKEND_LOG"
nohup ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 &
_new_pid=$!

echo "==== restart done: new_backend_pid=$_new_pid ===="
