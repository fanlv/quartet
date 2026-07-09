#!/bin/bash
# Watchdog for the quartet-web backend (single-process web service).
#
# Periodically checks whether the backend port is being LISTENed on. When the
# port has been DOWN for several CONSECUTIVE checks, the watchdog brings the
# backend back up. In the single-process model the backend also serves the
# front-end static build and terminates HTTPS, so there is no separate
# front-end process to guard.
#
# Two invariants make this safe to run underneath the very backend it guards:
#
#   1. It only ever *starts* the backend when the port is empty. It never sends
#      a signal to a live process — so it can never kill the backend (nor the
#      ACP agent / shell running underneath it) the way a restart-by-kill would.
#
#   2. It requires N consecutive down-checks before acting (default 3), so the
#      few-second port gap of a normal `make web` restart does not trip a false
#      restart and race the legitimate one. A last-moment recheck right before
#      spawning, plus the backend's own pre-bind port probe, prevent a second
#      instance from ever winning the port.
#
# It MUST be launched detached (setsid + nohup, reparented to init) so it lives
# outside the backend's process tree and survives the backend dying. The backend
# it spawns is itself setsid-detached, so stopping the watchdog never stops the
# backend, and the watchdog dying never orphans it into its own group.
#
# Args:
#   $1 = repo_root
#   $2 = backend_port          (443 when certs are present, else 8090)
#
# Env:
#   LOCAL_MEMORY               required by the backend (inherited by the spawn)
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
PIDFILE=/tmp/quartet-watchdog.pid

exec >> "$LOG" 2>&1

log() { echo "[$(date '+%F %T')] $*"; }

# Binding a privileged port (:443) as non-root needs sudo, both to see the
# root-owned listener and to (re)launch it. Loopback :8090 never needs sudo.
SUDO=""
if [ "$BACKEND_PORT" -lt 1024 ] && [ "$(id -u)" != "0" ]; then
    SUDO="sudo"
fi
# Preserve LOCAL_MEMORY etc. when relaunching under sudo.
RUN_PREFIX="$SUDO"
[ -n "$RUN_PREFIX" ] && RUN_PREFIX="$SUDO -E"

port_up() {
    # shellcheck disable=SC2086
    $SUDO lsof -tiTCP:"$BACKEND_PORT" -sTCP:LISTEN >/dev/null 2>&1
}

start_backend() {
    if [ -n "$DRYRUN" ]; then log "[dry-run] would start backend"; return 0; fi
    if [ ! -x "$REPO/bin/quartet-web" ]; then
        log "WARN cannot start backend: $REPO/bin/quartet-web missing — run 'make web' or 'make build-web'"
        return 1
    fi
    (
        cd "$REPO" || exit 1
        printf '\n----- watchdog backend start at %s -----\n' "$(date '+%F %T')" >> "$BACKEND_LOG"
        # shellcheck disable=SC2086
        if command -v setsid >/dev/null 2>&1; then
            setsid $RUN_PREFIX ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 < /dev/null &
        else
            nohup $RUN_PREFIX ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 < /dev/null &
        fi
    )
    log "backend start issued"
}

cleanup() {
    log "watchdog stopping (backend left running)"
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
log "watchdog started: pid=$$ repo=$REPO backend=:$BACKEND_PORT sudo=${SUDO:-none} interval=${INTERVAL}s threshold=$FAIL_THRESHOLD dryrun=${DRYRUN:+1}"

backend_down=0

while true; do
    if port_up; then
        if [ "$backend_down" -ne 0 ]; then log "backend back UP on :$BACKEND_PORT"; fi
        backend_down=0
    else
        backend_down=$((backend_down + 1))
        log "backend DOWN on :$BACKEND_PORT (count=$backend_down/$FAIL_THRESHOLD)"
        if [ "$backend_down" -ge "$FAIL_THRESHOLD" ]; then
            # Last-moment recheck: a normal restart may have just bound the port.
            sleep 1
            if port_up; then
                log "backend reappeared during recheck; skip restart"
            else
                log "backend restarting"
                start_backend
            fi
            backend_down=0
            # Grace period so a slow-starting backend is not re-triggered while
            # it is still initializing and not yet listening.
            sleep 5
        fi
    fi
    _isleep "$INTERVAL"
done
