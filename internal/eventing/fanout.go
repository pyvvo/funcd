package eventing

import (
	"context"
	"sync"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Fanout is the in-process Publisher driver (ADR-0108): a fired CloudEvent is delivered to every live
// subscriber whose (namespace, source, event) key matches it. Routing derives that key from the envelope
// — ParseSourceURI(ev.Source) + ev.Type — the inverse of NewNamedEvent, so the URI is the single
// canonical name↔key mapping. The F69 Sensor subscribes; a firing to no subscriber is a no-op. Delivery
// is non-durable (a fire while a subscriber is down is lost); a bus-backed Publisher is a V2 swap.
type Fanout struct {
	mu   sync.RWMutex
	subs map[eventKey]map[int]func(context.Context, CloudEvent)
	next int
}

// NewFanout builds an empty in-process fanout publisher.
func NewFanout() *Fanout {
	return &Fanout{subs: map[eventKey]map[int]func(context.Context, CloudEvent){}}
}

// Publish delivers ev to every subscriber of its derived (ns, source, event) key. A source URI that is
// not a funcd EventSource URI is dropped (never an error) — the seam only carries funcd named events.
func (f *Fanout) Publish(ctx context.Context, ev CloudEvent) error {
	ns, source, ok := ParseSourceURI(ev.Source)
	if !ok {
		return nil
	}
	k := eventKey{ns: ns, source: source, event: v1.ObjectName(ev.Type)}
	f.mu.RLock()
	fns := make([]func(context.Context, CloudEvent), 0, len(f.subs[k]))
	for _, fn := range f.subs[k] {
		fns = append(fns, fn)
	}
	f.mu.RUnlock()
	for _, fn := range fns { // deliver outside the lock so a slow subscriber can't block publishers
		fn(ctx, ev)
	}
	return nil
}

// Subscribe registers fn for the named event (ns, source, event); the returned cancel deregisters it.
func (f *Fanout) Subscribe(ns v1.NamespaceName, source, event v1.ObjectName, fn func(context.Context, CloudEvent)) (cancel func()) {
	k := eventKey{ns: ns, source: source, event: event}
	f.mu.Lock()
	id := f.next
	f.next++
	if f.subs[k] == nil {
		f.subs[k] = map[int]func(context.Context, CloudEvent){}
	}
	f.subs[k][id] = fn
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.subs[k], id)
		if len(f.subs[k]) == 0 {
			delete(f.subs, k)
		}
		f.mu.Unlock()
	}
}
