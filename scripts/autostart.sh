#!/bin/bash
# Auto-start supervisor for the quartet web service on macOS, built on top of
# scripts/watchdog.sh.
#
# A launchd job (LaunchAgent or LaunchDaemon, selected by scope) runs this
# script in `serve` mode in the foreground:
#   - RunAtLoad starts the watchdog (at login for the agent scope, at boot for
#     the daemon scope); the watchdog then starts the backend whenever the port
#     is free (never kills a live process).
#   - KeepAlive.SuccessfulExit=false revives the watchdog if it crashes or is
#     SIGKILLed. A manual `make web-stop` / `make web-watch-stop` terminates it
#     with SIGTERM, the watchdog trap exits 0, and launchd leaves it stopped.
#
# Scopes:
#   agent  LaunchAgent in ~/Library/LaunchAgents  — starts at login, no sudo
#   daemon LaunchDaemon in /Library/LaunchDaemons — starts at boot, needs sudo
#   The daemon plist sets UserName to the installing user so the backend keeps
#   writing LOCAL_MEMORY files as that user instead of root. Only one scope may
#   be active at a time: installing one removes the other.
#
# Usage ($1 = repo root, $2 = scope where relevant, default agent):
#   install <repo> [scope]    validate env, generate the plist, load the job
#   uninstall <repo> [scope]  bootout the job and remove the plist (backend
#                             keeps running)
#   status <repo> [scope]     show launchd job, watchdog, and backend port
#   serve <repo>              launchd entry point: ensure build artifacts,
#                             exec watchdog.sh
#
# Env used by `serve`:
#   LOCAL_MEMORY               required by the backend (baked into the plist)
#   QUARTET_AUTOSTART_LABEL    override the launchd label (default below)

set -u

LABEL="${QUARTET_AUTOSTART_LABEL:-com.fanlv.quartet}"
LOG=/tmp/quartet-autostart.log

log() { echo "[$(date '+%F %T')] $*"; }
die() { echo "❌ $*"; exit 1; }

usage() { die "usage: $0 <install|uninstall|status|serve> <repo_root> [agent|daemon]"; }

repo_arg() { [ -n "${1:-}" ] || usage; echo "$1"; }

# Agent vs daemon scope: plist location, launchd domain, and privileges.
scope_arg() {
    case "${1:-agent}" in
        agent|daemon) echo "$1" ;;
        *) usage ;;
    esac
}

scope_plist() {
    case "$1" in
        agent)  echo "$HOME/Library/LaunchAgents/${LABEL}.plist" ;;
        daemon) echo "/Library/LaunchDaemons/${LABEL}.plist" ;;
    esac
}

scope_domain() {
    case "$1" in
        agent)  echo "gui/$UID" ;;
        daemon) echo "system" ;;
    esac
}

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

# Generate the plist content on stdout for the given scope + repo.
plist_content() {
    local scope="$1" repo="$2" lpath
    lpath=$(launchd_path)
    cat <<PLIST_EOF
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
PLIST_EOF
    if [ "$scope" = "daemon" ]; then
        cat <<PLIST_EOF
    <key>UserName</key>
    <string>$(printf '%s' "$(id -un)" | xml_escape)</string>
PLIST_EOF
    fi
    echo "</dict>"
    echo "</plist>"
}

# Bootout a job in one scope; the system domain needs sudo (best effort).
scope_bootout() {
    case "$1" in
        agent)  launchctl bootout "gui/$UID/$LABEL" >/dev/null 2>&1 || true ;;
        daemon) sudo launchctl bootout "system/$LABEL" >/dev/null 2>&1 || true ;;
    esac
}

# Load the generated plist into the scope's domain; the system domain needs
# sudo (this is the point where the daemon scope asks for a password).
scope_bootstrap() {
    case "$1" in
        agent)  launchctl bootstrap "gui/$UID" "$2" ;;
        daemon) sudo launchctl bootstrap "system" "$2" ;;
    esac
}

# Remove any job/plist of the OTHER scope so only one supervisor stays active.
remove_other_scope() {
    local other="$1" other_plist
    other_plist=$(scope_plist "$other")
    if launchctl print "$(scope_domain "$other")/$LABEL" >/dev/null 2>&1; then
        log "removing the other autostart scope ($other) to keep a single supervisor"
        scope_bootout "$other"
    fi
    case "$other" in
        agent)  rm -f "$other_plist" ;;
        daemon) sudo rm -f "$other_plist" 2>/dev/null || true ;;
    esac
}

stop_manual_watchdog() {
    # Single-supervisor rule: stop a manually started watchdog so the
    # launchd-managed one is the only instance. This never touches the backend.
    if [ -f /tmp/quartet-watchdog.pid ] && kill -0 "$(cat /tmp/quartet-watchdog.pid 2>/dev/null)" 2>/dev/null; then
        log "stopping existing manual watchdog before install"
        make -C "$1" --no-print-directory web-watch-stop || true
    fi
}

cmd_install() {
    local repo scope plist
    repo=$(repo_arg "${1:-}")
    scope=$(scope_arg "${2:-agent}")
    plist=$(scope_plist "$scope")
    command -v launchctl >/dev/null 2>&1 || die "launchctl not found; autostart requires macOS"
    [ -n "${LOCAL_MEMORY:-}" ] || die "LOCAL_MEMORY is not set (same requirement as 'make web')"
    [ -f "$repo/scripts/watchdog.sh" ] || die "watchdog.sh not found under $repo/scripts"

    # Write the plist first: the daemon scope needs sudo here, and failing
    # early (bad password, no sudo) must leave the current autostart intact.
    if [ "$scope" = "daemon" ]; then
        local tmp
        tmp=$(mktemp /tmp/quartet-autostart.XXXXXX.plist)
        plist_content "$scope" "$repo" > "$tmp"
        # launchd refuses daemon plists not owned by root:wheel 0644.
        sudo install -m 0644 -o root -g wheel "$tmp" "$plist" || {
            rm -f "$tmp"; die "cannot write $plist (sudo required for the daemon scope)"; }
        rm -f "$tmp"
    else
        mkdir -p "$HOME/Library/LaunchAgents"
        plist_content "$scope" "$repo" > "$plist"
    fi

    stop_manual_watchdog "$repo"
    remove_other_scope "$([ "$scope" = agent ] && echo daemon || echo agent)"

    scope_bootout "$scope"
    scope_bootstrap "$scope" "$plist" \
        || die "launchctl bootstrap failed; check $plist"

    sleep 1
    if [ -f /tmp/quartet-watchdog.pid ] && kill -0 "$(cat /tmp/quartet-watchdog.pid 2>/dev/null)" 2>/dev/null; then
        echo "✅ Autostart installed and running (scope: $scope, label: $LABEL, plist: $plist)"
    else
        echo "⚠️  Job loaded but the watchdog has not reported a pid yet; check /tmp/quartet-watchdog.log and $LOG"
    fi
    echo "   Backend port: $(derive_port "$repo");  logs: /tmp/quartet-watchdog.log /tmp/quartet-backend.log $LOG"
    echo "   Stop service: make web-stop (stays stopped until the next login/boot);  disable autostart: make autostart-uninstall"
}

cmd_uninstall() {
    local scope plist
    scope=$(scope_arg "${2:-agent}")
    plist=$(scope_plist "$scope")
    scope_bootout "$scope"
    case "$scope" in
        agent)  rm -f "$plist" ;;
        daemon) sudo rm -f "$plist" 2>/dev/null || true ;;
    esac
    echo "✅ Autostart removed (scope: $scope, label: $LABEL). The backend keeps running; stop it with 'make backend-stop' if needed."
}

cmd_status() {
    local scope
    scope=$(scope_arg "${2:-agent}")
    echo "📊 Autostart status (scope: $scope, label: $LABEL):"
    if launchctl print "$(scope_domain "$scope")/$LABEL" 2>/dev/null | grep -E 'state|pid' | sed 's/^/  launchd: /'; then
        :
    else
        echo "  launchd: ❌ not loaded (install with 'make autostart-install')"
        if [ "$scope" = "daemon" ]; then
            echo "           (the system domain needs root: sudo launchctl print system/$LABEL)"
        fi
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
    uninstall) shift; cmd_uninstall "$@" ;;
    status)    shift; cmd_status "$@" ;;
    serve)     shift; cmd_serve "$@" ;;
    *) usage ;;
esac
