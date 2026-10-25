package workload

import (
	"context"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
)

// uidHex is the length of a store UID, 16 random bytes in hex.
const uidHex = 32

// Copy is one Backup's directory on a target as a listing shows it, read with list rights only (ADR-0287): it is
// whole once its manifest.yaml, written last, is there; Completed is that manifest's modification time. Members are
// the kind directories the listing holds, for display: a Restore matches the manifest's members.
type Copy struct {
	Name      v1.ObjectName
	UID       v1.UID
	Path      string // <ns>/<backup>-<uid>/
	Whole     bool
	Completed time.Time
	Members   []v1.BackupMember
}

// ListCopy lists the Backup directory path, <ns>/<backup>-<uid>/ (ADR-0287 Decision 5).
func ListCopy(ctx context.Context, target blob.Bucket, path string) (Copy, error) {
	const op = "workload.ListCopy"
	c, ok := copyAt(path)
	if !ok {
		return Copy{}, fault.Invalidf(op, "%q is not a Backup directory <ns>/<backup>-<uid>/", path)
	}
	var items []blob.Attributes
	if err := listFrom(ctx, target, path, "", func(a blob.Attributes) { items = append(items, a) }); err != nil {
		return Copy{}, fault.Wrapf(err, fault.KindOf(err), op, "list %q", path)
	}
	c.fill(items)
	return c, nil
}

// SlotCopies lists the copies of schedule's slots after `after` under ns, whole or not, oldest first (ADR-0287
// Decision 3). The listing starts after every key of after's slot: keys sort by slot, as Unix minutes have 8 digits
// until 2160 (the bound of a schedule name's length). Directories of other schedules, Backups by hand and exports
// under the same prefix are skipped.
func SlotCopies(ctx context.Context, target blob.Bucket, ns v1.NamespaceName, schedule v1.ObjectName, after time.Time) ([]Copy, error) {
	const op = "workload.SlotCopies"
	prefix := string(ns) + "/" + string(schedule) + "-"
	start := prefix + strconv.FormatInt(after.Unix()/60, 10) + "~"
	var dirs []string
	items := map[string][]blob.Attributes{}
	skip := map[string]bool{}
	err := listFrom(ctx, target, prefix, start, func(a blob.Attributes) {
		dir, _, ok := strings.Cut(strings.TrimPrefix(a.Key, string(ns)+"/"), "/")
		if !ok {
			return
		}
		p := string(ns) + "/" + dir + "/"
		if skip[p] {
			return
		}
		if _, seen := items[p]; !seen {
			c, ok := copyAt(p)
			slot, isSlot := v1.ParseBackupSlotName(schedule, c.Name)
			if !ok || !isSlot || !slot.After(after) {
				skip[p] = true
				return
			}
			dirs = append(dirs, p)
		}
		items[p] = append(items[p], a)
	})
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list %q", prefix)
	}
	out := make([]Copy, 0, len(dirs))
	for _, p := range dirs {
		c, _ := copyAt(p)
		c.fill(items[p])
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Copy) int {
		sa, _ := v1.ParseBackupSlotName(schedule, a.Name)
		sb, _ := v1.ParseBackupSlotName(schedule, b.Name)
		return sa.Compare(sb)
	})
	return out, nil
}

// copyAt splits a directory <ns>/<backup>-<uid>/ into its Backup name and UID.
func copyAt(p string) (Copy, bool) {
	dir := path.Base(strings.TrimSuffix(p, "/"))
	if !strings.HasSuffix(p, "/") || len(dir) < uidHex+2 || dir[len(dir)-uidHex-1] != '-' {
		return Copy{}, false
	}
	uid := dir[len(dir)-uidHex:]
	if strings.Trim(uid, "0123456789abcdef") != "" {
		return Copy{}, false
	}
	return Copy{Name: v1.ObjectName(dir[:len(dir)-uidHex-1]), UID: v1.UID(uid), Path: p}, true
}

// fill reads the directory's listing: the manifest, and each member's bytes under its kind directory (ADR-0222
// Decision 4's layout); a Lake counts its last attempt only.
func (c *Copy) fill(items []blob.Attributes) {
	type member struct {
		v1.BackupMember
		attempt int
	}
	var ms []*member
	byKey := map[string]*member{}
	for _, a := range items {
		rel := strings.TrimPrefix(a.Key, c.Path)
		if rel == manifestName {
			c.Whole, c.Completed = true, a.ModTime
			continue
		}
		seg := strings.Split(rel, "/")
		var bm v1.BackupMember
		attempt := 0
		switch {
		case len(seg) >= 3 && seg[0] == "kv":
			bm = v1.BackupMember{Kind: v1.KindKVStore, Name: v1.ObjectName(seg[1])}
		case len(seg) >= 3 && seg[0] == "blob":
			bm = v1.BackupMember{Kind: v1.KindBucket, Name: v1.ObjectName(seg[1])}
		case len(seg) >= 3 && seg[0] == "sql":
			bm = v1.BackupMember{Kind: v1.KindSQLDatabase, Name: v1.ObjectName(seg[1])}
		case len(seg) >= 4 && seg[0] == "volume":
			bm = v1.BackupMember{Kind: v1.KindFunction, Name: v1.ObjectName(seg[1]), Volume: v1.ObjectName(seg[2])}
		case len(seg) >= 4 && seg[0] == "lake":
			n, err := strconv.Atoi(seg[2])
			if err != nil {
				continue
			}
			bm, attempt = v1.BackupMember{Kind: v1.KindLake, Name: v1.ObjectName(seg[1])}, n
		default:
			continue
		}
		key := string(bm.Kind) + "/" + string(bm.Name) + "/" + string(bm.Volume)
		m := byKey[key]
		switch {
		case m == nil:
			m = &member{BackupMember: bm, attempt: attempt}
			byKey[key] = m
			ms = append(ms, m)
		case attempt > m.attempt:
			m.Bytes, m.attempt = 0, attempt
		case attempt < m.attempt:
			continue
		}
		m.Bytes += a.Size
	}
	c.Members = nil
	for _, m := range ms {
		c.Members = append(c.Members, m.BackupMember)
	}
}

// listFrom calls each for every object under prefix whose key sorts after `after`, in key order.
func listFrom(ctx context.Context, b blob.Bucket, prefix, after string, each func(blob.Attributes)) error {
	for {
		items, more, err := b.ListAfter(ctx, prefix, after, listPage)
		if err != nil {
			return err
		}
		for _, it := range items {
			each(it)
		}
		if !more || len(items) == 0 {
			return nil
		}
		after = items[len(items)-1].Key
	}
}
