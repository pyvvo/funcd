package eventstore

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing"
	"github.com/pyvvo/funcd/internal/kvstore"
)

// legacyPrefix holds the ADR-0157 seen lists in the KV that MigrateSeenLists moves. The move is a temporary
// workaround: it goes in the first minor release after the one that ships it (ADR-0201).
const legacyPrefix = "_eventing/blobwatch/"

// MigrateSeenLists moves the seen lists the KV holds under legacyPrefix into s (ADR-0201 Decision 6). A record with
// no bucket (an ADR-0119 Cursor) is skipped, and one s already holds keeps the newer list s has. Every key under the
// prefix is deleted only after every copy committed, so an error leaves the KV keys and the next start resumes.
func MigrateSeenLists(ctx context.Context, kv kvstore.KV, s *Store) (moved int, err error) {
	const op = "eventstore.MigrateSeenLists"
	keys, err := kv.List(ctx, legacyPrefix)
	if err != nil {
		return 0, fault.Wrapf(err, fault.KindOf(err), op, "seen-list move: list %s in the KV", legacyPrefix)
	}
	seen := s.SeenLists()
	for _, k := range keys {
		names := strings.SplitN(strings.TrimPrefix(k, legacyPrefix), "/", 3)
		if len(names) != 3 {
			continue
		}
		ns, source, event := v1.NamespaceName(names[0]), v1.ObjectName(names[1]), v1.ObjectName(names[2])
		raw, found, err := kv.Get(ctx, k)
		if err != nil {
			return moved, fault.Wrapf(err, fault.KindOf(err), op, "seen-list move: get %s", k)
		}
		if !found {
			continue
		}
		var rec eventing.SeenList
		if err := json.Unmarshal(raw, &rec); err != nil {
			return moved, fault.Invalidf(op, "seen-list move: decode %s: %v", k, err)
		}
		if rec.Bucket == "" {
			continue
		}
		cur, err := seen.Load(ctx, ns, source, event)
		if err != nil {
			return moved, fault.Wrapf(err, fault.KindOf(err), op, "seen-list move: load %s", k)
		}
		if cur.Bucket != "" {
			continue
		}
		if err := seen.Save(ctx, ns, source, event, rec); err != nil {
			return moved, fault.Wrapf(err, fault.KindOf(err), op, "seen-list move: save %s", k)
		}
		moved++
	}
	for _, k := range keys {
		if err := kv.Delete(ctx, k); err != nil {
			return moved, fault.Wrapf(err, fault.KindOf(err), op, "seen-list move: delete %s", k)
		}
	}
	return moved, nil
}
