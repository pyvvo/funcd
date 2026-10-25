package kv

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
)

// quarantineRoot is the KV substrate's quarantine (ADR-0288 Decision 2). Its first segment is no DNS label, so the
// stores and deletedStores never read it as a store.
const quarantineRoot = ".quarantine/"

// Quarantine is the KV stores' quarantine.Store and quarantine.Returner: entry <ns>/<store>/<since> holds its state
// at .quarantine/<ns>/<store>/<since>/state and the store's keys under …/<since>/data/.
type Quarantine struct{ r *Reconciler }

// Quarantine is the reconciler's quarantine.
func (r *Reconciler) Quarantine() Quarantine { return Quarantine{r: r} }

var (
	_ quarantine.Store    = Quarantine{}
	_ quarantine.Returner = Quarantine{}
)

func entryPrefix(ns v1.NamespaceName, name v1.ObjectName, since time.Time) string {
	return quarantineRoot + string(ns) + "/" + string(name) + "/" + quarantine.Stamp(since) + "/"
}

func storeRef(ns v1.NamespaceName, name v1.ObjectName) quarantine.Ref {
	return quarantine.Ref{ObjectRef: v1.ObjectRef{Kind: v1.KindKVStore, Namespace: ns, Name: name}}
}

// Move copies ref's keys into its moving entry, or a new one dated now, then drops the store's prefix and writes the
// entry kept (Decision 3): a crash before the drop leaves a moving entry, which the next start resumes.
func (q Quarantine) Move(ctx context.Context, ref quarantine.Ref, now time.Time) (quarantine.Entry, error) {
	const op = "services.kv.Quarantine.Move"
	if q.r.kv == nil {
		return quarantine.Entry{}, fault.Unavailablef(op, "no KV substrate")
	}
	entries, err := q.List(ctx)
	if err != nil {
		return quarantine.Entry{}, err
	}
	e := quarantine.Entry{Ref: ref, Since: now.UTC().Truncate(time.Millisecond), State: quarantine.Moving}
	for _, x := range entries {
		if x.Ref == ref && x.State == quarantine.Moving {
			e = x
		}
	}
	dst := entryPrefix(ref.Namespace, ref.Name, e.Since)
	if err := q.r.kv.Put(ctx, dst+"state", []byte(quarantine.Moving)); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.KindOf(err), op, "write the entry of %s", ref)
	}
	src := storePrefix(ref.Namespace, ref.Name)
	if err := q.copyKeys(ctx, src, dst+"data/"); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.KindOf(err), op, "copy %s", ref)
	}
	if err := q.r.kv.DropPrefix(src); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.KindOf(err), op, "drop the prefix of %s", ref)
	}
	if err := q.r.kv.Put(ctx, dst+"state", []byte(quarantine.Kept)); err != nil {
		return quarantine.Entry{}, fault.Wrapf(err, fault.KindOf(err), op, "write the entry of %s", ref)
	}
	e.State = quarantine.Kept
	return e, nil
}

// settle writes kept each moving entry with nothing left to move (Decision 3): its source is empty (a crash after the
// drop), or a KVStore of the name exists again, whose keys not yet moved stay with it. An entry whose source still
// holds keys and whose store is not found stays moving: the next start resumes it.
func (q Quarantine) settle(ctx context.Context) error {
	entries, err := q.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if e.State != quarantine.Moving {
			continue
		}
		keys, err := q.r.kv.List(ctx, storePrefix(e.Namespace, e.Name))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(keys) > 0 {
			_, gerr := q.r.store.Get(ctx, v1.KindKVStore.GVK(), e.Namespace, e.Name)
			if gerr != nil {
				if fault.KindOf(gerr) != fault.NotFound {
					errs = append(errs, gerr)
				}
				continue
			}
		}
		errs = append(errs, q.r.kv.Put(ctx, entryPrefix(e.Namespace, e.Name, e.Since)+"state", []byte(quarantine.Kept)))
	}
	return errors.Join(errs...)
}

// List returns every entry under .quarantine/, its state read from its state key; one with data and no state reads
// kept.
func (q Quarantine) List(ctx context.Context) ([]quarantine.Entry, error) {
	const op = "services.kv.Quarantine.List"
	if q.r.kv == nil {
		return nil, nil
	}
	keys, err := q.r.kv.List(ctx, quarantineRoot)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the quarantine")
	}
	var out []quarantine.Entry
	seen := map[string]bool{}
	for _, k := range keys {
		parts := strings.SplitN(strings.TrimPrefix(k, quarantineRoot), "/", 4)
		if len(parts) < 4 {
			continue
		}
		ns, name := v1.NamespaceName(parts[0]), v1.ObjectName(parts[1])
		since, err := quarantine.ParseStamp(parts[2])
		if err != nil || ns.Validate() != nil || name.Validate() != nil {
			continue
		}
		prefix := entryPrefix(ns, name, since)
		if seen[prefix] {
			continue
		}
		seen[prefix] = true
		st, err := q.state(ctx, prefix)
		if err != nil {
			return nil, err
		}
		out = append(out, quarantine.Entry{Ref: storeRef(ns, name), Since: since, State: st})
	}
	return out, nil
}

func (q Quarantine) state(ctx context.Context, prefix string) (quarantine.State, error) {
	v, _, err := q.r.kv.Get(ctx, prefix+"state")
	if err != nil {
		return "", fault.Wrapf(err, fault.KindOf(err), "services.kv.Quarantine", "read the state of %s", prefix)
	}
	return quarantine.ParseState(string(v))
}

// Expire drops every entry Expired at cutoff.
func (q Quarantine) Expire(ctx context.Context, cutoff time.Time) ([]quarantine.Entry, error) {
	entries, err := q.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []quarantine.Entry
	var errs []error
	for _, e := range entries {
		if !e.Expired(cutoff) {
			continue
		}
		if err := q.r.kv.DropPrefix(entryPrefix(e.Namespace, e.Name, e.Since)); err != nil {
			errs = append(errs, fault.Wrapf(err, fault.KindOf(err), "services.kv.Quarantine.Expire", "drop the entry of %s", e.Ref))
			continue
		}
		out = append(out, e)
	}
	return out, errors.Join(errs...)
}

// Return copies ref's entry back into the store's prefix, then drops the entry (Decision 6). A kept entry needs an
// empty store; a returning one resumes.
func (q Quarantine) Return(ctx context.Context, ref quarantine.Ref, since time.Time) error {
	const op = "services.kv.Quarantine.Return"
	if q.r.kv == nil {
		return fault.Unavailablef(op, "no KV substrate")
	}
	entries, err := q.List(ctx)
	if err != nil {
		return err
	}
	e, err := quarantine.Pick(entries, ref, since)
	if err != nil {
		return err
	}
	src, dst := entryPrefix(e.Namespace, e.Name, e.Since), storePrefix(ref.Namespace, ref.Name)
	if e.State == quarantine.Kept {
		keys, err := q.r.kv.List(ctx, dst)
		if err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "list %s", ref)
		}
		if len(keys) > 0 {
			return fault.Conflictf(op, "%s holds %d keys: the quarantine returns only into an empty store", ref, len(keys))
		}
		if err := q.r.kv.Put(ctx, src+"state", []byte(quarantine.Returning)); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "write the entry of %s", ref)
		}
	}
	if err := q.copyKeys(ctx, src+"data/", dst); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "copy back %s", ref)
	}
	if err := q.r.kv.DropPrefix(src); err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "drop the entry of %s", ref)
	}
	return nil
}

// copyKeys puts under dst every key under src, its suffix kept.
func (q Quarantine) copyKeys(ctx context.Context, src, dst string) error {
	keys, err := q.r.kv.List(ctx, src)
	if err != nil {
		return err
	}
	for _, k := range keys {
		v, found, err := q.r.kv.Get(ctx, k)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if err := q.r.kv.Put(ctx, dst+strings.TrimPrefix(k, src), v); err != nil {
			return err
		}
	}
	return nil
}
