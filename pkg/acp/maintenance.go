package acp

import (
	"fmt"
	"sync"
)

// RuntimeMaintenance coordinates credential/configuration replacements with
// process startup, transient probes and active RPC users. Idle processes are
// retired so the next use restores its persisted native session in a new one.
var runtimeMaintenance = struct {
	sync.Mutex
	blocked map[string]bool
	opening map[string]int
	conns   map[*Conn]bool
}{blocked: map[string]bool{}, opening: map[string]int{}, conns: map[*Conn]bool{}}

func beginRuntimeCreation(key string) (func(), error) {
	runtimeMaintenance.Lock()
	defer runtimeMaintenance.Unlock()
	if runtimeMaintenance.blocked[key] {
		return nil, fmt.Errorf("ACP runtime %q is updating credentials or configuration; retry after the update completes", key)
	}
	runtimeMaintenance.opening[key]++
	return func() { runtimeMaintenance.Lock(); runtimeMaintenance.opening[key]--; runtimeMaintenance.Unlock() }, nil
}

// BeginRuntimeMaintenance is fail-fast: it never interrupts an active prompt,
// authentication attempt, probe or constructor. The caller must release it.
func BeginRuntimeMaintenance(key string) (func(), error) {
	runtimeMaintenance.Lock()
	if runtimeMaintenance.blocked[key] || runtimeMaintenance.opening[key] > 0 {
		runtimeMaintenance.Unlock()
		return nil, fmt.Errorf("ACP runtime %q is busy starting or updating; retry after it completes", key)
	}
	var idle []*Conn
	for c, transient := range runtimeMaintenance.conns {
		if c.runtimeKey != key || !c.isAlive() {
			continue
		}
		c.useMu.Lock()
		busy := transient || c.activeUses > 0
		c.useMu.Unlock()
		if busy {
			runtimeMaintenance.Unlock()
			return nil, fmt.Errorf("ACP runtime %q process %d is in use; finish its task, authentication or validation before switching", key, c.Pid())
		}
		idle = append(idle, c)
	}
	runtimeMaintenance.blocked[key] = true
	for _, c := range idle {
		c.MarkClosedByIdleReap()
	}
	runtimeMaintenance.Unlock()
	for _, c := range idle {
		c.Close()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			runtimeMaintenance.Lock()
			delete(runtimeMaintenance.blocked, key)
			runtimeMaintenance.Unlock()
		})
	}, nil
}
