package acp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type runtimeOperation struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// RuntimeMaintenance coordinates credential/configuration replacements with
// process startup, one-shot generation, transient probes and active RPC users.
// Idle processes are retired so the next use restores its native session in a
// new process.
var runtimeMaintenance = struct {
	sync.Mutex
	blocked map[string]bool
	running map[string]map[*runtimeOperation]struct{}
	conns   map[*Conn]bool
}{blocked: map[string]bool{}, running: map[string]map[*runtimeOperation]struct{}{}, conns: map[*Conn]bool{}}

// TrackRuntimeOperation registers work that depends on the current Agent
// credentials. A forced update cancels its context and waits for release.
func TrackRuntimeOperation(ctx context.Context, key string) (context.Context, func(), error) {
	runtimeMaintenance.Lock()
	defer runtimeMaintenance.Unlock()
	if runtimeMaintenance.blocked[key] {
		return nil, nil, fmt.Errorf("ACP runtime %q is updating credentials or configuration; retry after the update completes", key)
	}
	ctx, cancel := context.WithCancel(ctx)
	operation := &runtimeOperation{cancel: cancel, done: make(chan struct{})}
	if runtimeMaintenance.running[key] == nil {
		runtimeMaintenance.running[key] = make(map[*runtimeOperation]struct{})
	}
	runtimeMaintenance.running[key][operation] = struct{}{}
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			cancel()
			runtimeMaintenance.Lock()
			delete(runtimeMaintenance.running[key], operation)
			if len(runtimeMaintenance.running[key]) == 0 {
				delete(runtimeMaintenance.running, key)
			}
			close(operation.done)
			runtimeMaintenance.Unlock()
		})
	}, nil
}

// BeginRuntimeMaintenance blocks new process creation and RPC leases until the
// caller releases it. With force, active processes and operations are stopped
// before credentials can be replaced; otherwise a busy runtime is rejected.
func BeginRuntimeMaintenance(ctx context.Context, key string, force bool) (func(), error) {
	runtimeMaintenance.Lock()
	if runtimeMaintenance.blocked[key] || len(runtimeMaintenance.running[key]) > 0 && !force {
		runtimeMaintenance.Unlock()
		return nil, fmt.Errorf("ACP runtime %q is busy running or updating; retry after it completes", key)
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
	var operations []*runtimeOperation
	for operation := range runtimeMaintenance.running[key] {
		operation.cancel()
		operations = append(operations, operation)
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

	// A process may have started before maintenance acquired the lock. Its
	// cancellation and cleanup must finish before copying files.
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, operation := range operations {
		select {
		case <-operation.done:
		case <-waitCtx.Done():
			release()
			return nil, fmt.Errorf("wait for ACP runtime %q operation to stop: %w", key, waitCtx.Err())
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
