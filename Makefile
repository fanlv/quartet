.PHONY: help build build-all build-acp build-cli build-eino-cli build-web build-frontend build-ios test test-web test-ios e2e e2e-ios clean run run-cli run-web run-frontend run-backend web web-logs web-stop web-status backend-stop web-watch web-watch-stop web-watch-logs install-project-tools install-skill install-skill-cli install-skill-copy install-skill-run install-skill-all install-skill-list

CERTS_DIR := $(CURDIR)/certs
# Serving model, derived ONCE at parse time so every target below
# (start / stop / status / watchdog / restart) agrees on the same port and
# privilege:
#   certs present -> backend serves HTTPS on all interfaces at :443 (reachable
#                    by domain); binding 443 as non-root needs sudo.
#   no certs      -> plaintext HTTP on all interfaces at :8090.
# Known blind spot: an explicit QUARTET_LISTEN_ADDR at runtime is NOT reflected
# in these scripts (single-user local use; documented in the feature notes).
HAS_CERTS := $(shell [ -f "$(CERTS_DIR)/cert.pem" ] && [ -f "$(CERTS_DIR)/key.pem" ] && echo 1)
ifeq ($(HAS_CERTS),1)
BACKEND_PORT := 443
BACKEND_PROTO := https
ifneq ($(shell id -u),0)
SUDO := sudo
endif
else
BACKEND_PORT := 8090
BACKEND_PROTO := http
endif
WEB_BINARY := $(CURDIR)/bin/quartet-web
BACKEND_LOG := /tmp/quartet-backend.log
WATCHDOG_LOG := /tmp/quartet-watchdog.log
WATCHDOG_PID := /tmp/quartet-watchdog.pid
STOP_PROCESS_TREE := $(CURDIR)/scripts/stop-process-tree.sh
WATCHDOG := $(CURDIR)/scripts/watchdog.sh
FRONTEND_ENV_CHECK := $(CURDIR)/scripts/frontend-env-check.sh
FRONTEND_DEPS := $(CURDIR)/scripts/frontend-deps.sh
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GIT_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
GIT_DIRTY ?= $(shell if [ -n "$$(git status --porcelain 2>/dev/null)" ]; then echo true; else echo false; fi)
WEB_LDFLAGS := -X main.buildTime=$(BUILD_TIME) -X main.buildCommit=$(GIT_COMMIT) -X main.buildDirty=$(GIT_DIRTY)
IOS_TEST_DESTINATION ?= platform=iOS Simulator,name=iPhone 17 Pro,OS=latest
IOS_DERIVED_DATA ?= /tmp/quartet-ios-derived

# Skill install (see install-skill target). The quartet-workflow skill drives
# the quartet-cli binary, which its SKILL.md (requires.bins) expects on PATH, so
# installing the skill is two steps: build+install the CLI into INSTALL_BIN_DIR,
# then register the skill directory with the `skills` CLI for each agent.
QUARTET_CLI_BIN := $(CURDIR)/bin/quartet-cli
INSTALL_BIN_DIR ?= $(HOME)/.local/bin
SKILL_NAME ?= quartet-workflow
SKILL_SOURCE ?= $(CURDIR)/skill
SKILLS_CLI ?= npx --yes skills
SKILL_AGENTS ?= claude-code codex opencode trae trae-cn
PROJECT_SKILL_NAMES := quartet-workflow quartet-schedule quartet-wechat
SKILL_AGENT_FLAGS = $(foreach agent,$(SKILL_AGENTS),--agent "$(agent)")
SKILL_COPY_FLAG ?=

help:
	@printf 'Usage: make <target>\n\n'
	@printf 'Build targets:\n'
	@printf '  %-24s %s\n' 'build' 'Build acp, cli, and web binaries into bin/'
	@printf '  %-24s %s\n' 'build-all' 'Run go build ./...'
	@printf '  %-24s %s\n' 'build-acp' 'Build bin/quartet-acp'
	@printf '  %-24s %s\n' 'build-cli' 'Build bin/quartet-cli'
	@printf '  %-24s %s\n' 'build-eino-cli' 'Build eino-cli and install it to INSTALL_BIN_DIR'
	@printf '  %-24s %s\n' 'build-web' 'Build bin/quartet-web'
	@printf '  %-24s %s\n' 'build-frontend' 'Build frontend SPA into static/'
	@printf '  %-24s %s\n\n' 'build-ios' 'Build the iOS app (requires macOS + Xcode)'
	@printf 'Test targets:\n'
	@printf '  %-24s %s\n' 'test' 'Run Go build, frontend tests, and Playwright E2E'
	@printf '  %-24s %s\n' 'test-web' 'Run frontend component tests'
	@printf '  %-24s %s\n' 'test-ios' 'Build the iOS app for Simulator without signing'
	@printf '  %-24s %s\n' 'e2e-ios' 'Run native iOS UI tests in Simulator'
	@printf '  %-24s %s\n\n' 'e2e' 'Run frontend Playwright E2E tests'
	@printf 'Run/service targets:\n'
	@printf '  %-24s %s\n' 'run' 'Alias for web'
	@printf '  %-24s %s\n' 'run-cli' 'Run quartet-cli with go run'
	@printf '  %-24s %s\n' 'run-backend' 'Run web backend with go run'
	@printf '  %-24s %s\n' 'run-frontend' 'Run Vite dev server'
	@printf '  %-24s %s\n' 'web' 'Build frontend/backend and start or restart web service'
	@printf '  %-24s %s\n' 'web-status' 'Show backend and watchdog status'
	@printf '  %-24s %s\n' 'web-logs' 'Follow backend log'
	@printf '  %-24s %s\n' 'web-stop' 'Stop backend web service and orphan quartet-web processes'
	@printf '  %-24s %s\n' 'backend-stop' 'Stop backend only; watchdog untouched'
	@printf '  %-24s %s\n' 'web-watch' 'Start detached backend watchdog'
	@printf '  %-24s %s\n' 'web-watch-stop' 'Stop backend watchdog'
	@printf '  %-24s %s\n\n' 'web-watch-logs' 'Follow watchdog log'
	@printf 'Install targets:\n'
	@printf '  %-24s %s\n' 'install-project-tools' 'Install quartet-cli and every skill shipped by this project'
	@printf '  %-24s %s\n' 'install-skill' 'Build/install quartet-cli and register the skill'
	@printf '  %-24s %s\n' 'install-skill-copy' 'Install skill files by copying instead of symlinking'
	@printf '  %-24s %s\n' 'install-skill-cli' 'Build and install quartet-cli to INSTALL_BIN_DIR'
	@printf '  %-24s %s\n' 'install-skill-run' 'Register the skill directory with the skills CLI'
	@printf '  %-24s %s\n' 'install-skill-all' 'Install the skill for every known skills CLI agent'
	@printf '  %-24s %s\n\n' 'install-skill-list' 'List skills under SKILL_SOURCE without installing'
	@printf 'Cleanup targets:\n'
	@printf '  %-24s %s\n' 'clean' 'Remove bin/'

build-all:
	@echo "Building all applications..."
	go build ./...
	@echo "All applications built successfully!"

test: build-all test-web e2e

test-web:
	@echo "Running web component tests..."
	@bash "$(FRONTEND_ENV_CHECK)" "$(CURDIR)/web"
	@bash "$(FRONTEND_DEPS)" "$(CURDIR)/web"
	@cd web && npm test

e2e:
	@echo "Running web E2E tests..."
	@bash "$(FRONTEND_ENV_CHECK)" "$(CURDIR)/web"
	@bash "$(FRONTEND_DEPS)" "$(CURDIR)/web"
	@cd web && npm run test:e2e

build:
	@mkdir -p bin
	@echo "Building acp..."
	go build -o bin/quartet-acp ./cmd/acp
	@echo "Building cli..."
	go build -o bin/quartet-cli ./cmd/quartet-cli
	@echo "Building web..."
	go build -ldflags "$(WEB_LDFLAGS)" -o bin/quartet-web ./cmd/web
	@echo "All binaries built to bin/"

build-acp:
	@mkdir -p bin
	go build -o bin/quartet-acp ./cmd/acp

build-cli:
	@mkdir -p bin
	go build -o bin/quartet-cli ./cmd/quartet-cli

# build-eino-cli builds the standalone eino-cli ACP agent and installs it to
# INSTALL_BIN_DIR (must be on $PATH) so the backend's probe can discover it via
# exec.LookPath("eino-cli").
build-eino-cli:
	@mkdir -p bin
	@echo "==> Building bin/eino-cli"
	go build -o bin/eino-cli ./cmd/eino-cli
	@echo "==> Installing eino-cli to $(INSTALL_BIN_DIR)"
	@mkdir -p "$(INSTALL_BIN_DIR)"
	@cp bin/eino-cli "$(INSTALL_BIN_DIR)/eino-cli"
	@installed="$$(cd "$(INSTALL_BIN_DIR)" && pwd)/eino-cli"; \
	found="$$(command -v eino-cli 2>/dev/null || true)"; \
	printf '[ok] eino-cli installed: %s\n' "$$installed"; \
	if test "$$found" = "$$installed"; then \
		printf '  PATH resolves to installed eino-cli\n'; \
	else \
		printf 'warning: %s is installed but eino-cli is not resolvable on PATH (found=%q); add %s to PATH\n' "$$installed" "$$found" "$$(cd "$(INSTALL_BIN_DIR)" && pwd)" >&2; \
	fi

build-web:
	@mkdir -p bin
	go build -ldflags "$(WEB_LDFLAGS)" -o bin/quartet-web ./cmd/web

# Build only the frontend SPA into static/ (no backend build, no restart). Safe
# to run from inside an agent shell — it never touches the running backend.
# Syncs frontend deps first when node_modules is missing or package(-lock).json
# changed, then runs the vite production build (outputs to ../static per
# web/vite.config.ts).
build-frontend:
	@echo "🎨 Building frontend into static/ ..."; \
	bash "$(FRONTEND_ENV_CHECK)" "$(CURDIR)/web" || exit 1; \
	bash "$(FRONTEND_DEPS)" "$(CURDIR)/web" || exit 1; \
	cd web || exit 1; \
	npm run build || { echo "❌ Frontend build failed"; exit 1; }; \
	echo "✅ Frontend built into static/"

build-ios:
	@if ! command -v xcodebuild >/dev/null 2>&1; then \
		echo "❌ xcodebuild is required; run this target on macOS with Xcode installed"; \
		exit 1; \
	fi
	xcodebuild -workspace ios/Quartet.xcworkspace -scheme Quartet -configuration Debug -destination 'generic/platform=iOS' build

test-ios:
	@if ! command -v xcodebuild >/dev/null 2>&1; then \
		echo "❌ xcodebuild is required; run this target on macOS with Xcode installed"; \
		exit 1; \
	fi
	xcodebuild -workspace ios/Quartet.xcworkspace -scheme Quartet -configuration Debug -destination 'generic/platform=iOS Simulator' -derivedDataPath '$(IOS_DERIVED_DATA)' CODE_SIGNING_ALLOWED=NO build

e2e-ios:
	@if ! command -v xcodebuild >/dev/null 2>&1; then \
		echo "❌ xcodebuild is required; run this target on macOS with Xcode installed"; \
		exit 1; \
	fi
	xcodebuild -workspace ios/Quartet.xcworkspace -scheme Quartet -configuration Debug -destination '$(IOS_TEST_DESTINATION)' -derivedDataPath '$(IOS_DERIVED_DATA)' CODE_SIGNING_ALLOWED=NO test

run-cli:
	go run ./cmd/quartet-cli

run-backend:
	go run ./cmd/web

run-frontend:
	@bash "$(FRONTEND_ENV_CHECK)" "$(CURDIR)/web"
	@bash "$(FRONTEND_DEPS)" "$(CURDIR)/web"
	# With certs present the dev server binds :443, which needs sudo. Run it with
	# VITE_CACHE_DIR pointing outside node_modules so the root-owned vite dep cache
	# never lands in web/node_modules (a root-owned cache makes the next plain-user
	# `npm ci` in build-frontend fail with EACCES on rmdir).
	@certs_sudo=0; \
	if [ -f "$(CERTS_DIR)/cert.pem" ] && [ -f "$(CERTS_DIR)/key.pem" ] && [ "$$(id -u)" != "0" ]; then \
		certs_sudo=1; \
	fi; \
	if [ "$$certs_sudo" = "1" ]; then \
		cd web && sudo env VITE_CACHE_DIR="/tmp/quartet-vite-dev-cache-$$(id -un)" npm run dev; \
	else \
		cd web && npm run dev; \
	fi

run: web

web: 
	@if [ -z "$$LOCAL_MEMORY" ]; then \
		echo "❌ LOCAL_MEMORY environment variable is not set. Please set it first."; \
		echo "   Example: export LOCAL_MEMORY=/path/to/local_memory"; \
		exit 1; \
	fi; \
	echo "✅ LOCAL_MEMORY configured: $$LOCAL_MEMORY (layout is validated by quartet-web at startup)"
	@$(MAKE) build-frontend
	@echo "📦 Building backend..."; \
	mkdir -p bin; \
	go build -ldflags "$(WEB_LDFLAGS)" -o bin/quartet-web ./cmd/web || exit 1; \
	echo "✅ Backend built successfully"
	@bash "$(CURDIR)/scripts/web-serve.sh" "$(CURDIR)" "$(BACKEND_PORT)" "$(SUDO)" "$(BACKEND_PROTO)"

web-logs:
	@tail -f $(BACKEND_LOG)

web-watch:
	@if [ -z "$$LOCAL_MEMORY" ]; then \
		echo "❌ LOCAL_MEMORY environment variable is not set. Please set it first."; \
		exit 1; \
	fi; \
	if [ -f "$(WATCHDOG_PID)" ] && kill -0 "$$(cat $(WATCHDOG_PID) 2>/dev/null)" 2>/dev/null; then \
		echo "ℹ️  Watchdog already running (pid: $$(cat $(WATCHDOG_PID)))"; \
		exit 0; \
	fi; \
	chmod +x "$(WATCHDOG)" 2>/dev/null || true; \
	: > $(WATCHDOG_LOG); \
	echo "🐶 Starting backend watchdog (detached)..."; \
	if command -v setsid >/dev/null 2>&1; then \
		( setsid "$(WATCHDOG)" "$(CURDIR)" "$(BACKEND_PORT)" </dev/null >>$(WATCHDOG_LOG) 2>&1 & ); \
	elif command -v perl >/dev/null 2>&1; then \
		( perl -e 'use POSIX qw(setsid); setsid(); exec @ARGV or die "exec: $$!"' "$(WATCHDOG)" "$(CURDIR)" "$(BACKEND_PORT)" </dev/null >>$(WATCHDOG_LOG) 2>&1 & ); \
	else \
		( "$(WATCHDOG)" "$(CURDIR)" "$(BACKEND_PORT)" </dev/null >>$(WATCHDOG_LOG) 2>&1 & ); \
	fi; \
	sleep 1; \
	if [ -f "$(WATCHDOG_PID)" ] && kill -0 "$$(cat $(WATCHDOG_PID) 2>/dev/null)" 2>/dev/null; then \
		echo "✅ Watchdog running (pid: $$(cat $(WATCHDOG_PID)), log: $(WATCHDOG_LOG))"; \
	else \
		echo "⚠️  Watchdog did not report a pid yet; check $(WATCHDOG_LOG)"; \
	fi

web-watch-stop:
	@if [ -f "$(WATCHDOG_PID)" ]; then \
		_wpid=$$(cat $(WATCHDOG_PID) 2>/dev/null); \
		if [ -n "$$_wpid" ] && kill -0 "$$_wpid" 2>/dev/null; then \
			echo "🛑 Stopping watchdog (pid: $$_wpid)... services left running"; \
			kill "$$_wpid" 2>/dev/null || true; \
			for _i in 1 2 3 4 5; do \
				kill -0 "$$_wpid" 2>/dev/null || break; \
				sleep 1; \
			done; \
			if kill -0 "$$_wpid" 2>/dev/null; then \
				echo "⚠️  Watchdog still alive after SIGTERM; sending SIGKILL"; \
				kill -9 "$$_wpid" 2>/dev/null || true; \
				sleep 1; \
			fi; \
			if kill -0 "$$_wpid" 2>/dev/null; then \
				echo "❌ Failed to stop watchdog (pid: $$_wpid)"; \
			else \
				echo "✅ Watchdog stopped"; \
			fi; \
		else \
			echo "ℹ️  Watchdog not running (stale pidfile)"; \
		fi; \
		rm -f "$(WATCHDOG_PID)"; \
	else \
		echo "ℹ️  No watchdog pidfile; nothing to stop"; \
	fi

web-watch-logs:
	@tail -f $(WATCHDOG_LOG)

web-stop:
	@echo "🛑 Stopping web service (backend on :$(BACKEND_PORT))..."
	@backend_pid=$$($(SUDO) lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN 2>/dev/null); \
	backend_orphans=""; \
	for _p in $$(pgrep -x quartet-web 2>/dev/null); do \
		if [ "$$_p" != "$$backend_pid" ]; then backend_orphans="$$backend_orphans $$_p"; fi; \
	done; \
	if [ -n "$$backend_pid" ]; then \
		echo "Stopping backend (pid: $$backend_pid)..."; \
		$(SUDO) kill "$$backend_pid" 2>/dev/null || true; \
	fi; \
	if [ -n "$$backend_orphans" ]; then \
		echo "🧹 Killing orphan quartet-web processes:$$backend_orphans"; \
		for _p in $$backend_orphans; do $(SUDO) kill "$$_p" 2>/dev/null || true; done; \
	fi; \
	echo "✅ Backend stopped"

backend-stop:
	@echo "🛑 Stopping backend only (watchdog untouched)..."
	@backend_pid=$$(lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN 2>/dev/null); \
	backend_orphans=""; \
	for _p in $$(pgrep -x quartet-web 2>/dev/null); do \
		if [ "$$_p" != "$$backend_pid" ]; then \
			backend_orphans="$$backend_orphans $$_p"; \
		fi; \
	done; \
	if [ -n "$$backend_pid" ]; then \
		echo "Stopping backend (pid: $$backend_pid, port: $(BACKEND_PORT))..."; \
		kill "$$backend_pid" 2>/dev/null || true; \
	else \
		echo "ℹ️  No backend running on port $(BACKEND_PORT)"; \
	fi; \
	if [ -n "$$backend_orphans" ]; then \
		echo "🧹 Killing orphan quartet-web processes:$$backend_orphans"; \
		for _p in $$backend_orphans; do \
			kill "$$_p" 2>/dev/null || true; \
		done; \
	fi; \
	for _i in 1 2 3 4 5 6 7 8 9 10; do \
		lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN >/dev/null 2>&1 || break; \
		sleep 1; \
	done; \
	if lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "⚠️  Backend still bound to port $(BACKEND_PORT) after SIGTERM; sending SIGKILL"; \
		[ -n "$$backend_pid" ] && kill -9 "$$backend_pid" 2>/dev/null || true; \
		sleep 1; \
	fi; \
	if lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN >/dev/null 2>&1; then \
		echo "❌ Backend port $(BACKEND_PORT) is still in use"; \
		exit 1; \
	fi; \
	echo "✅ Backend stopped"

web-status:
	@echo "📊 Web service status:"
	@backend_pid=$$($(SUDO) lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN 2>/dev/null); \
	backend_orphans=""; \
	for _p in $$(pgrep -x quartet-web 2>/dev/null); do \
		if [ "$$_p" != "$$backend_pid" ]; then backend_orphans="$$backend_orphans $$_p"; fi; \
	done; \
	if [ -n "$$backend_pid" ]; then \
		echo "  Backend:  ✅ Running (pid: $$backend_pid, $(BACKEND_PROTO)://0.0.0.0:$(BACKEND_PORT))"; \
	else \
		echo "  Backend:  ❌ Not running"; \
	fi; \
	if [ -n "$$backend_orphans" ]; then \
		echo "  ⚠️  Orphan quartet-web processes (not bound to :$(BACKEND_PORT)):$$backend_orphans"; \
		echo "     Run 'make web-stop' to clean them up."; \
	fi; \
	if [ -f "$(WATCHDOG_PID)" ] && kill -0 "$$(cat $(WATCHDOG_PID) 2>/dev/null)" 2>/dev/null; then \
		echo "  Watchdog: ✅ Running (pid: $$(cat $(WATCHDOG_PID)), log: $(WATCHDOG_LOG))"; \
	else \
		echo "  Watchdog: ❌ Not running (start with 'make web-watch')"; \
	fi

clean:
	rm -rf bin

# install-skill installs the quartet-workflow skill: first build+install its CLI
# onto PATH, then register the skill directory with the `skills` CLI. Override
# SKILL_AGENTS / INSTALL_BIN_DIR / SKILLS_CLI / SKILL_SOURCE as needed.
install-skill: install-skill-cli
	@$(MAKE) --no-print-directory install-skill-run

# install-project-tools is the one-click setup used by the Web skill settings
# page. It builds/installs quartet-cli, installs every skill currently shipped
# under SKILL_SOURCE for every supported agent, then verifies the known Quartet
# skills are visible in the global skill list.
install-project-tools: install-skill-cli
	@printf '==> Installing all Quartet project skills\n'
	@log="$$(mktemp "$${TMPDIR:-/tmp}/quartet-project-skills-add.XXXXXX")"; \
	if $(SKILLS_CLI) add "$(SKILL_SOURCE)" -g --all --full-depth >"$$log" 2>&1; then \
		cat "$$log"; \
		rm -f "$$log"; \
	else \
		status=$$?; \
		printf 'error: project skills install failed; full log follows:\n' >&2; \
		sed 's/^/  /' "$$log" >&2; \
		rm -f "$$log"; \
		exit "$$status"; \
	fi
	@$(SKILLS_CLI) ls -g --json | python3 -c "import json, sys; expected = set(sys.argv[1:]); items = json.load(sys.stdin); installed = {item.get('name') for item in items}; missing = sorted(expected - installed); sys.exit('error: missing installed project skills: {}'.format(', '.join(missing))) if missing else None; print('[ok] Project skills installed: {}'.format(', '.join(sorted(expected))))" $(PROJECT_SKILL_NAMES)

# install-skill-copy installs the skill files by copying them (--copy) instead
# of symlinking, after installing the CLI.
install-skill-copy: install-skill-cli
	@$(MAKE) --no-print-directory install-skill-run SKILL_COPY_FLAG=--copy

# install-skill-cli builds the quartet-cli binary and installs it to
# INSTALL_BIN_DIR so the skill's SKILL.md (requires.bins: quartet-cli) can
# resolve it on PATH. Warns if INSTALL_BIN_DIR is not the PATH entry that wins.
install-skill-cli:
	@echo "==> Building $(QUARTET_CLI_BIN)"
	@mkdir -p bin
	@go build -o $(QUARTET_CLI_BIN) ./cmd/quartet-cli
	@echo "==> Installing CLI to $(INSTALL_BIN_DIR)"
	@mkdir -p "$(INSTALL_BIN_DIR)"
	@cp "$(QUARTET_CLI_BIN)" "$(INSTALL_BIN_DIR)/quartet-cli"
	@installed="$$(cd "$(INSTALL_BIN_DIR)" && pwd)/quartet-cli"; \
	found="$$(command -v quartet-cli 2>/dev/null || true)"; \
	"$$installed" --help >/dev/null 2>&1 || true; \
	printf '[ok] CLI installed: %s\n' "$$installed"; \
	if test "$$found" = "$$installed"; then \
		printf '  PATH resolves to installed CLI\n'; \
	else \
		printf 'warning: %s is installed but not the quartet-cli found on PATH; add %s to PATH before using the skill\n' "$$installed" "$$(cd "$(INSTALL_BIN_DIR)" && pwd)" >&2; \
	fi

# install-skill-run registers the skill directory with the `skills` CLI for each
# agent in SKILL_AGENTS. On failure the full skills-add log is printed; on
# success it verifies the skill is listed by `skills ls -g`.
install-skill-run:
	@printf '==> Installing skill %s for agents: %s\n' "$(SKILL_NAME)" "$(SKILL_AGENTS)"
	@log="$$(mktemp "$${TMPDIR:-/tmp}/quartet-skills-add.XXXXXX")"; \
	if $(SKILLS_CLI) add "$(SKILL_SOURCE)" -g --skill "$(SKILL_NAME)" $(SKILL_AGENT_FLAGS) -y --full-depth $(SKILL_COPY_FLAG) >"$$log" 2>&1; then \
		rm -f "$$log"; \
	else \
		status=$$?; \
		printf 'error: skills add failed; full log follows:\n' >&2; \
		sed 's/^/  /' "$$log" >&2; \
		rm -f "$$log"; \
		exit "$$status"; \
	fi
	@$(SKILLS_CLI) ls -g --json | python3 -c "import json, sys; name = sys.argv[1]; items = json.load(sys.stdin); matches = [item for item in items if item.get('name') == name]; \
sys.exit('error: {} is not listed by skills ls -g'.format(name)) if not matches else None; \
item = matches[0]; print('[ok] Skill installed: {}'.format(item.get('path', '(unknown)'))); agents = ', '.join(item.get('agents') or []); print('  Agents: {}'.format(agents or '(none)'))" "$(SKILL_NAME)"

# install-skill-all installs the skill for every agent the `skills` CLI knows.
install-skill-all:
	@$(MAKE) --no-print-directory install-skill SKILL_AGENTS='*'

# install-skill-list lists the skills discoverable under SKILL_SOURCE without
# installing anything (useful to confirm the skill is detected).
install-skill-list:
	$(SKILLS_CLI) add "$(SKILL_SOURCE)" --list --full-depth
