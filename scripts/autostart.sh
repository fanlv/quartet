#!/bin/bash
# Auto-start (login) + auto-revive supervisor for the quartet web service on
# macOS, built on top of scripts/watchdog.sh.
#
# A launchd LaunchAgent runs this script in `serve` mode in the foreground:
#   - RunAtLoad starts the watchdog at login; the watchdog then starts the
#     backend whenever the port is free (never kills a live process).
#   - KeepAlive.SuccessfulExit=false revives the watchdog if it crashes or is
#     SIGKILLed. A manual `make web-stop` / `make web-watch-stop` terminates it
#     with SIGTERM, the watchdog trap exits 0, and launchd leaves it stopped.
#
# Subcommands (invoked by the Makefile autostart-* targets, $1 = repo root):
#   install    validate env, generate the plist, load the agent (idempotent)
#   uninstall  bootout the agent and remove the plist (backend keeps running)
#   status     show launchd job, watchdog, and backend port status
#   serve      launchd entry point: ensure build artifacts, exec watchdog.sh
#
# Env used by `serve`:
#   LOCAL_MEMORY               required by the backend (baked into the plist)
#   QUARTET_AUTOSTART_LABEL    override the launchd label (default below)

set -u

LABEL="${QUARTET_AUTOSTART_LABEL:-com.fanlv.quartet}"
PLIST="$HOME/Library/LaunchAgents/${LABEL}.plist"
LOG=/tmp/quartet-autostart.log

log() { echo "[$(date '+%F %T')] $*"; }
die() { echo "❌ $*"; exit 1; }

repo_arg() { [ -n "${1:-}" ] || die "usage: $0 <install|uninstall|status|serve> <repo_root>"; echo "$1"; }

# Backend port, derived with the same rule as the Makefile: TLS certs present
# -> HTTPS :443, otherwise plaintext HTTP :8090.
derive_port() {
    if [ -f "$1/certs/cert.pem" ] && [ -f "$1/certs/key.pem" ]; then
        echo 443
    else
        echo 8090
    fi
}

# Explicit PATH for the launchd environment (launchd's default PATH cannot
# resolve go / node / make).
launchd_path() {
    local p="/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
    local d
    for d in "$HOME/.local/bin" "$HOME/go/bin" "$HOME/.g/go/bin"; do
        [ -d "$d" ] && p="$p:$d"
    done
    printf '%s' "$p"
}

xml_escape() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'; }

cmd_install() {
    local repo; repo=$(repo_arg "${1:-}")
    command -v launchctl >/dev/null 2>&1 || die "launchctl not found; autostart requires macOS"
    [ -n "${LOCAL_MEMORY:-}" ] || die "LOCAL_MEMORY is not set (same requirement as 'make web')"
    [ -f "$repo/scripts/watchdog.sh" ] || die "watchdog.sh not found under $repo/scripts"

    # Single-supervisor rule: stop a manually started watchdog so the
    # launchd-managed one is the only instance. This never touches the backend.
    if [ -f /tmp/quartet-watchdog.pid ] && kill -0 "$(cat /tmp/quartet-watchdog.pid 2>/dev/null)" 2>/dev/null; then
        log "stopping existing manual watchdog before install"
        make -C "$repo" --no-print-directory web-watch-stop || true
    fi

    local port lpath
    port=$(derive_port "$repo")
    lpath=$(launchd_path)

    mkdir -p "$HOME/Library/LaunchAgents"
    cat > "$PLIST" <<PLIST_EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$(printf '%s' "$LABEL" | xml_escape)</string>
    <key>ProgramArguments</key>
    <array>
        <string>/bin/bash</string>
        <string>$(printf '%s' "$repo/scripts/autostart.sh" | xml_escape)</string>
        <string>serve</string>
        <string>$(printf '%s' "$repo" | xml_escape)</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>
    <key>WorkingDirectory</key>
    <string>$(printf '%s' "$repo" | xml_escape)</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>LOCAL_MEMORY</key>
        <string>$(printf '%s' "$LOCAL_MEMORY" | xml_escape)</string>
        <key>PATH</key>
        <string>$(printf '%s' "$lpath" | xml_escape)</string>
    </dict>
    <key>StandardOutPath</key>
    <string>$LOG</string>
    <key>StandardErrorPath</key>
    <string>$LOG</string>
</dict>
</plist>
PLIST_EOF

    launchctl bootout "gui/$UID/$LABEL" >/dev/null 2>&1 || true
    launchctl bootstrap "gui/$UID" "$PLIST" || die "launchctl bootstrap failed; check $PLIST"

    sleep 1
    if [ -f /tmp/quartet-watchdog.pid ] && kill -0 "$(cat /tmp/quartet-watchdog.pid 2>/dev/null)" 2>/dev/null; then
        echo "✅ LaunchAgent installed and running (label: $LABEL, plist: $PLIST)"
    else
        echo "⚠️  LaunchAgent loaded but the watchdog has not reported a pid yet; check /tmp/quartet-watchdog.log and $LOG"
    fi
    echo "   Backend port: $port;  logs: /tmp/quartet-watchdog.log /tmp/quartet-backend.log $LOG"
    echo "   Stop service: make web-stop (stays stopped until next login);  disable autostart: make autostart-uninstall"
}

cmd_uninstall() {
    launchctl bootout "gui/$UID/$LABEL" >/dev/null 2>&1 || true
    rm -f "$PLIST"
    echo "✅ LaunchAgent removed ($LABEL). The backend keeps running; stop it with 'make backend-stop' if needed."
}

cmd_status() {
    echo "📊 Autostart status (label: $LABEL):"
    if launchctl print "gui/$UID/$LABEL" 2>/dev/null | grep -E 'state|pid' | sed 's/^/  launchd: /'; then
        :
    else
        echo "  launchd: ❌ not loaded (install with 'make autostart-install')"
    fi
    if [ -f /tmp/quartet-watchdog.pid ] && kill -0 "$(cat /tmp/quartet-watchdog.pid 2>/dev/null)" 2>/dev/null; then
        echo "  Watchdog: ✅ running (pid: $(cat /tmp/quartet-watchdog.pid))"
    else
        echo "  Watchdog: ❌ not running"
    fi
    local port
    port=$(derive_port "$(repo_arg "${1:-}")")
    if lsof -tiTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
        echo "  Backend:  ✅ listening on :$port"
    else
        echo "  Backend:  ❌ not listening on :$port"
    fi
}

cmd_serve() {
    local repo; repo=$(repo_arg "${1:-}")
    [ -n "${LOCAL_MEMORY:-}" ] || die "LOCAL_MEMORY is not set in the launchd environment"
    cd "$repo" || die "cd $repo failed"

    # Fill in missing build artifacts only; never rebuild over a running
    # service, and never restart anything here — the watchdog owns that.
    if [ ! -x "$repo/bin/quartet-web" ]; then
        log "bin/quartet-web missing; building..."
        make build-web || log "WARN backend build failed; watchdog will report the missing binary"
    fi
    if [ ! -f "$repo/static/index.html" ]; then
        log "static/index.html missing; building frontend..."
        make build-frontend || log "WARN frontend build failed; backend will serve without a UI"
    fi

    exec bash "$repo/scripts/watchdog.sh" "$repo" "$(derive_port "$repo")"
}

case "${1:-}" in
    install)   shift; cmd_install "$@" ;;
    uninstall) cmd_uninstall ;;
    status)    shift; cmd_status "$@" ;;
    serve)     shift; cmd_serve "$@" ;;
    *) die "usage: $0 <install|uninstall|status|serve> <repo_root>" ;;
esac
