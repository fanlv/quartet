#!/bin/bash
# Watchdog for the quartet-web backend + vite frontend.
#
# Periodically checks whether the backend port and frontend port are being
# LISTENed on. When a port has been DOWN for several CONSECUTIVE checks, the
# watchdog brings that service back up.
#
# Two invariants make this safe to run underneath the very backend it guards:
#
#   1. It only ever *starts* a service when the port is empty. It never sends a
#      signal to a live process — so it can never kill the backend (nor the ACP
#      agent / shell running underneath it) the way a restart-by-kill would.
#
#   2. It requires N consecutive down-checks before acting (default 3), so the
#      few-second port gap of a normal `make web` restart does not trip a false
#      restart and race the legitimate one. A last-moment recheck right before
#      spawning, plus the backend's own pre-bind port probe, prevent a second
#      instance from ever winning the port.
#
# It MUST be launched detached (setsid + nohup, reparented to init) so it lives
# outside the backend's process tree and survives the backend dying. Services it
# spawns are themselves setsid-detached, so stopping the watchdog never stops
# the services, and the watchdog dying never orphans them into its own group.
#
# Args:
#   $1 = repo_root
#   $2 = backend_port
#
# Env:
#   LOCAL_MEMORY               required by the backend (inherited by spawned backend)
#   QUARTET_WD_INTERVAL        seconds between checks (default 10)
#   QUARTET_WD_FAIL_THRESHOLD  consecutive down-checks before restart (default 3)
#   QUARTET_WD_DRYRUN          if set, log "would start" instead of spawning

set -u

REPO="${1:?usage: $0 <repo_root> <backend_port>}"
BACKEND_PORT="${2:?usage: $0 <repo_root> <backend_port>}"

INTERVAL="${QUARTET_WD_INTERVAL:-10}"
FAIL_THRESHOLD="${QUARTET_WD_FAIL_THRESHOLD:-3}"
DRYRUN="${QUARTET_WD_DRYRUN:-}"

LOG=/tmp/quartet-watchdog.log
BACKEND_LOG=/tmp/quartet-backend.log
FRONTEND_LOG=/tmp/quartet-vite.log
PIDFILE=/tmp/quartet-watchdog.pid

CERTS_DIR="$REPO/certs"

exec >> "$LOG" 2>&1

log() { echo "[$(date '+%F %T')] $*"; }

# Frontend port + sudo mirror `make web`: HTTPS/443 when certs exist, else 5173.
# The backend port (8090, loopback) never needs sudo.
if [ -f "$CERTS_DIR/cert.pem" ] && [ -f "$CERTS_DIR/key.pem" ]; then
    FRONTEND_PORT=443
    if [ "$(id -u)" != "0" ]; then SUDO="sudo"; else SUDO=""; fi
else
    FRONTEND_PORT=5173
    SUDO=""
fi

# port_up <port> <sudo-prefix>
port_up() {
    # shellcheck disable=SC2086
    $2 lsof -tiTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}

start_backend() {
    if [ -n "$DRYRUN" ]; then log "[dry-run] would start backend"; return 0; fi
    if [ ! -x "$REPO/bin/quartet-web" ]; then
        log "WARN cannot start backend: $REPO/bin/quartet-web missing — run 'make build-web'"
        return 1
    fi
    (
        cd "$REPO" || exit 1
        printf '\n----- watchdog backend start at %s -----\n' "$(date '+%F %T')" >> "$BACKEND_LOG"
        if command -v setsid >/dev/null 2>&1; then
            setsid ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 < /dev/null &
        else
            nohup ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 < /dev/null &
        fi
    )
    log "backend start issued"
}

start_frontend() {
    if [ -n "$DRYRUN" ]; then log "[dry-run] would start frontend"; return 0; fi
    if [ ! -d "$REPO/web/node_modules" ]; then
        log "WARN cannot start frontend: $REPO/web/node_modules missing — run 'make web' once"
        return 1
    fi
    (
        cd "$REPO/web" || exit 1
        printf '\n----- watchdog vite start at %s -----\n' "$(date '+%F %T')" >> "$FRONTEND_LOG"
        # shellcheck disable=SC2086
        if command -v setsid >/dev/null 2>&1; then
            setsid $SUDO npm run dev >> "$FRONTEND_LOG" 2>&1 < /dev/null &
        else
            nohup $SUDO npm run dev >> "$FRONTEND_LOG" 2>&1 < /dev/null &
        fi
    )
    log "frontend start issued"
}

cleanup() {
    log "watchdog stopping (services left running)"
    [ -n "${_sleep_pid:-}" ] && kill "$_sleep_pid" 2>/dev/null
    rm -f "$PIDFILE"
    exit 0
}
trap cleanup TERM INT HUP

# Interruptible sleep: run sleep in the background and `wait` on it. `wait` is a
# shell builtin, so a SIGTERM during it fires the trap immediately instead of
# being deferred until an external `sleep` returns — the watchdog stops at once
# rather than after a full interval.
_isleep() {
    sleep "$1" &
    _sleep_pid=$!
    wait "$_sleep_pid" 2>/dev/null
    _sleep_pid=""
}

echo "$$" > "$PIDFILE"
log "watchdog started: pid=$$ repo=$REPO backend=:$BACKEND_PORT frontend=:$FRONTEND_PORT interval=${INTERVAL}s threshold=$FAIL_THRESHOLD dryrun=${DRYRUN:+1}"

backend_down=0
frontend_down=0

# guard_service <name> <port> <sudo> <down-counter-var> <start-fn>
guard_service() {
    _name="$1"; _port="$2"; _sudo="$3"; _counter_var="$4"; _start_fn="$5"
    eval "_count=\$$_counter_var"
    if port_up "$_port" "$_sudo"; then
        if [ "$_count" -ne 0 ]; then log "$_name back UP on :$_port"; fi
        eval "$_counter_var=0"
        return
    fi
    _count=$((_count + 1))
    eval "$_counter_var=$_count"
    log "$_name DOWN on :$_port (count=$_count/$FAIL_THRESHOLD)"
    if [ "$_count" -ge "$FAIL_THRESHOLD" ]; then
        # Last-moment recheck: a normal restart may have just bound the port.
        sleep 1
        if port_up "$_port" "$_sudo"; then
            log "$_name reappeared during recheck; skip restart"
        else
            log "$_name restarting"
            "$_start_fn"
        fi
        eval "$_counter_var=0"
        # Grace period so a slow-starting service is not re-triggered while
        # it is still initializing and not yet listening.
        sleep 5
    fi
}

while true; do
    guard_service backend  "$BACKEND_PORT"  ""      backend_down  start_backend
    guard_service frontend "$FRONTEND_PORT" "$SUDO" frontend_down start_frontend
    _isleep "$INTERVAL"
done
