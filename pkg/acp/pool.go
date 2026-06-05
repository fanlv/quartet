package acp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
)

// trackedConns registers Conn instances so the idle reaper can close ones
// that have been idle for longer than connIdleTimeout.
var (
	trackedConns   []*Conn
	trackedConnsMu sync.Mutex
	poolClosing    bool
)

var errConnPoolClosing = errors.New("acp connection pool is shutting down")

func isConnPoolClosing() bool {
	trackedConnsMu.Lock()
	defer trackedConnsMu.Unlock()
	return poolClosing
}

// trackConn registers a connection for idle reaping.
func trackConn(c *Conn) error {
	trackedConnsMu.Lock()
	defer trackedConnsMu.Unlock()
	if poolClosing {
		return errConnPoolClosing
	}
	trackedConns = append(trackedConns, c)
	return nil
}

// CloseAllConns closes all tracked connections. Use during graceful shutdown.
func CloseAllConns() {
	trackedConnsMu.Lock()
	poolClosing = true
	toClose := trackedConns
	trackedConns = nil
	trackedConnsMu.Unlock()

	// Close outside trackedConnsMu: Conn.Close() terminates the ACP subprocess
	// and can block up to gracefulShutdownTimeout. Setting poolClosing before
	// releasing the lock prevents shutdown-time requests from registering a new
	// connection that would escape this close pass.
	for _, c := range toClose {
		c.Close()
	}
}

// StartIdleReaper launches a background goroutine that periodically scans
// tracked connections and closes ones that have been idle for longer than
// connIdleTimeout. This prevents unbounded accumulation of subprocess
// memory when sessions complete but the server keeps running.
//
// Call the returned stop function to terminate the reaper (e.g. during shutdown).
func StartIdleReaper() (stop func()) {
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		ticker := time.NewTicker(idleReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				reapIdleConns()
			}
		}
	}()
	return func() {
		close(stopCh)
		<-doneCh
	}
}

// reapIdleConns closes tracked connections that have not been used within
// connIdleTimeout. The lastUsedAt timestamp is refreshed on each user
// message (service Run → TouchLastUsed) and on every streaming session
// update from the ACP subprocess (sdkClient → onActivity), so active
// conversations remain safe even across long-running prompts.
func reapIdleConns() {
	now := time.Now()
	var toClose []*Conn

	trackedConnsMu.Lock()
	old := trackedConns
	alive := old[:0]
	for _, c := range old {
		if idle, reap := c.tryBeginIdleReap(now); reap {
			if idle > 0 {
				logger.Debugf(context.Background(), "[ACP] reaping idle Conn pid=%d idle=%s", c.Pid(), idle)
			}
			toClose = append(toClose, c)
			continue
		}
		alive = append(alive, c)
	}
	for i := len(alive); i < len(old); i++ {
		old[i] = nil
	}
	trackedConns = alive
	trackedConnsMu.Unlock()

	// Close outside trackedConnsMu: Conn.Close() terminates the ACP subprocess
	// and can block up to gracefulShutdownTimeout. Keeping it out of the global
	// pool lock prevents one slow process shutdown from delaying new connection
	// registration on the request path.
	for _, c := range toClose {
		c.Close()
	}
}
