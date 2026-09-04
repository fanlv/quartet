#!/bin/bash
# Start (or restart) quartet-web as the single web service.
#
# In the single-process model the backend serves the pre-built front-end from
# static/ and, when TLS certs exist, terminates HTTPS itself on :443 — there is
# no separate vite front-end process any more. This script:
#   - stops a leftover vite dev server from the old two-process setup (so :443
#     is free for the backend),
#   - reaps orphan quartet-web instances not bound to the target port,
#   - starts the backend DETACHED (setsid, reparented to init) so it survives
#     the caller (`make web` from an agent shell), restarting in place when one
#     is already bound to the port,
#   - waits until it is accepting connections, and prints the access URL.
#
# Note: when TLS is active on :443 the backend ADDITIONALLY serves a
# loopback-only plaintext listener on 127.0.0.1:8090 (for quartet-cli and
# local scripts). That companion port is managed by the backend itself, not
# by this script.
#
# The backend is always launched with cwd = repo root because it resolves
# static/ and certs/ relative to the working directory.
#
# Args:
#   $1 = repo_root
#   $2 = backend_port          (443 when certs are present, else 8090)
#   $3 = sudo prefix           ("" or "sudo"; needed to bind/inspect :443 as non-root)
#   $4 = backend_proto         (https|http, only used for the printed URL)
set -u

REPO="${1:?usage: $0 <repo_root> <port> <sudo> <proto>}"
PORT="${2:?usage: $0 <repo_root> <port> <sudo> <proto>}"
SUDO="${3:-}"
PROTO="${4:-http}"

# TEMP: reproduce the agy "end_turn with unfinished tool call(s)" failure with
# verbose ACP logs (tool_call / tool_call_update ground truth). Revert to the
# Info default once the diagnosis is done.
export QUARTET_LOG_LEVEL=debug

BACKEND_LOG=/tmp/quartet-backend.log
RESTART_LOG=/tmp/quartet-restart.log
STOP_TREE="$REPO/scripts/stop-process-tree.sh"
RESTART_SH="$REPO/scripts/restart-backend.sh"

log() { echo "$*"; }

# PID currently LISTENing on the target port. Honors the sudo prefix so a
# root-owned :443 process is visible from a non-root shell.
_port_pid() { $SUDO lsof -tiTCP:"$PORT" -sTCP:LISTEN 2>/dev/null | head -n1; }

# Best-effort address that another device on the same network can use. Prefer
# the source address selected by the default route, then fall back to the first
# non-loopback/non-link-local IPv4 reported by the host.
_lan_ipv4() {
    local _ip="" _iface="" _candidate=""

    if command -v ip >/dev/null 2>&1; then
        _ip=$(ip -4 route get 1.1.1.1 2>/dev/null \
            | awk '{ for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }')
    elif command -v route >/dev/null 2>&1 && command -v ipconfig >/dev/null 2>&1; then
        _iface=$(route -n get default 2>/dev/null | awk '/interface:/ { print $2; exit }')
        [ -n "$_iface" ] && _ip=$(ipconfig getifaddr "$_iface" 2>/dev/null || true)
    fi

    if [ -z "$_ip" ] && command -v hostname >/dev/null 2>&1; then
        for _candidate in $(hostname -I 2>/dev/null); do
            case "$_candidate" in
                127.*|169.254.*|*:* ) continue ;;
                *.*.*.* ) _ip="$_candidate"; break ;;
            esac
        done
    fi

    printf '%s' "$_ip"
}

cd "$REPO" || { echo "FATAL: cd $REPO failed"; exit 1; }

# --- migration cleanup: stop any leftover vite dev server for this repo ------
# The old two-process setup ran vite on :443/:5173. In single-process mode the
# backend wants :443, so stop any vite bound to this repo's web dir first.
if [ -d /proc ]; then
    for _vp in $($SUDO pgrep -x node 2>/dev/null); do
        _cmd=$($SUDO tr '\0' ' ' < "/proc/$_vp/cmdline" 2>/dev/null || echo "")
        case "$_cmd" in *node_modules/.bin/vite*) ;; *) continue ;; esac
        _cwd=$($SUDO readlink "/proc/$_vp/cwd" 2>/dev/null || echo "")
        case "$_cwd" in
            "$REPO/web"|*"(deleted)")
                log "🧹 Stopping leftover vite dev server (pid $_vp) from the old two-process setup"
                $SUDO "$STOP_TREE" "$_vp" ;;
        esac
    done
fi

# --- detect the current backend + orphans ------------------------------------
backend_pid=$(_port_pid)
backend_orphans=""
for _p in $(pgrep -x quartet-web 2>/dev/null); do
    [ "$_p" = "$backend_pid" ] && continue
    backend_orphans="$backend_orphans $_p"
done
if [ -n "$backend_orphans" ]; then
    log "🧹 Killing orphan quartet-web processes (not bound to :$PORT):$backend_orphans"
    for _p in $backend_orphans; do $SUDO kill "$_p" 2>/dev/null || true; done
fi

# --- start or restart --------------------------------------------------------
# When sudo is in play (non-root binding :443) preserve the environment so the
# backend still sees LOCAL_MEMORY and friends.
run_prefix="$SUDO"
[ -n "$run_prefix" ] && run_prefix="$SUDO -E"

if [ -n "$backend_pid" ]; then
    log "🔄 Restarting backend (pid $backend_pid) on :$PORT via detached process..."
    : > "$RESTART_LOG"
    if command -v setsid >/dev/null 2>&1; then
        ( setsid "$RESTART_SH" "$backend_pid" "$PORT" "$REPO" "$SUDO" </dev/null >>"$RESTART_LOG" 2>&1 & )
    else
        ( "$RESTART_SH" "$backend_pid" "$PORT" "$REPO" "$SUDO" </dev/null >>"$RESTART_LOG" 2>&1 & )
    fi
    log "✅ Backend restart initiated (log: $RESTART_LOG)"
else
    log "🌐 Starting backend on :$PORT..."
    printf '\n----- start at %s -----\n' "$(date '+%F %T')" >> "$BACKEND_LOG"
    if command -v setsid >/dev/null 2>&1; then
        # shellcheck disable=SC2086
        ( setsid $run_prefix ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 </dev/null & )
    else
        # shellcheck disable=SC2086
        ( nohup $run_prefix ./bin/quartet-web >> "$BACKEND_LOG" 2>&1 </dev/null & )
    fi
fi

# --- wait for readiness ------------------------------------------------------
# Two-stage like `make web`'s old check: when restarting, first wait for the old
# pid to exit, then for a NEW pid to bind the port. Fresh start skips stage 1.
log "⏳ Waiting for backend on :$PORT (timeout 60s)..."
_start=$(date +%s)
_ok=0
_ready_pid=""
_old="$backend_pid"
_old_dead=0
[ -z "$_old" ] && _old_dead=1
for _i in $(seq 1 120); do
    if [ "$_old_dead" = "0" ]; then
        kill -0 "$_old" 2>/dev/null || _old_dead=1
    else
        _cur=$(_port_pid)
        if [ -n "$_cur" ] && [ "$_cur" != "$_old" ]; then
            _ok=1
            _ready_pid="$_cur"
            break
        fi
    fi
    sleep 0.5
done
_elapsed=$(( $(date +%s) - _start ))
if [ "$_ok" = "1" ]; then
    log "✅ Backend ready on :$PORT (pid $_ready_pid, ${_elapsed}s)"
else
    log "❌ Backend did not become ready on :$PORT within ${_elapsed}s"
    log "   Last 40 lines of $BACKEND_LOG:"
    tail -n 40 "$BACKEND_LOG" 2>/dev/null | sed 's/^/   /'
    log "   Full log: tail -f $BACKEND_LOG"
    exit 1
fi

# --- print access info -------------------------------------------------------
echo ""
echo "================================================"
if [ "$PORT" = "443" ]; then
    echo "  Web UI:  ${PROTO}://<your-domain>/     (backend bound on 0.0.0.0:443)"
    echo "           ${PROTO}://localhost/          (local access)"
else
    lan_ip=$(_lan_ipv4)
    echo "  Web UI:  ${PROTO}://localhost:${PORT}/       (local access)"
    if [ -n "$lan_ip" ]; then
        echo "           ${PROTO}://${lan_ip}:${PORT}/  (LAN access)"
    else
        echo "           ${PROTO}://<LAN-IP-unavailable>:${PORT}/  (LAN address not detected)"
    fi
fi
echo "================================================"
echo ""
echo "Stop:  make web-stop"
echo "Logs:  make web-logs      (tail -f $BACKEND_LOG)"
echo "Watch: make web-watch     (auto-revive if the port goes down)"
echo ""
