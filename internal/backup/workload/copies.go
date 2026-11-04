package workload

import (
	"cmp"
	"context"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/config"
)

// uidHex is the length of a store UID, 16 random bytes in hex.
const uidHex = 32

// Limits bound one listing of a target and one copy: funcdconfig.yaml's workloadBackup keys.
type Limits struct {
	// CopyMembers is the most members one copy may hold: Resolve refuses a scope over it, and a listing that names more fails. A
	// scope's members are stores that exist in its namespace, named one by one or held by an App or a ResourceGroup, so a real
	// scope stays far below the default, where a Backup's record stays under 200 KiB.
	CopyMembers int
	// ListedKeys is the most keys one listing of a target reads. A copy holds a key per 8 MiB part, so the default holds 8 TiB of
	// copies.
	ListedKeys int
	// ListedKeyBytes is the most bytes of keys one listing of a target reads: the default is 256 bytes a key at the default
	// ListedKeys, where a copy's key takes about 100 bytes.
	ListedKeyBytes int64
}

// DefaultLimits are the workloadBackup keys' defaults.
func DefaultLimits() Limits {
	keyBytes, _ := v1.ByteSize(config.DefaultWorkloadBackupMaxListedKeySize).Bytes()
	return Limits{
		CopyMembers:    config.DefaultWorkloadBackupMaxCopyMembers,
		ListedKeys:     config.DefaultWorkloadBackupMaxListedKeys,
		ListedKeyBytes: keyBytes,
	}
}

// OrDefaults is l with each zero field at its default.
func (l Limits) OrDefaults() Limits {
	d := DefaultLimits()
	return Limits{
		CopyMembers:    cmp.Or(l.CopyMembers, d.CopyMembers),
		ListedKeys:     cmp.Or(l.ListedKeys, d.ListedKeys),
		ListedKeyBytes: cmp.Or(l.ListedKeyBytes, d.ListedKeyBytes),
	}
}

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

// ListCopy lists the Backup directory path, <ns>/<backup>-<uid>/ (ADR-0287 Decision 5), its members as kinds parse them.
func ListCopy(ctx context.Context, target blob.Bucket, path string, kinds []MemberKind, limits Limits) (Copy, error) {
	const op = "workload.ListCopy"
	c, ok := copyAt(path)
	if !ok {
		return Copy{}, fault.Invalidf(op, "%q is not a Backup directory <ns>/<backup>-<uid>/", path)
	}
	var items []blob.Attributes
	if err := listFrom(ctx, target, path, "", limits, func(a blob.Attributes) { items = append(items, a) }); err != nil {
		return Copy{}, fault.Wrapf(err, fault.KindOf(err), op, "list %q", path)
	}
	if err := c.fill(items, kinds, limits.CopyMembers); err != nil {
		return Copy{}, err
	}
	return c, nil
}

// SlotCopies lists the copies of schedule's slots after `after` under ns, whole or not, oldest first (ADR-0287
// Decision 3). The listing starts after every key of after's slot: keys sort by slot, as Unix minutes have 8 digits
// until 2160 (the bound of a schedule name's length). Directories of other schedules, Backups by hand and exports
// under the same prefix are skipped.
func SlotCopies(ctx context.Context, target blob.Bucket, ns v1.NamespaceName, schedule v1.ObjectName, after time.Time,
	kinds []MemberKind, limits Limits) ([]Copy, error) {
	const op = "workload.SlotCopies"
	prefix := string(ns) + "/" + string(schedule) + "-"
	start := prefix + strconv.FormatInt(after.Unix()/60, 10) + "~"
	var dirs []string
	items := map[string][]blob.Attributes{}
	skip := map[string]bool{}
	err := listFrom(ctx, target, prefix, start, limits, func(a blob.Attributes) {
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
		if err := c.fill(items[p], kinds, limits.CopyMembers); err != nil {
			return nil, err
		}
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
// Decision 4's layout), skipping a key no kind parses or that names no store; a Lake counts its last attempt only. Over
// maxMembers members it is fault.Invalid.
func (c *Copy) fill(items []blob.Attributes, kinds []MemberKind, maxMembers int) error {
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
		attempt, ok := 0, false
		for _, k := range kinds {
			if bm, attempt, ok = k.Parse(seg); ok {
				break
			}
		}
		if !ok || bm.Name.Validate() != nil || (bm.Volume != "" && bm.Volume.Validate() != nil) {
			continue
		}
		key := string(bm.Kind) + "/" + string(bm.Name) + "/" + string(bm.Volume)
		m := byKey[key]
		switch {
		case m == nil:
			if len(ms) == maxMembers {
				return fault.Invalidf("workload.fill", "%s lists more than %d members", c.Path, maxMembers)
			}
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
	return nil
}

// listFrom calls each for every object under prefix whose key sorts after `after`, in key order; over limits.ListedKeys keys or
// limits.ListedKeyBytes bytes of keys it is fault.Invalid.
func listFrom(ctx context.Context, b blob.Bucket, prefix, after string, limits Limits, each func(blob.Attributes)) error {
	listed, keyBytes := 0, int64(0)
	for {
		items, more, err := b.ListAfter(ctx, prefix, after, listPage)
		if err != nil {
			return err
		}
		listed += len(items)
		for _, it := range items {
			keyBytes += int64(len(it.Key))
		}
		if listed > limits.ListedKeys || keyBytes > limits.ListedKeyBytes {
			return fault.Invalidf("workload.listFrom", "it holds more than %d keys or %d bytes of keys", limits.ListedKeys, limits.ListedKeyBytes)
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
