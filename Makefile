.PHONY: build build-all build-acp build-cli build-web build-frontend test test-web e2e clean run run-cli run-web run-frontend run-backend web web-logs web-stop web-status backend-stop web-watch web-watch-stop web-watch-logs install-acp-deps install-skill install-skill-cli install-skill-copy install-skill-run install-skill-all install-skill-list

CERTS_DIR := $(CURDIR)/certs
# Serving model, derived ONCE at parse time so every target below
# (start / stop / status / watchdog / restart) agrees on the same port and
# privilege:
#   certs present -> backend serves HTTPS on all interfaces at :443 (reachable
#                    by domain); binding 443 as non-root needs sudo.
#   no certs      -> plaintext HTTP on loopback :8090.
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
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GIT_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
GIT_DIRTY ?= $(shell if [ -n "$$(git status --porcelain 2>/dev/null)" ]; then echo true; else echo false; fi)
WEB_LDFLAGS := -X main.buildTime=$(BUILD_TIME) -X main.buildCommit=$(GIT_COMMIT) -X main.buildDirty=$(GIT_DIRTY)

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
SKILL_AGENT_FLAGS = $(foreach agent,$(SKILL_AGENTS),--agent "$(agent)")
SKILL_COPY_FLAG ?=

build-all:
	@echo "Building all applications..."
	go build ./...
	@echo "All applications built successfully!"

test: build-all test-web e2e

test-web:
	@echo "Running web component tests..."
	@cd web && npm test

e2e:
	@echo "Running web E2E tests..."
	@cd web && npm run test:e2e

build:
	@mkdir -p bin
	@echo "Building acp..."
	go build -o bin/quartet-acp ./cmd/acp
	@echo "Building cli..."
	go build -o bin/quartet-cli ./cmd/cli
	@echo "Building web..."
	go build -ldflags "$(WEB_LDFLAGS)" -o bin/quartet-web ./cmd/web
	@echo "All binaries built to bin/"

build-acp:
	@mkdir -p bin
	go build -o bin/quartet-acp ./cmd/acp

build-cli:
	@mkdir -p bin
	go build -o bin/quartet-cli ./cmd/cli

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
	cd web || exit 1; \
	if [ ! -d node_modules ] || [ ! -x node_modules/.bin/vite ]; then \
		echo "📦 Installing frontend dependencies..."; \
		if [ -f package-lock.json ]; then npm ci || npm install || exit 1; else npm install || exit 1; fi; \
		touch node_modules; \
	elif [ package.json -nt node_modules ] || { [ -f package-lock.json ] && [ package-lock.json -nt node_modules ]; }; then \
		echo "📦 Syncing frontend dependencies..."; \
		if [ -f package-lock.json ]; then npm ci || npm install || exit 1; else npm install || exit 1; fi; \
		touch node_modules; \
	fi; \
	npm run build || { echo "❌ Frontend build failed"; exit 1; }; \
	echo "✅ Frontend built into static/"

run-cli:
	go run ./cmd/cli

run-backend:
	go run ./cmd/web

run-frontend:
	@cd web && ( \
		_need_install=0; \
		if [ ! -d node_modules ] || [ ! -x node_modules/.bin/vite ]; then \
			_need_install=1; \
			echo "📦 node_modules missing, installing frontend dependencies..."; \
		elif [ package.json -nt node_modules ] || { [ -f package-lock.json ] && [ package-lock.json -nt node_modules ]; }; then \
			_need_install=1; \
			echo "📦 package.json/lock changed, syncing frontend dependencies..."; \
		fi; \
		if [ "$$_need_install" = "1" ]; then \
			if [ -f package-lock.json ]; then \
				npm ci || npm install || exit 1; \
			else \
				npm install || exit 1; \
			fi; \
			touch node_modules; \
		fi; \
	)
	@if [ -f "$(CERTS_DIR)/cert.pem" ] && [ -f "$(CERTS_DIR)/key.pem" ] && [ "$$(id -u)" != "0" ]; then \
		cd web && sudo npm run dev; \
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

# install-acp-deps installs (or upgrades) the npm packages required for
# ACP agents: Claude Code, Codex, and OpenCode.
#
# --force is required because these bins (e.g. codex-acp) may already exist as
# leftover symlinks from a differently-scoped package; without it npm aborts
# with EEXIST, which — since this is a prerequisite of `web` — would block the
# whole build/restart before it ever recompiles or bounces the backend.
install-acp-deps:
	@echo "📦 Installing/upgrading ACP agent dependencies..."
	@npm install -g @agentclientprotocol/claude-agent-acp || { echo "❌ Failed to install @agentclientprotocol/claude-agent-acp"; exit 1; }
	@npm install -g @agentclientprotocol/codex-acp || { echo "❌ Failed to install @agentclientprotocol/codex-acp"; exit 1; }
	@npm install -g opencode-ai || { echo "❌ Failed to install opencode-ai"; exit 1; }
	@echo "✅ ACP dependencies ready"

# install-skill installs the quartet-workflow skill: first build+install its CLI
# onto PATH, then register the skill directory with the `skills` CLI. Override
# SKILL_AGENTS / INSTALL_BIN_DIR / SKILLS_CLI / SKILL_SOURCE as needed.
install-skill: install-skill-cli
	@$(MAKE) --no-print-directory install-skill-run

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
	@go build -o $(QUARTET_CLI_BIN) ./cmd/cli
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
