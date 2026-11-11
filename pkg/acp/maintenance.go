package acp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type runtimeCreation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// RuntimeMaintenance coordinates credential/configuration replacements with
// process startup, transient probes and active RPC users. Idle processes are
// retired so the next use restores its persisted native session in a new one.
var runtimeMaintenance = struct {
	sync.Mutex
	blocked map[string]bool
	opening map[string]map[*runtimeCreation]struct{}
	conns   map[*Conn]bool
}{blocked: map[string]bool{}, opening: map[string]map[*runtimeCreation]struct{}{}, conns: map[*Conn]bool{}}

func beginRuntimeCreation(ctx context.Context, key string) (context.Context, func(), error) {
	runtimeMaintenance.Lock()
	defer runtimeMaintenance.Unlock()
	if runtimeMaintenance.blocked[key] {
		return nil, nil, fmt.Errorf("ACP runtime %q is updating credentials or configuration; retry after the update completes", key)
	}
	ctx, cancel := context.WithCancel(ctx)
	creation := &runtimeCreation{cancel: cancel, done: make(chan struct{})}
	if runtimeMaintenance.opening[key] == nil {
		runtimeMaintenance.opening[key] = make(map[*runtimeCreation]struct{})
	}
	runtimeMaintenance.opening[key][creation] = struct{}{}
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			runtimeMaintenance.Lock()
			delete(runtimeMaintenance.opening[key], creation)
			if len(runtimeMaintenance.opening[key]) == 0 {
				delete(runtimeMaintenance.opening, key)
			}
			close(creation.done)
			runtimeMaintenance.Unlock()
		})
	}, nil
}

// BeginRuntimeMaintenance blocks new process creation and RPC leases until the
// caller releases it. With force, active processes and constructors are stopped
// before credentials can be replaced; otherwise a busy runtime is rejected.
func BeginRuntimeMaintenance(ctx context.Context, key string, force bool) (func(), error) {
	runtimeMaintenance.Lock()
	if runtimeMaintenance.blocked[key] || len(runtimeMaintenance.opening[key]) > 0 && !force {
		runtimeMaintenance.Unlock()
		return nil, fmt.Errorf("ACP runtime %q is busy starting or updating; retry after it completes", key)
	}
	var conns []*Conn
	for c, transient := range runtimeMaintenance.conns {
		if c.runtimeKey != key || !c.isAlive() {
			continue
		}
		c.useMu.Lock()
		busy := transient || c.activeUses > 0
		c.useMu.Unlock()
		if busy && !force {
			runtimeMaintenance.Unlock()
			return nil, fmt.Errorf("ACP runtime %q process %d is in use; finish its task, authentication or validation before switching", key, c.Pid())
		}
		conns = append(conns, c)
	}
	runtimeMaintenance.blocked[key] = true
	var creations []*runtimeCreation
	for creation := range runtimeMaintenance.opening[key] {
		creation.cancel()
		creations = append(creations, creation)
	}
	for _, c := range conns {
		c.MarkClosedByIdleReap()
	}
	runtimeMaintenance.Unlock()
	var once sync.Once
	release := func() {
		once.Do(func() {
			runtimeMaintenance.Lock()
			delete(runtimeMaintenance.blocked, key)
			runtimeMaintenance.Unlock()
		})
	}
	var closing sync.WaitGroup
	for _, c := range conns {
		closing.Go(c.Close)
	}
	closing.Wait()

	// Constructors may have started a process before maintenance acquired the
	// lock. Their cancellation and cleanup must finish before copying files.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, creation := range creations {
		select {
		case <-creation.done:
		case <-waitCtx.Done():
			release()
			return nil, fmt.Errorf("wait for ACP runtime %q process startup to stop: %w", key, waitCtx.Err())
		}
	}
	var stopErrors []error
	for _, c := range conns {
		if c.isAlive() {
			stopErrors = append(stopErrors, fmt.Errorf("ACP runtime %q process %d is still running after shutdown", key, c.Pid()))
		}
	}
	if err := errors.Join(stopErrors...); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
