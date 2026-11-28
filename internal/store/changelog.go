package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The change log that worker nodes follow (ADR-0365 Decision 3): one record per object write, at the write's revision
// and in its transaction, so a node that follows it after a restart of either side misses nothing. A kind encrypted at
// rest keeps its record sealed by the same encryptor.
const (
	changeBucket          = "\x00node-changes"
	changeFloorKey        = "changes-floor"
	defaultChangeRetain   = 100_000
	changeCompactInterval = 1024
)

// Change is one change-log record. Object is the object as stored, in JSON, a Deleted record's included; Origin names
// the worker node that wrote it, empty for a write of the control plane's own.
type Change struct {
	Rev       uint64           `json:"rev"`
	Type      EventType        `json:"type"`
	Kind      v1.Kind          `json:"kind"`
	Namespace v1.NamespaceName `json:"namespace,omitempty"`
	Name      v1.ObjectName    `json:"name"`
	Object    json.RawMessage  `json:"object,omitempty"`
	Sealed    []byte           `json:"sealed,omitempty"`
	Origin    v1.ObjectName    `json:"origin,omitempty"`
}

// ChangeLog reads the change log. Changes returns the records after since that keep accepts, oldest first, at most limit,
// and the revision to resume after; a since below the compaction floor is fault.PreconditionFailed, and the follower
// starts again from a snapshot. Head is the store-wide revision. ChangeSignal is closed at the next record. Timeline is
// the store's timeline (ADR-0202): a store loaded from a backup has a new one, so a revision of another timeline is
// from before the restore, and its follower starts again from a snapshot too.
type ChangeLog interface {
	Changes(ctx context.Context, since uint64, limit int, keep func(Change) bool) ([]Change, uint64, error)
	Head(ctx context.Context) (uint64, error)
	ChangeSignal() <-chan struct{}
	Timeline() string
}

// WithChangeLog makes the store keep the change log, holding the last retain records; retain <= 0 ⇒ 100 000.
func WithChangeLog(retain int) Option {
	return func(c *config) error {
		if retain <= 0 {
			retain = defaultChangeRetain
		}
		c.changeRetain = retain
		return nil
	}
}

type (
	noChangeKey struct{}
	originKey   struct{}
	copyKey     struct{}
)

// WithoutChangeLog marks a write the change log skips: a node's status write-back, which no node needs back.
func WithoutChangeLog(ctx context.Context) context.Context {
	return context.WithValue(ctx, noChangeKey{}, true)
}

// WithOrigin marks the writes made for worker node node, so the feed does not send them back to it.
func WithOrigin(ctx context.Context, node v1.ObjectName) context.Context {
	return context.WithValue(ctx, originKey{}, node)
}

// AsCopy marks the writes of an object that another store holds (FEAT-0002): a node's copy of the control plane's, or
// the control plane's copy of a node's. The copy keeps the object's uid, generation and creation time, so it is the same
// object on both sides, and its name is never generated anew. An object with no uid gets its own, as any write's does.
func AsCopy(ctx context.Context) context.Context { return context.WithValue(ctx, copyKey{}, true) }

// copied reports whether ctx's write keeps the identity meta carries.
func copied(ctx context.Context, meta *v1.ObjectMeta) bool {
	return ctx.Value(copyKey{}) != nil && meta.UID != ""
}

// putChange writes obj's record at rev in tx and reports whether it wrote one.
func (s *store) putChange(ctx context.Context, tx Txn, rev uint64, typ EventType, obj v1.Object) (bool, error) {
	const op = "store.putChange"
	if s.changeRetain == 0 || ctx.Value(noChangeKey{}) != nil {
		return false, nil
	}
	kind := obj.GroupVersionKind().Kind
	meta := obj.GetObjectMeta()
	c := Change{Rev: rev, Type: typ, Kind: kind, Namespace: meta.Namespace, Name: meta.Name}
	if origin, ok := ctx.Value(originKey{}).(v1.ObjectName); ok {
		c.Origin = origin
	}
	raw, err := s.encode(ctx, kind, obj)
	if err != nil {
		return false, err
	}
	if _, sealed := s.enc[kind]; sealed {
		c.Sealed = raw
	} else {
		c.Object = raw
	}
	rec, err := json.Marshal(c)
	if err != nil {
		return false, fault.Wrapf(err, fault.Internal, op, "marshal the record")
	}
	return true, tx.Put(changeBucket, changeKey(rev), rec)
}

func changeKey(rev uint64) string { return fmt.Sprintf("%020d", rev) }

// errPastFloor ends a compaction's scan at the first record it keeps: the records come in revision order.
var errPastFloor = errors.New("past the change log's floor")

// afterChange wakes the followers, and drops the records older than the retained window every changeCompactInterval
// revisions. A failed compaction leaves the records for the next one.
func (s *store) afterChange(ctx context.Context, rev uint64) {
	s.signalChange()
	if rev%changeCompactInterval != 0 || rev <= uint64(s.changeRetain) {
		return
	}
	floor := rev - uint64(s.changeRetain)
	_ = s.eng.Update(context.WithoutCancel(ctx), func(tx Txn) error {
		var old []string
		if err := tx.ScanFrom(changeBucket, "", func(key string, _ []byte) error {
			n, perr := strconv.ParseUint(key, 10, 64)
			switch {
			case perr != nil:
			case n > floor:
				return errPastFloor
			default:
				old = append(old, key)
			}
			return nil
		}); err != nil && !errors.Is(err, errPastFloor) {
			return err
		}
		for _, key := range old {
			if err := tx.Delete(changeBucket, key); err != nil {
				return err
			}
		}
		return tx.Put(metaBucket, changeFloorKey, []byte(strconv.FormatUint(floor, 10)))
	})
}

func (s *store) Timeline() string { return s.timeline }

func (s *store) Head(ctx context.Context) (uint64, error) {
	var head uint64
	err := s.eng.View(ctx, func(tx Txn) error {
		r, err := readRevision(tx)
		head = r
		return err
	})
	return head, err
}

func (s *store) Changes(ctx context.Context, since uint64, limit int, keep func(Change) bool) ([]Change, uint64, error) {
	const op = "store.Changes"
	var (
		all   []Change
		head  uint64
		floor uint64
	)
	err := s.eng.View(ctx, func(tx Txn) error {
		r, err := readRevision(tx)
		if err != nil {
			return err
		}
		head = r
		if raw, found, ferr := tx.Get(metaBucket, changeFloorKey); ferr != nil {
			return ferr
		} else if found {
			if floor, err = strconv.ParseUint(string(raw), 10, 64); err != nil {
				return fault.Wrapf(err, fault.Internal, op, "corrupt change floor")
			}
		}
		if since < floor {
			return nil
		}
		return tx.ScanFrom(changeBucket, changeKey(since+1), func(key string, val []byte) error {
			n, perr := strconv.ParseUint(key, 10, 64)
			if perr != nil || n <= since {
				return nil
			}
			var c Change
			if uerr := json.Unmarshal(val, &c); uerr != nil {
				return fault.Wrapf(uerr, fault.Internal, op, "decode record %d", n)
			}
			all = append(all, c)
			return nil
		})
	})
	if err != nil {
		return nil, 0, err
	}
	if since < floor {
		return nil, 0, fault.PreconditionFailedf(op, "revision %d is older than the change log, which starts after %d", since, floor)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Rev < all[j].Rev })
	var out []Change
	for _, c := range all {
		if !keep(c) {
			continue
		}
		if limit > 0 && len(out) == limit {
			return out, out[len(out)-1].Rev, nil
		}
		if c.Sealed != nil {
			obj, derr := s.decode(ctx, c.Kind, c.Sealed)
			if derr != nil {
				return nil, 0, derr
			}
			raw, merr := json.Marshal(obj)
			if merr != nil {
				return nil, 0, fault.Wrapf(merr, fault.Internal, op, "marshal %s", c.Kind)
			}
			c.Object, c.Sealed = raw, nil
		}
		out = append(out, c)
	}
	return out, head, nil
}

func (s *store) ChangeSignal() <-chan struct{} {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	if s.changeCh == nil {
		s.changeCh = make(chan struct{})
	}
	return s.changeCh
}

func (s *store) signalChange() {
	s.changeMu.Lock()
	defer s.changeMu.Unlock()
	if s.changeCh != nil {
		close(s.changeCh)
		s.changeCh = nil
	}
}
