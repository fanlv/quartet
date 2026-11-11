package handler

import (
	"fmt"
	"sync"
)

type agentExecutionGate struct {
	mu      sync.Mutex
	entries map[string]*agentExecutionEntry
}

type agentExecutionEntry struct {
	cond          *sync.Cond
	readers       int
	managementOps int
	deleting      bool
	maintenance   bool
}

func newAgentExecutionGate() *agentExecutionGate {
	return &agentExecutionGate{entries: make(map[string]*agentExecutionEntry)}
}

func (g *agentExecutionGate) entry(agentID string) *agentExecutionEntry {
	entry := g.entries[agentID]
	if entry == nil {
		entry = &agentExecutionEntry{}
		entry.cond = sync.NewCond(&g.mu)
		g.entries[agentID] = entry
	}
	return entry
}

func (g *agentExecutionGate) acquireExecution(agentID string) (func(), bool) {
	return g.acquire(agentID, false)
}

func (g *agentExecutionGate) acquireManagement(agentID string) (func(), bool) {
	return g.acquire(agentID, true)
}

func (g *agentExecutionGate) acquire(agentID string, management bool) (func(), bool) {
	if g == nil || agentID == "" {
		return func() {}, true
	}
	g.mu.Lock()
	entry := g.entry(agentID)
	if entry.deleting || entry.maintenance {
		g.mu.Unlock()
		return nil, false
	}
	entry.readers++
	if management {
		entry.managementOps++
	}
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			entry.readers--
			if management {
				entry.managementOps--
			}
			if entry.readers == 0 {
				entry.cond.Broadcast()
			}
			g.mu.Unlock()
		})
	}, true
}

func (g *agentExecutionGate) beginMaintenance(agentID string, force bool) (func(), error) {
	g.mu.Lock()
	entry := g.entry(agentID)
	if entry.managementOps > 0 {
		managementOps := entry.managementOps
		g.mu.Unlock()
		return nil, fmt.Errorf("Agent %q is busy (%d configuration or installation operations); finish them before switching accounts", agentID, managementOps)
	}
	if entry.deleting || entry.maintenance || entry.readers > 0 && !force {
		readers := entry.readers
		g.mu.Unlock()
		return nil, fmt.Errorf("Agent %q is busy (%d active execution, authentication or management operations); finish them before switching accounts", agentID, readers)
	}
	entry.maintenance = true
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() { g.mu.Lock(); entry.maintenance = false; entry.cond.Broadcast(); g.mu.Unlock() })
	}, nil
}

func (g *agentExecutionGate) beginDelete(agentID string) (func(bool), bool) {
	if g == nil || agentID == "" {
		return func(bool) {}, true
	}
	g.mu.Lock()
	entry := g.entry(agentID)
	if entry.deleting {
		// Management operations are serialized by the catalog transaction lock.
		// A deleting entry here therefore represents a retry/resume after an
		// earlier destructive cleanup failure, not a concurrent delete.
		g.mu.Unlock()
		return func(bool) {}, true
	}
	entry.deleting = true
	g.mu.Unlock()
	var once sync.Once
	return func(keepBlocked bool) {
		once.Do(func() {
			g.mu.Lock()
			entry.deleting = keepBlocked
			entry.cond.Broadcast()
			g.mu.Unlock()
		})
	}, true
}

func (g *agentExecutionGate) waitForExecutions(agentID string) {
	if g == nil || agentID == "" {
		return
	}
	g.mu.Lock()
	entry := g.entry(agentID)
	for entry.readers > 0 {
		entry.cond.Wait()
	}
	g.mu.Unlock()
}

func (g *agentExecutionGate) allow(agentID string) {
	if g == nil || agentID == "" {
		return
	}
	g.mu.Lock()
	entry := g.entry(agentID)
	entry.deleting = false
	entry.cond.Broadcast()
	g.mu.Unlock()
}
