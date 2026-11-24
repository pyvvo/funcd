package egress

import (
	"net/netip"
	"sync"

	"github.com/pyvvo/funcd/internal/auth"
)

// MemoryWorkerIndex is the in-memory WorkerIndex (ADR-0117, §5) the containerd runtime populates at
// worker provisioning: on worker-up it records the assigned funcd0 IP → the worker's principal Ref (its
// owner kind); on worker-down it removes it. The gateway reads it to authenticate the caller by source
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

// Replace makes the index hold exactly m, as one change: the add-on engines' addresses are taken whole from each
// look at the runtime (ADR-0332 Decision 3).
func (w *MemoryWorkerIndex) Replace(m map[netip.Addr]auth.EntityRef) {
	next := make(map[netip.Addr]auth.EntityRef, len(m))
	for ip, ref := range m {
		next[ip] = ref
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m = next
}

// Sources is the one view through which the gateway and the forwarder name a source address (ADR-0352 Decision 3): the
// worker index's entry, else the engine index's. ok is false when neither holds ip, or when both hold it with different
// principals, which is refused as an unknown source is. A nil index holds nothing.
func Sources(workers, engines WorkerIndex) WorkerIndex {
	return sources{workers: workers, engines: engines}
}

type sources struct{ workers, engines WorkerIndex }

func (s sources) Lookup(ip netip.Addr) (auth.EntityRef, bool) {
	w, inWorkers := lookup(s.workers, ip)
	e, inEngines := lookup(s.engines, ip)
	switch {
	case inWorkers && inEngines:
		if w != e {
			return auth.EntityRef{}, false
		}
		return w, true
	case inEngines:
		return e, true
	}
	return w, inWorkers
}

func lookup(idx WorkerIndex, ip netip.Addr) (auth.EntityRef, bool) {
	if idx == nil {
		return auth.EntityRef{}, false
	}
	return idx.Lookup(ip)
}
