// Package restore reads the platform backup generations of ADR-0203 back (ADR-0206): it lists them as a lineage,
// resolves a restore point, restores one offline into empty directories and leaves the platform held, and inspects
// one into memory engines.
package restore

import (
	"context"

	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/version"
)

// The states of a listed generation (Decision 3).
const (
	StateComplete   = "complete"
	StateIncomplete = "incomplete"
	StateAbandoned  = "abandoned"
	StateNewer      = "newer"
)

// Generation is one listed generation: its manifest (only Generation and Timeline while incomplete), its class and
// its state: incomplete without a manifest, abandoned on a dead branch (backup.Abandoned), newer when this funcd
// cannot read it (format or version), else complete.
type Generation struct {
	Manifest backup.Manifest
	Class    backup.Class
	State    string
}

// Ref is the generation's (timeline, n).
func (g Generation) Ref() backup.GenRef {
	return backup.GenRef{Timeline: g.Manifest.Timeline, Generation: g.Manifest.Generation}
}

// List lists src once and reads each complete generation's manifest, in backup.List's order.
func List(ctx context.Context, src blob.Bucket) ([]Generation, error) {
	entries, err := backup.List(ctx, src)
	if err != nil {
		return nil, err
	}
	gens := make([]Generation, 0, len(entries))
	var ms []backup.Manifest
	for _, e := range entries {
		g := Generation{Class: e.Class, State: StateIncomplete,
			Manifest: backup.Manifest{Generation: e.Generation, Timeline: e.Timeline}}
		if e.Complete {
			if g.Manifest, err = backup.ReadManifest(ctx, src, e); err != nil {
				return nil, err
			}
			g.State = StateComplete
			ms = append(ms, g.Manifest)
		}
		gens = append(gens, g)
	}
	abandoned := backup.Abandoned(ms)
	for i := range gens {
		g := &gens[i]
		switch {
		case g.State != StateComplete:
		case abandoned[g.Ref()]:
			g.State = StateAbandoned
		case g.Manifest.Format > backup.Format || CheckVersion(g.Manifest.Funcd, version.Version) != nil:
			g.State = StateNewer
		}
	}
	return gens, nil
}

// parents maps each timeline to the generation its restore loaded, from any of its manifests.
func parents(gens []Generation) map[string]*backup.GenRef {
	out := map[string]*backup.GenRef{}
	for _, g := range gens {
		if p := g.Manifest.Parent; p != nil {
			out[g.Manifest.Timeline] = p
		}
	}
	return out
}

// descends reports whether timeline a is b or follows from it by parent.
func descends(par map[string]*backup.GenRef, a, b string) bool {
	seen := map[string]bool{}
	for a != "" && !seen[a] {
		if a == b {
			return true
		}
		seen[a] = true
		p := par[a]
		if p == nil {
			return false
		}
		a = p.Timeline
	}
	return false
}
