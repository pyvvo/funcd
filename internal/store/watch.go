package store

import (
	"context"
	"strconv"
	"sync"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// subscriber is a registered in-process watcher. Events with rv > sinceRV that
// match gvk (+ optional namespace) are delivered on ch.
type subscriber struct {
	id        int
	gvk       v1.GroupVersionKind
	ns        v1.NamespaceName
	sinceRV   uint64
	ch        chan Event
	closeOnce sync.Once
}

func (sub *subscriber) stop() { sub.closeOnce.Do(func() { close(sub.ch) }) }

// recordedEvent is a ring entry: an event plus the revision at which it occurred.
type recordedEvent struct {
	rv uint64
	ev Event
}

// publish records an event in the replay ring and fans it out to matching
// subscribers. A subscriber whose buffer is full is dropped (its channel closed)
// so a slow watcher cannot stall writers — the caller observes a closed stream
// and re-lists. Called after the engine txn commits.
func (s *store) publish(rev uint64, ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rev > s.rev {
		s.rev = rev
	}
	s.ring = append(s.ring, recordedEvent{rv: rev, ev: ev})
	if len(s.ring) > s.ringCap {
		s.ring = append([]recordedEvent(nil), s.ring[len(s.ring)-s.ringCap:]...)
	}
	for id, sub := range s.subs {
		if !subMatches(sub, ev) || rev <= sub.sinceRV {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			sub.stop()
			delete(s.subs, id)
		}
	}
}

func subMatches(sub *subscriber, ev Event) bool {
	if sub.gvk != ev.Object.GroupVersionKind() {
		return false
	}
	if sub.ns != "" && ev.Object.GetNamespace() != sub.ns {
		return false
	}
	return true
}

func (s *store) removeSub(id int) {
	s.mu.Lock()
	delete(s.subs, id)
	s.mu.Unlock()
}

// Watch implements list-then-watch. Without SinceResourceVersion it snapshots the
// current matching set as Added then streams live; with one it replays the change
// ring from that revision then streams live. A resourceVersion older than the
// retained ring returns fault.Unavailable (the caller re-lists). ctx cancellation
// or Stop ends the watch promptly and leaks no goroutine.
func (s *store) Watch(ctx context.Context, gvk v1.GroupVersionKind, opts WatchOptions) (Watch, error) {
	if s.initErr != nil {
		return nil, s.initErr
	}
	sub := &subscriber{gvk: gvk, ns: opts.Namespace, ch: make(chan Event, watchChanBuf)}

	var initial []Event

	s.mu.Lock()
	startRV := s.rev
	sub.sinceRV = startRV

	if opts.SinceResourceVersion == "" {
		snap, err := s.snapshot(ctx, gvk, opts.Namespace, startRV)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		initial = snap
	} else {
		since, err := strconv.ParseUint(opts.SinceResourceVersion, 10, 64)
		if err != nil {
			s.mu.Unlock()
			return nil, fault.Invalidf("store.Watch", "invalid resourceVersion %q", opts.SinceResourceVersion)
		}
		if since < startRV {
			if len(s.ring) == 0 || s.ring[0].rv > since+1 {
				s.mu.Unlock()
				return nil, fault.Unavailablef("store.Watch", "resourceVersion %d is too old; re-list", since)
			}
			for _, re := range s.ring {
				if re.rv > since && subMatches(sub, re.ev) {
					initial = append(initial, re.ev)
				}
			}
		}
	}

	id := s.nextSubID
	s.nextSubID++
	sub.id = id
	s.subs[id] = sub
	s.mu.Unlock()

	w := &watch{store: s, sub: sub, result: make(chan Event), done: make(chan struct{})}
	go w.run(ctx, initial)
	return w, nil
}

// snapshot returns the current matching objects (with rv <= maxRV) as Added
// events. Objects created concurrently after the snapshot point (rv > maxRV) are
// skipped here and delivered on the live stream instead, so there are no dupes.
func (s *store) snapshot(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, maxRV uint64) ([]Event, error) {
	bucket := gvk.String()
	var evs []Event
	err := s.eng.View(ctx, func(tx Txn) error {
		return tx.Scan(bucket, func(_ string, val []byte) error {
			obj, derr := s.decode(ctx, gvk.Kind, val)
			if derr != nil {
				return derr
			}
			if ns != "" && obj.GetNamespace() != ns {
				return nil
			}
			rv, perr := strconv.ParseUint(obj.GetObjectMeta().ResourceVersion, 10, 64)
			if perr != nil {
				return fault.Internalf("store.snapshot", "corrupt resourceVersion: %v", perr)
			}
			if rv > maxRV {
				return nil
			}
			evs = append(evs, Event{Type: Added, Object: obj})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return evs, nil
}

// watch is the caller-facing handle returned by Watch.
type watch struct {
	store  *store
	sub    *subscriber
	result chan Event
	done   chan struct{}
	once   sync.Once
}

func (w *watch) ResultChan() <-chan Event { return w.result }

func (w *watch) Stop() {
	w.once.Do(func() {
		close(w.done)
		w.store.removeSub(w.sub.id)
	})
}

// run feeds the initial events then forwards the live stream until ctx is
// cancelled, Stop is called, or the subscriber is dropped. On exit it
// unregisters the subscriber and closes the result channel.
func (w *watch) run(ctx context.Context, initial []Event) {
	defer close(w.result)
	defer w.store.removeSub(w.sub.id)

	for _, ev := range initial {
		select {
		case w.result <- ev:
		case <-ctx.Done():
			return
		case <-w.done:
			return
		}
	}
	for {
		select {
		case ev, ok := <-w.sub.ch:
			if !ok {
				return
			}
			select {
			case w.result <- ev:
			case <-ctx.Done():
				return
			case <-w.done:
				return
			}
		case <-ctx.Done():
			return
		case <-w.done:
			return
		}
	}
}
