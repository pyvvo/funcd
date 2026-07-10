package egress

import (
	"net/netip"
	"sync"

	"github.com/green-0-rabbit/funcd/internal/auth"
)

// MemoryWorkerIndex is the in-memory WorkerIndex (ADR-0117, §5) the containerd runtime populates at
// worker provisioning: on worker-up it records the assigned funcd0 IP → the worker's (namespace,
// function) Ref; on worker-down it removes it. The gateway reads it to authenticate the caller by source
// IP (never client-asserted). Concurrency-safe.
type MemoryWorkerIndex struct {
	mu sync.RWMutex
	m  map[netip.Addr]auth.EntityRef
}

// NewMemoryWorkerIndex returns an empty index.
func NewMemoryWorkerIndex() *MemoryWorkerIndex {
	return &MemoryWorkerIndex{m: map[netip.Addr]auth.EntityRef{}}
}

// Add records a worker's funcd0 source IP → its principal Ref (worker-up).
func (w *MemoryWorkerIndex) Add(ip netip.Addr, ref auth.EntityRef) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m[ip] = ref
}

// Remove drops a worker's IP mapping (worker-down); safe if absent.
func (w *MemoryWorkerIndex) Remove(ip netip.Addr) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.m, ip)
}

// Lookup resolves a source IP to its principal Ref; ok==false ⇒ unknown worker (the gateway denies+audits).
func (w *MemoryWorkerIndex) Lookup(ip netip.Addr) (auth.EntityRef, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	ref, ok := w.m[ip]
	return ref, ok
}
