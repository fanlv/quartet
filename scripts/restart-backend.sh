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
# init and detached from the old backend's process tree before it runs.
#
# It kills ONLY the single old backend process that holds the port — never the
# whole process tree. The backend's descendants (agent/job shells, ACP
# subprocesses, and any in-program caller that triggered this restart) must
# survive; tree-killing here would SIGTERM the very agent/shell that asked for
# the restart. Orphaned descendants are reparented to init and reclaimed by the
# new instance's startup cleanup (CleanupOrphanedConns + ACP orphan scan).

set -u

OLD_PID="${1:?usage: $0 <old_pid> <port> <repo_root>}"
PORT="${2:?usage: $0 <old_pid> <port> <repo_root>}"
REPO="${3:?usage: $0 <old_pid> <port> <repo_root>}"

LOG=/tmp/quartet-restart.log
BACKEND_LOG=/tmp/quartet-backend.log

exec >> "$LOG" 2>&1
echo "==== restart start: pid=$$ ppid=$PPID time=$(date '+%F %T') old_pid=$OLD_PID port=$PORT repo=$REPO ===="

_self=$$

# Kill only the port-holding backend process; preserve its process tree.
_kill_backend() {
    local _pid="$1"
    echo "kill_backend: target=$_pid self=$_self (single pid, tree preserved)"
    kill "$_pid" 2>/dev/null || true
    for _i in 1 2 3 4 5; do
        kill -0 "$_pid" 2>/dev/null || return 0
        sleep 1
    done
    if kill -0 "$_pid" 2>/dev/null; then
        echo "kill_backend: pid $_pid still alive after SIGTERM; SIGKILL"
        kill -9 "$_pid" 2>/dev/null || true
    fi
}

_kill_backend "$OLD_PID"

for _i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 1
    lsof -tiTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1 || break
done

if lsof -tiTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "WARN: port $PORT still bound after kill_backend; SIGKILL old_pid=$OLD_PID"
    kill -9 "$OLD_PID" 2>/dev/null || true
    sleep 1
fi

cd "$REPO" || { echo "FATAL: cd $REPO failed"; exit 1; }

printf "\n----- restart at %s -----\n" "$(date '+%F %T')" >> "$BACKEND_LOG"
nohup ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 &
_new_pid=$!

echo "==== restart done: new_backend_pid=$_new_pid ===="
