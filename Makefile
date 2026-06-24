.PHONY: build build-all build-acp build-cli build-web test test-web e2e clean run run-cli run-web run-frontend run-backend web web-logs web-stop web-status backend-stop web-watch web-watch-stop web-watch-logs install-acp-deps

BACKEND_PORT := 8090
CERTS_DIR := $(CURDIR)/certs
WEB_BINARY := $(CURDIR)/bin/quartet-web
BACKEND_LOG := /tmp/quartet-backend.log
FRONTEND_LOG := /tmp/quartet-vite.log
WATCHDOG_LOG := /tmp/quartet-watchdog.log
WATCHDOG_PID := /tmp/quartet-watchdog.pid
STOP_PROCESS_TREE := $(CURDIR)/scripts/stop-process-tree.sh
WATCHDOG := $(CURDIR)/scripts/watchdog.sh
BUILD_TIME ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
GIT_COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
GIT_DIRTY ?= $(shell if [ -n "$$(git status --porcelain 2>/dev/null)" ]; then echo true; else echo false; fi)
WEB_LDFLAGS := -X main.buildTime=$(BUILD_TIME) -X main.buildCommit=$(GIT_COMMIT) -X main.buildDirty=$(GIT_DIRTY)

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
	mkdir -p "$$LOCAL_MEMORY/workspaces" "$$LOCAL_MEMORY/knowledge" "$$LOCAL_MEMORY/agent" "$$LOCAL_MEMORY/bin" "$$LOCAL_MEMORY/shell" "$$LOCAL_MEMORY/im"; \
	chmod 755 "$$LOCAL_MEMORY/workspaces" "$$LOCAL_MEMORY/knowledge" "$$LOCAL_MEMORY/agent" "$$LOCAL_MEMORY/bin" "$$LOCAL_MEMORY/shell" "$$LOCAL_MEMORY/im"; \
	chmod 777 "$$LOCAL_MEMORY/workspaces"; \
	echo "✅ LOCAL_MEMORY directories ready: $$LOCAL_MEMORY/{workspaces,knowledge,agent,bin,im}"; \
	echo "🧹 Cleaning previous logs: $(BACKEND_LOG), $(FRONTEND_LOG)"; \
	: > $(BACKEND_LOG); : > $(FRONTEND_LOG); \
	echo "🚀 Starting web services..."; \
	if [ -f "$(CERTS_DIR)/cert.pem" ] && [ -f "$(CERTS_DIR)/key.pem" ]; then \
		frontend_port=443; \
		frontend_proto="https"; \
		if [ "$$(id -u)" != "0" ]; then SUDO="sudo"; else SUDO=""; fi; \
		echo "🔒 SSL certificates found, using HTTPS mode (port 443)"; \
	else \
		frontend_port=5173; \
		frontend_proto="http"; \
		SUDO=""; \
		echo "🔓 No SSL certificates found, using HTTP mode (port 5173)"; \
	fi; \
	backend_pid=$$(lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN 2>/dev/null); \
	backend_orphans=""; \
	for _p in $$(pgrep -x quartet-web 2>/dev/null); do \
		if [ "$$_p" != "$$backend_pid" ]; then \
			backend_orphans="$$backend_orphans $$_p"; \
		fi; \
	done; \
	frontend_pid=$$( $$SUDO lsof -tiTCP:$$frontend_port -sTCP:LISTEN 2>/dev/null); \
	if [ -d /proc ]; then \
		_vite_orphans=""; \
		for _vp in $$( $$SUDO pgrep -x node 2>/dev/null); do \
			_cmd=$$($$SUDO tr '\0' ' ' < /proc/$$_vp/cmdline 2>/dev/null || echo ""); \
			case "$$_cmd" in \
				*node_modules/.bin/vite*) ;; \
				*) continue ;; \
			esac; \
			[ "$$_vp" = "$$frontend_pid" ] && continue; \
			_cwd=$$($$SUDO readlink "/proc/$$_vp/cwd" 2>/dev/null || echo ""); \
			case "$$_cwd" in \
				"$(CURDIR)/web"|*"(deleted)") \
					_vite_orphans="$$_vite_orphans $$_vp" ;; \
			esac; \
		done; \
		if [ -n "$$_vite_orphans" ]; then \
			echo "🧹 Killing orphan vite processes (this repo's web dir or cwd deleted, any port):$$_vite_orphans"; \
			for _vp in $$_vite_orphans; do \
				$$SUDO "$(STOP_PROCESS_TREE)" "$$_vp"; \
			done; \
		fi; \
		_self_pgid=$$(ps -o pgid= -p $$$$ 2>/dev/null | tr -d ' '); \
		_make_orphans=""; \
		for _mp in $$( $$SUDO pgrep -x make 2>/dev/null); do \
			_mp_ppid=$$(ps -o ppid= -p $$_mp 2>/dev/null | tr -d ' '); \
			[ "$$_mp_ppid" != "1" ] && continue; \
			_mp_pgid=$$(ps -o pgid= -p $$_mp 2>/dev/null | tr -d ' '); \
			[ "$$_mp_pgid" = "$$_self_pgid" ] && continue; \
			_mp_cmd=$$($$SUDO tr '\0' ' ' < /proc/$$_mp/cmdline 2>/dev/null || echo ""); \
			case "$$_mp_cmd" in \
				"make web "*|"make web") _make_orphans="$$_make_orphans $$_mp" ;; \
			esac; \
		done; \
		if [ -n "$$_make_orphans" ]; then \
			echo "🧹 Killing orphan 'make web' process trees (PPID=1):$$_make_orphans"; \
			for _mp in $$_make_orphans; do \
				$$SUDO "$(STOP_PROCESS_TREE)" "$$_mp"; \
			done; \
		fi; \
	fi; \
	if [ -n "$$frontend_pid" ]; then \
		echo "🔄 Stopping existing frontend (pid: $$frontend_pid)..."; \
		$$SUDO "$(STOP_PROCESS_TREE)" "$$frontend_pid"; \
		for i in 1 2 3 4 5; do \
			sleep 1; \
			if ! $$SUDO lsof -tiTCP:$$frontend_port -sTCP:LISTEN >/dev/null 2>&1; then \
				break; \
			fi; \
		done; \
		if $$SUDO lsof -tiTCP:$$frontend_port -sTCP:LISTEN >/dev/null 2>&1; then \
			echo "❌ Frontend port $$frontend_port is still in use after stop attempt"; \
			exit 1; \
		fi; \
	fi; \
	if [ -n "$$backend_orphans" ]; then \
		echo "🧹 Killing orphan quartet-web processes (not bound to port $(BACKEND_PORT)):$$backend_orphans"; \
		for _p in $$backend_orphans; do \
			kill "$$_p" 2>/dev/null || true; \
		done; \
	fi; \
	echo "📦 Building backend..."; \
	mkdir -p bin; \
	go build -ldflags "$(WEB_LDFLAGS)" -o bin/quartet-web ./cmd/web || exit 1; \
	echo "✅ Backend built successfully"; \
	_old_backend_pid="$$backend_pid"; \
	if [ -n "$$backend_pid" ]; then \
		echo "🔄 Restarting backend (pid: $$backend_pid) via detached process..."; \
		: > /tmp/quartet-restart.log; \
		if command -v setsid >/dev/null 2>&1; then \
			( setsid "$(CURDIR)/scripts/restart-backend.sh" "$$backend_pid" "$(BACKEND_PORT)" "$$(pwd)" </dev/null >>/tmp/quartet-restart.log 2>&1 & ); \
		elif command -v perl >/dev/null 2>&1; then \
			( perl -e 'use POSIX qw(setsid); setsid(); exec @ARGV or die "exec: $$!"' "$(CURDIR)/scripts/restart-backend.sh" "$$backend_pid" "$(BACKEND_PORT)" "$$(pwd)" </dev/null >>/tmp/quartet-restart.log 2>&1 & ); \
		else \
			( "$(CURDIR)/scripts/restart-backend.sh" "$$backend_pid" "$(BACKEND_PORT)" "$$(pwd)" </dev/null >>/tmp/quartet-restart.log 2>&1 & ); \
		fi; \
		echo "✅ Backend restart initiated (log: /tmp/quartet-restart.log)"; \
	else \
		echo "🌐 Starting backend on port $(BACKEND_PORT)..."; \
		printf "\n----- start at $$(date '+%F %T') -----\n" >> /tmp/quartet-backend.log; \
		nohup ./bin/quartet-web >> /tmp/quartet-backend.log 2>&1 & \
	fi; \
	echo "⏳ Waiting for backend to be ready on port $(BACKEND_PORT) (timeout 60s)..."; \
	_ready_start=$$(date +%s); \
	_ready_ok=0; \
	_ready_stage_old_dead=0; \
	if [ -z "$$_old_backend_pid" ]; then _ready_stage_old_dead=1; fi; \
	for _i in $$(seq 1 120); do \
		if [ "$$_ready_stage_old_dead" = "0" ]; then \
			if ! kill -0 "$$_old_backend_pid" 2>/dev/null; then \
				_ready_stage_old_dead=1; \
			fi; \
		else \
			_cur_pid=$$(lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN 2>/dev/null | head -n1); \
			if [ -n "$$_cur_pid" ] && [ "$$_cur_pid" != "$$_old_backend_pid" ]; then \
				_ready_ok=1; \
				_ready_pid=$$_cur_pid; \
				break; \
			fi; \
		fi; \
		sleep 0.5; \
	done; \
	_ready_elapsed=$$(($$(date +%s) - _ready_start)); \
	if [ "$$_ready_ok" = "1" ]; then \
		echo "✅ Backend ready on port $(BACKEND_PORT) (pid: $$_ready_pid, $${_ready_elapsed}s)"; \
	else \
		echo "❌ Backend did not become ready on port $(BACKEND_PORT) within $${_ready_elapsed}s"; \
		echo "   Last 40 lines of backend log:"; \
		tail -n 40 $(BACKEND_LOG) 2>/dev/null | sed 's/^/   /'; \
		echo "   Full log: tail -f $(BACKEND_LOG)"; \
		exit 1; \
	fi; \
	echo "🎨 Starting frontend on port $$frontend_port..."; \
	cd web && ( \
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
		printf "\n----- vite start at $$(date '+%F %T') -----\n" >> $(FRONTEND_LOG); \
		$$SUDO nohup npm run dev >> $(FRONTEND_LOG) 2>&1 & \
	); \
	echo "⏳ Waiting for frontend to be ready on port $$frontend_port (timeout 60s)..."; \
	_fe_start=$$(date +%s); \
	_fe_ok=0; \
	_fe_pid=""; \
	for _i in $$(seq 1 120); do \
		_fe_pid=$$( $$SUDO lsof -tiTCP:$$frontend_port -sTCP:LISTEN 2>/dev/null | head -n1); \
		if [ -n "$$_fe_pid" ]; then \
			_fe_ok=1; \
			break; \
		fi; \
		sleep 0.5; \
	done; \
	_fe_elapsed=$$(($$(date +%s) - _fe_start)); \
	if [ "$$_fe_ok" = "1" ]; then \
		echo "✅ Frontend ready on port $$frontend_port (pid: $$_fe_pid, $${_fe_elapsed}s)"; \
	else \
		echo "❌ Frontend did not become ready on port $$frontend_port within $${_fe_elapsed}s"; \
		echo "   Last 40 lines of vite log:"; \
		tail -n 40 $(FRONTEND_LOG) 2>/dev/null | sed 's/^/   /'; \
		echo "   Full log: tail -f $(FRONTEND_LOG)"; \
		exit 1; \
	fi; \
	echo ""; \
	if [ "$$frontend_port" = "443" ]; then \
		frontend_url="https://local.fanlv.fun/"; \
	else \
		frontend_url="http://localhost:$$frontend_port"; \
	fi; \
	echo "================================================"; \
	echo "  Backend:  http://localhost:$(BACKEND_PORT)"; \
	echo "  Frontend: $$frontend_url"; \
	echo "================================================"; \
	echo ""; \
	echo "Stop: make web-stop"; \
	echo ""; \
	echo "Logs (backend):  tail -f $(BACKEND_LOG)"; \
	echo "Logs (vite dev): tail -f $(FRONTEND_LOG)   # Vite dev server only"; \
	echo "Logs (browser):  open Settings → 日志 in the UI     # browser runtime warn/error"; \
	echo ""; \
	echo "📜 Follow backend logs: make web-logs"; \
	echo ""

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
	echo "🐶 Starting backend/frontend watchdog (detached)..."; \
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
	@echo "🛑 Stopping web services..."
	@if [ -f "$(CERTS_DIR)/cert.pem" ] && [ -f "$(CERTS_DIR)/key.pem" ]; then \
		frontend_port=443; \
		if [ "$$(id -u)" != "0" ]; then SUDO="sudo"; else SUDO=""; fi; \
	else \
		frontend_port=5173; \
		SUDO=""; \
	fi; \
	backend_pid=$$(lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN 2>/dev/null); \
	backend_orphans=""; \
	for _p in $$(pgrep -x quartet-web 2>/dev/null); do \
		if [ "$$_p" != "$$backend_pid" ]; then \
			backend_orphans="$$backend_orphans $$_p"; \
		fi; \
	done; \
	frontend_pid=$$( $$SUDO lsof -tiTCP:$$frontend_port -sTCP:LISTEN 2>/dev/null); \
	_vite_repo=""; \
	if [ -d /proc ]; then \
		for _vp in $$( $$SUDO pgrep -x node 2>/dev/null); do \
			_cmd=$$($$SUDO tr '\0' ' ' < /proc/$$_vp/cmdline 2>/dev/null || echo ""); \
			case "$$_cmd" in *node_modules/.bin/vite*) ;; *) continue ;; esac; \
			[ "$$_vp" = "$$frontend_pid" ] && continue; \
			_cwd=$$($$SUDO readlink "/proc/$$_vp/cwd" 2>/dev/null || echo ""); \
			case "$$_cwd" in "$(CURDIR)/web"|*"(deleted)") _vite_repo="$$_vite_repo $$_vp" ;; esac; \
		done; \
	fi; \
	if [ -n "$$backend_pid" ]; then \
		echo "Stopping backend (pid: $$backend_pid)..."; \
		kill "$$backend_pid" 2>/dev/null || true; \
	fi; \
	if [ -n "$$backend_orphans" ]; then \
		echo "🧹 Killing orphan quartet-web processes:$$backend_orphans"; \
		for _p in $$backend_orphans; do \
			kill "$$_p" 2>/dev/null || true; \
		done; \
	fi; \
	if [ -n "$$frontend_pid" ]; then \
		echo "Stopping frontend (pid: $$frontend_pid)..."; \
		$$SUDO "$(STOP_PROCESS_TREE)" "$$frontend_pid"; \
	fi; \
	if [ -n "$$_vite_repo" ]; then \
		echo "🧹 Killing other vite processes for this repo (any port):$$_vite_repo"; \
		for _vp in $$_vite_repo; do \
			$$SUDO "$(STOP_PROCESS_TREE)" "$$_vp"; \
		done; \
	fi; \
	echo "✅ All services stopped"

backend-stop:
	@echo "🛑 Stopping backend only (frontend untouched)..."
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
	@echo "📊 Web services status:"
	@if [ -f "$(CERTS_DIR)/cert.pem" ] && [ -f "$(CERTS_DIR)/key.pem" ]; then \
		frontend_port=443; \
		frontend_proto="https"; \
		if [ "$$(id -u)" != "0" ]; then SUDO="sudo"; else SUDO=""; fi; \
	else \
		frontend_port=5173; \
		frontend_proto="http"; \
		SUDO=""; \
	fi; \
	backend_pid=$$(lsof -tiTCP:$(BACKEND_PORT) -sTCP:LISTEN 2>/dev/null); \
	backend_orphans=""; \
	for _p in $$(pgrep -x quartet-web 2>/dev/null); do \
		if [ "$$_p" != "$$backend_pid" ]; then \
			backend_orphans="$$backend_orphans $$_p"; \
		fi; \
	done; \
	frontend_pid=$$( $$SUDO lsof -tiTCP:$$frontend_port -sTCP:LISTEN 2>/dev/null); \
	_vite_repo=""; \
	if [ -d /proc ]; then \
		for _vp in $$( $$SUDO pgrep -x node 2>/dev/null); do \
			_cmd=$$($$SUDO tr '\0' ' ' < /proc/$$_vp/cmdline 2>/dev/null || echo ""); \
			case "$$_cmd" in *node_modules/.bin/vite*) ;; *) continue ;; esac; \
			[ "$$_vp" = "$$frontend_pid" ] && continue; \
			_cwd=$$($$SUDO readlink "/proc/$$_vp/cwd" 2>/dev/null || echo ""); \
			case "$$_cwd" in "$(CURDIR)/web"|*"(deleted)") _vite_repo="$$_vite_repo $$_vp" ;; esac; \
		done; \
	fi; \
	if [ -n "$$backend_pid" ]; then \
		echo "  Backend:  ✅ Running (pid: $$backend_pid, port: $(BACKEND_PORT))"; \
	else \
		echo "  Backend:  ❌ Not running"; \
	fi; \
	if [ -n "$$backend_orphans" ]; then \
		echo "  ⚠️  Orphan quartet-web processes (not bound to port $(BACKEND_PORT)):$$backend_orphans"; \
		echo "     Run 'make web-stop' to clean them up."; \
	fi; \
	if [ -n "$$frontend_pid" ]; then \
		echo "  Frontend: ✅ Running (pid: $$frontend_pid, port: $$frontend_port, $$frontend_proto)"; \
	else \
		echo "  Frontend: ❌ Not running"; \
	fi; \
	if [ -n "$$_vite_repo" ]; then \
		echo "  ⚠️  Other vite processes for this repo on a different port:$$_vite_repo"; \
		echo "     Run 'make web-stop' to clean them up."; \
	fi; \
	if [ -f "$(WATCHDOG_PID)" ] && kill -0 "$$(cat $(WATCHDOG_PID) 2>/dev/null)" 2>/dev/null; then \
		echo "  Watchdog: ✅ Running (pid: $$(cat $(WATCHDOG_PID)), log: $(WATCHDOG_LOG))"; \
	else \
		echo "  Watchdog: ❌ Not running (start with 'make web-watch')"; \
	fi

clean:
	rm -rf bin

# install-acp-deps installs the npm packages required by npx-based ACP
# agents, but only when the agent's own CLI binary is already present in
# $PATH. For these agents the bin looked up in $PATH (e.g. `claude`) is a
# different program from what the ACP serve command actually runs (e.g.
# `npx @agentclientprotocol/claude-agent-acp`), so having the CLI alone is
# not enough to talk ACP — the helper package must be installed too.
# Agents whose bin and serve command are the same program need nothing
# here and are intentionally omitted.
install-acp-deps:
	@echo "🔍 Checking ACP agent dependencies..."
	@installed=0; \
	check_and_install() { \
		bin="$$1"; pkg="$$2"; name="$$3"; \
		if command -v "$$bin" >/dev/null 2>&1; then \
			echo "📦 $$name detected ($$bin), installing $$pkg ..."; \
			npm install -g "$$pkg" || { echo "❌ Failed to install $$pkg"; exit 1; }; \
			installed=$$((installed+1)); \
		else \
			echo "⏭️  $$name not found ($$bin in \$$PATH), skipping $$pkg"; \
		fi; \
	}; \
	check_and_install claude   "@agentclientprotocol/claude-agent-acp" "Claude"; \
	check_and_install codex    "@zed-industries/codex-acp"             "Codex"; \
	check_and_install kilocode "@kilocode/cli"                         "KiloCode"; \
	echo "✅ ACP dependency check done ($$installed package(s) installed)"
