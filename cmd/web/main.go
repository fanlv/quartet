package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/config"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/fanlv/quartet/cmd/web/handler"
	acpagent "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging/media"
	"github.com/fanlv/quartet/pkg/sandbox"
	svcacp "github.com/fanlv/quartet/services/agent/acp"
	acpprobe "github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/auth"
	"github.com/fanlv/quartet/services/schedule"
	"github.com/fanlv/quartet/types/consts"
	"github.com/hertz-contrib/cors"
)

const (
	// defaultHTTPAddr is the plaintext default when no TLS certs are present.
	defaultHTTPAddr = "0.0.0.0:8090"
	// localHTTPAddr stays loopback-only for the TLS companion listener used by
	// quartet-cli and local scripts.
	localHTTPAddr = "127.0.0.1:8090"
	// defaultHTTPSAddr is the default when TLS certs are present. It binds all
	// interfaces so the UI is reachable by domain (matching the previous vite
	// front-end behaviour on 443).
	defaultHTTPSAddr = "0.0.0.0:443"

	defaultCertsDir = "certs"
	certFileName    = "cert.pem"
	keyFileName     = "key.pem"
)

const maxRequestBodySize = 16 << 20 // 16 MiB: 10 MiB upload cap + multipart overhead.
const httpShutdownTimeout = 5 * time.Second
const sandboxShutdownTimeout = 2 * time.Minute

// Filled by `go build -ldflags` in Makefile. Keep defaults explicit so
// `go run ./cmd/web` and ad-hoc builds still produce a useful startup log
// instead of silently omitting version fields.
var (
	buildTime   = "unknown"
	buildCommit = "unknown"
	buildDirty  = "unknown"
)

// Changes on every process start so clients can distinguish a completed
// restart from a health response served by the old process.
var serverInstanceID = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

// listenConfig is the resolved server binding: the address plus an optional TLS
// config. tlsCfg is nil for plaintext HTTP.
type listenConfig struct {
	addr   string
	tlsCfg *tls.Config
}

// scheme returns "https" or "http" for logging/printing.
func (lc listenConfig) scheme() string {
	if lc.tlsCfg != nil {
		return "https"
	}
	return "http"
}

// certsDir returns the directory that may hold cert.pem/key.pem.
func certsDir() string {
	if v := strings.TrimSpace(os.Getenv(consts.EnvKeyCertsDir)); v != "" {
		return v
	}
	return defaultCertsDir
}

// resolveListen decides the final bind address and whether to enable TLS.
//
//   - certs present → load them and default the address to 0.0.0.0:443
//   - certs absent  → plaintext, default address 0.0.0.0:8090
//
// TLS is coupled ONLY to cert existence: QUARTET_LISTEN_ADDR overrides the
// default address but never the TLS decision, so a custom address still gets
// HTTPS when certs exist. A cert that exists but fails to load is a hard error
// (never a silent downgrade to HTTP) so operators aren't fooled into thinking
// HTTPS is up when it isn't.
func resolveListen(ctx context.Context) listenConfig {
	dir := certsDir()
	certPath := filepath.Join(dir, certFileName)
	keyPath := filepath.Join(dir, keyFileName)

	var tlsCfg *tls.Config
	defaultAddr := defaultHTTPAddr
	if fileExists(certPath) && fileExists(keyPath) {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			logger.Fatalf(ctx, "TLS certs found under %s but failed to load (cert=%s key=%s): %v", dir, certPath, keyPath, err)
		}
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{cert}}
		defaultAddr = defaultHTTPSAddr
	}

	addr := defaultAddr
	if v := strings.TrimSpace(os.Getenv(consts.EnvKeyListenAddr)); v != "" {
		addr = v
	}
	return listenConfig{addr: addr, tlsCfg: tlsCfg}
}

// fileExists reports whether p exists and is a regular file.
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// corsOrigins parses QUARTET_CORS_ORIGINS into a comma-separated allowlist.
// The default (empty) falls back to same-origin only: we don't emit an ACAO
// header for cross-origin requests, which is the safe default for a local
// server that ships with no web client on a third-party origin. Operators
// who proxy the UI from a different origin can set the env var explicitly.
// Cookie authentication requires concrete origins when credentials are used;
// a wildcard entry is ignored.
func corsOrigins() []string {
	v := strings.TrimSpace(os.Getenv(consts.EnvKeyCORSOrigins))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			if s == "*" {
				logger.Warnf(context.Background(), "[cors] ignore wildcard origin: cookie authentication requires an explicit origin")
				continue
			}
			out = append(out, s)
		}
	}
	return out
}

func logStartupBuildInfo(ctx context.Context) {
	exe, exeErr := os.Executable()
	if exeErr != nil {
		exe = "unknown:" + exeErr.Error()
	}
	wd, wdErr := os.Getwd()
	if wdErr != nil {
		wd = "unknown:" + wdErr.Error()
	}

	goVersion := "unknown"
	vcsRevision := "unknown"
	vcsTime := "unknown"
	vcsModified := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		goVersion = bi.GoVersion
		for _, setting := range bi.Settings {
			switch setting.Key {
			case "vcs.revision":
				if setting.Value != "" {
					vcsRevision = setting.Value
				}
			case "vcs.time":
				if setting.Value != "" {
					vcsTime = setting.Value
				}
			case "vcs.modified":
				if setting.Value != "" {
					vcsModified = setting.Value
				}
			}
		}
	}

	logger.Infof(ctx,
		"[startup] quartet-web binary: pid=%d exe=%s cwd=%s buildTime=%s buildCommit=%s buildDirty=%s go=%s vcsRevision=%s vcsTime=%s vcsModified=%s",
		os.Getpid(), exe, wd, buildTime, buildCommit, buildDirty, goVersion, vcsRevision, vcsTime, vcsModified)
}

// waitForListenReady polls the given TCP address until a dial succeeds, meaning
// the HTTP listener is actually accepting connections. Returns early if the
// server goroutine reports an error (listener bind failed) or if the root
// context is cancelled. Callers should treat a non-nil error as "the server is
// not ready and likely won't become ready" rather than a transient.
func waitForListenReady(ctx context.Context, addr string, timeout time.Duration, serverErr <-chan error) error {
	deadline := time.Now().Add(timeout)
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	for {
		select {
		case err := <-serverErr:
			if err != nil {
				return fmt.Errorf("server goroutine exited before ready: %w", err)
			}
			return fmt.Errorf("server goroutine exited before ready with nil error")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("listener not ready after %s: last dial error: %v", timeout, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func main() {
	// Honor QUARTET_LOG_LEVEL at boot. Silently ignored when unset or when
	// the value is unknown — the logger keeps its Info default.
	if lvl := os.Getenv(consts.EnvKeyLogLevel); lvl != "" {
		logger.SetLevel(lvl)
	}

	logStartupBuildInfo(context.Background())
	if os.Getenv("LOCAL_MEMORY") == "" {
		logger.Fatalf(context.Background(), "LOCAL_MEMORY environment variable is required")
	}
	// Root context is cancellable by SIGINT/SIGTERM and is the parent of all
	// long-running background tasks (eviction loops, schedulers, IM listeners,
	// etc.). When main returns, every descendant ctx is cancelled, which lets
	// retry/sleep loops exit cleanly instead of hanging onto external services
	// during shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Register ACP command allowlist and settings-backed env provider
	// before anything constructs an ACP connection. pkg/acp rejects
	// unregistered commands and skips env injection when no provider is
	// installed, and handler.NewHandler initializes the ACP probe cache
	// which opens real NewConn subprocesses — so these two registrations
	// must land BEFORE NewHandler, not after it. Split from init() (was
	// services/agent/acp/env.go + services/agent/probe/probe.go) so
	// startup wiring is visible here instead of piggybacking on import
	// ordering.
	acpprobe.InitAllowedAgentCommands()
	svcacp.InitEnvProvider()

	// Clean up orphaned ACP subprocesses left by a previous crash before
	// creating any new connections. Must run BEFORE handler.NewHandler,
	// which initializes the ACP probe cache and may spawn fresh ACP
	// subprocesses — racing the cleanup window otherwise.
	acpagent.CleanupOrphanedConns()

	h, err := handler.NewHandler(ctx)
	if err != nil {
		logger.Fatalf(ctx, "Failed to initialize handler: %v", err)
	}

	// Resolve the bind address and TLS decision ONCE. Everything below —
	// server construction, the pre-bind probe, and the readiness self-check —
	// shares this single result so address/protocol is a single-point change.
	lc := resolveListen(ctx)
	trustedProxies, err := resolveTrustedProxies()
	if err != nil {
		logger.Fatalf(ctx, "Invalid %s: %v", consts.EnvKeyTrustedProxies, err)
	}

	s := newServer(lc, trustedProxies)
	registerRoutes(s, h)

	// When TLS is active on the public address, additionally serve plaintext
	// HTTP on loopback so local tooling (quartet-cli, workflow shell scripts)
	// can reach the API without TLS/cert juggling. Without this, the cert
	// deployment leaves local clients no working address: 443 needs a valid
	// hostname/cert, and 8090 is dark.
	var local *server.Hertz
	if lc.tlsCfg != nil {
		local = newServer(listenConfig{addr: localHTTPAddr}, trustedProxies)
		registerRoutes(local, h)
	}

	// Pre-bind probe: reserve the TCP port BEFORE starting any background
	// worker (scheduler, ACP reaper, IM listeners) so a duplicate-process
	// launch fails fast with a clear error instead of silently running its
	// schedulers / IM connections alongside the real instance. We hold the
	// probe listener until just before Hertz binds so another process can't
	// steal the port between probe close and Hertz listen. A plain TCP probe is
	// fine even in TLS mode — it only reserves the port, no handshake.
	addr := lc.addr
	probe, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Fatalf(ctx, "bind probe failed on %s: %v (is another quartet instance already running?)", addr, err)
	}

	// Initialize and start the scheduler (reuse handler's schedule service to share state)
	schSvc := h.GetScheduleService()
	trigger := h.ScheduleTrigger
	scheduler := schedule.NewScheduler(schSvc, trigger)
	h.SetScheduler(scheduler)
	scheduler.Start(ctx)
	defer scheduler.Stop()

	// Spin() swallows Run() errors and then returns silently; if the listener
	// fails (e.g. port already in use) main would block on <-ctx.Done() with
	// no server running. Use Engine.Run() directly so early errors cancel the
	// root context and trigger the normal shutdown path.
	//
	// Hertz's netpoll listener panics (not returns err) when bind fails, so a
	// plain `safe.Go` would recover the panic and leave `serverErr` silent —
	// main would then block on <-ctx.Done() while the scheduler/ACP reaper/IM
	// listeners keep running. Handle the panic inline and forward it as an
	// error so the select below can cancel the root context.
	serverErr := make(chan error, 1)
	// Release the probe listener the instant before Hertz binds. A zero-RTT
	// race window remains, but a duplicate process that arrives in this gap
	// would have failed the probe above and exited.
	if err := probe.Close(); err != nil {
		logger.Warnf(ctx, "bind probe close failed: %v", err)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				serverErr <- fmt.Errorf("HTTP server panic: %v\n%s", r, debug.Stack())
			}
		}()
		serverErr <- s.Engine.Run()
	}()
	// Self-check: dial the port until the Hertz listener is actually accepting
	// connections. Without this, "Server is running on ..." fires ~0ms after the
	// goroutine starts but BEFORE Hertz finishes binding — misleading any
	// operator or start-up watcher (e.g. `make web`) that treats this log line
	// as the ready signal. The probe above already proved the port is grabbable,
	// so a failure here is unusual and points at a real problem (listener goroutine
	// panicked between probe.Close and Hertz's Listen) rather than a transient.
	if err := waitForListenReady(ctx, addr, 5*time.Second, serverErr); err != nil {
		logger.Errorf(ctx, "HTTP server readiness self-check failed on %s: %v", addr, err)
		cancel()
	} else {
		logger.Infof(ctx, "Server is running on %s://%s", lc.scheme(), addr)
	}
	authState, _ := h.AuthStatus()
	logger.Infof(ctx, "[security] user session authentication state=%s", authState)

	// Start the loopback plaintext companion listener (constructed above).
	// Non-fatal: if 127.0.0.1:8090 is unavailable the public TLS server keeps
	// working and only local tooling loses its shortcut — log loudly instead
	// of failing startup.
	if local != nil {
		localErr := make(chan error, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					localErr <- fmt.Errorf("local HTTP server panic: %v\n%s", r, debug.Stack())
				}
			}()
			localErr <- local.Engine.Run()
		}()
		if err := waitForListenReady(ctx, localHTTPAddr, 5*time.Second, localErr); err != nil {
			logger.Errorf(ctx, "local loopback HTTP listener on %s unavailable: %v (quartet-cli and local scripts cannot reach the API without it)", localHTTPAddr, err)
		} else {
			logger.Infof(ctx, "Local loopback HTTP listener is running on http://%s (for quartet-cli / local scripts)", localHTTPAddr)
		}
		// Surface a mid-run exit of the local listener. During normal shutdown
		// ctx is already cancelled, so the expected Run() return stays quiet.
		go func() {
			if err := <-localErr; err != nil && ctx.Err() == nil {
				logger.Errorf(ctx, "local loopback HTTP server exited: %v", err)
			}
		}()
	}

	// Start the idle reaper to periodically close unused ACP connections,
	// preventing unbounded subprocess memory growth.
	stopReaper := acpagent.StartIdleReaper()
	media.StartCacheCleanup(ctx)

	h.StartIMListeners(ctx)

	select {
	case <-ctx.Done():
	case err := <-serverErr:
		if err != nil {
			logger.Errorf(ctx, "HTTP server exited: %v", err)
		}
		cancel()
	}
	logger.Info("Shutting down server...")

	stopReaper()

	// Stop IM listeners (Lark WebSocket / WeChat iLink) BEFORE HTTP shutdown
	// so their goroutines unwind under our control. Without this, the SDK
	// read loops race with the implicit context cancel triggered by signal
	// handling and emit a misleading "[lark/sdk] receive message failed:
	// ... use of closed network connection" WARN on every restart even though
	// the close is fully expected. The shutdown-aware sdkLogger then sees
	// listener.stopped == true and demotes the disconnect log to Debug.
	h.StopIMListeners()

	// Shut down the HTTP server first so it stops accepting new requests and
	// drains in-flight ones within httpShutdownTimeout. This must happen
	// BEFORE StopAll: otherwise a request that lands in the StopAll race
	// window can register a fresh job goroutine right after StopAll snapshots
	// s.cancels, leaving it leaked through process exit. It also keeps the
	// container alive for any in-flight request still touching a sandbox
	// (stream flush, final write) — sandbox.Shutdown happens last.
	httpShutdownCtx, httpShutdownCancel := context.WithTimeout(context.Background(), httpShutdownTimeout)
	defer httpShutdownCancel()
	if err := s.Shutdown(httpShutdownCtx); err != nil {
		logger.Errorf(ctx, "Server shutdown error: %v", err)
	}
	if local != nil {
		if err := local.Shutdown(httpShutdownCtx); err != nil {
			logger.Errorf(ctx, "Local loopback server shutdown error: %v", err)
		}
	}

	// Stop all running jobs so their goroutines can save final state and exit cleanly.
	h.GetJobService().StopAll()

	// Flush usage-stats writes so the last debounced batch lands on disk
	// before exit. Best-effort: failures are logged inside the service.
	if us := h.GetUsageStats(); us != nil {
		us.Flush(ctx)
	}

	acpagent.CloseAllConns()

	// Tear down every per-workspace sandbox container with an independent,
	// much larger budget than HTTP shutdown. Sharing the same 5s ctx makes
	// later compose-down calls inherit an already-expired deadline and leak
	// containers on busy exits.
	sandboxShutdownCtx, sandboxShutdownCancel := context.WithTimeout(context.Background(), sandboxShutdownTimeout)
	defer sandboxShutdownCancel()
	sandbox.Shutdown(sandboxShutdownCtx)

	logger.Info("Server stopped")
}

func newServer(lc listenConfig, trustedProxies []*net.IPNet) *server.Hertz {
	// Route Hertz's own logs through pkg/logger so timestamps and levels match
	// the rest of the backend log. Default hlog emits `2026/05/07 17:19:17.873 ...
	// [Info] HERTZ: ...` which would interleave with our own
	// `2026-05-07 17:19:17 INFO ...` format and break `grep`.
	hlog.SetLogger(&hlogBridge{level: hlog.LevelInfo})
	// Suppress Hertz's per-route "[Debug] HERTZ: Method=... absolutePath=..."
	// registration lines (hundreds at boot) so the business log isn't diluted.
	// Info is still emitted, which preserves "Using network library=netpoll"
	// and "HTTP server listening on address=..." — the two startup signals we
	// want to keep visible.
	hlog.SetLevel(hlog.LevelInfo)

	opts := []config.Option{
		server.WithHostPorts(lc.addr),
		server.WithExitWaitTime(httpShutdownTimeout),
		server.WithIdleTimeout(30 * time.Minute),
		server.WithStreamBody(true),
		server.WithMaxRequestBodySize(maxRequestBodySize),
	}
	// WithTLS flips Hertz to the standard (net/http) transporter — netpoll has
	// no TLS support — and serves HTTPS only: the port will not accept plaintext
	// requests (matching the previous vite-on-443 behaviour).
	if lc.tlsCfg != nil {
		opts = append(opts, server.WithTLS(lc.tlsCfg))
	}
	h := server.Default(opts...)
	// Hertz trusts forwarding headers from every peer by default. Restrict them
	// so login throttling cannot be bypassed with a forged X-Forwarded-For.
	h.SetClientIPFunc(app.ClientIPWithOption(app.ClientIPOptions{
		RemoteIPHeaders: []string{"X-Forwarded-For", "X-Real-IP"},
		TrustedCIDRs:    trustedProxies,
	}))

	origins := corsOrigins()
	if len(origins) > 0 {
		h.Use(cors.New(cors.Config{
			AllowOrigins:     origins,
			AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowHeaders:     []string{"Content-Type", "X-Requested-With", auth.CSRFHeader},
			ExposeHeaders:    []string{"Content-Length"},
			AllowCredentials: true,
			MaxAge:           24 * time.Hour,
		}))
		logger.Infof(context.Background(), "[cors] cross-origin enabled for: %s", strings.Join(origins, ", "))
	} else {
		// Previous releases hard-coded AllowOrigins=["*"]; the default flipped
		// to same-origin only. Emit a one-line hint so operators upgrading a
		// deployment that serves the UI from a different origin can set
		// QUARTET_CORS_ORIGINS explicitly instead of debugging ACAO failures.
		logger.Infof(context.Background(), "[cors] same-origin only (set %s=<origin,...> to allow cross-origin; previous releases defaulted to '*')", consts.EnvKeyCORSOrigins)
	}

	// Compress buffered JSON API responses. The middleware itself checks the
	// response content type and skips body streams, so SSE keeps its immediate
	// flush behaviour and static/file responses remain unchanged. Register it
	// outside the logger so access logging still sees the original JSON body.
	h.Use(jsonGzipMiddleware())
	h.Use(loggerMiddleware())

	return h
}

func resolveTrustedProxies() ([]*net.IPNet, error) {
	value := strings.TrimSpace(os.Getenv(consts.EnvKeyTrustedProxies))
	if strings.EqualFold(value, "none") {
		return nil, nil
	}
	if value == "" {
		value = "127.0.0.0/8,::1/128"
	}

	items := strings.Split(value, ",")
	trusted := make([]*net.IPNet, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("trusted proxy entries cannot be empty")
		}
		if ip := net.ParseIP(item); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip = ip.To4()
				bits = 32
			}
			trusted = append(trusted, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, network, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("invalid IP or CIDR %q: %w", item, err)
		}
		trusted = append(trusted, network)
	}
	return trusted, nil
}
