package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	kvbadger "github.com/pyvvo/funcd/internal/kvstore/badger"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/restore"
)

// kvTarget is the KV export's target group's target (ADR-0209 Decision 1): kvstore.backup.target, else backup.target.
func kvTarget(cfg config.Config) string { return cmp.Or(cfg.Kvstore.Backup.Target, cfg.Backup.Target) }

// kvPoints lists the KV points for restore list: from o's source when the KV target is that source, else from the
// KV target with the same credential; none when no KV target is configured.
func kvPoints(ctx context.Context, f *restoreFlags, o restore.Options) ([]kvbadger.Point, error) {
	if f.from != "" || kvTarget(o.Config) == o.Config.Backup.Target {
		return kvbadger.ListPoints(ctx, o.Source)
	}
	src, _, err := f.source(ctx, "kvstore.backup.target", kvTarget(o.Config))
	if err != nil {
		return nil, err
	}
	defer func() { _ = src.Close() }()
	return kvbadger.ListPoints(ctx, src)
}

// kvState is a point's restore verdict: restorable, newer (ADR-0206's CheckVersion refuses its funcd) or not restorable.
func kvState(p kvbadger.Point) string {
	switch {
	case !p.Restorable:
		return "not restorable"
	case restore.CheckVersion(p.Funcd, version.Version) != nil:
		return "newer"
	default:
		return "restorable"
	}
}

// writeKVRows prints the KV points (ADR-0209), none when the target holds none.
func writeKVRows(out io.Writer, ps []kvbadger.Point) error {
	if len(ps) == 0 {
		return nil
	}
	w := &errWriter{w: out}
	w.printf("\n%-24s %-12s %-24s %-14s %s\n", "KV POINT", "CLASS", "AT", "FUNCD", "STATE")
	for _, p := range ps {
		w.printf("%-24s %-12s %-24s %-14s %s\n", fmt.Sprintf("%d/%d", p.Chain, p.Segment), cmp.Or(string(p.Class), "-"),
			v1.NewTimestamp(p.At).String(), cmp.Or(p.Funcd, "-"), kvState(p))
	}
	return w.err
}

// parseKVPoint parses <chain>/<segment>.
func parseKVPoint(s string) (chain, segment uint64, err error) {
	c, m, ok := strings.Cut(s, "/")
	if ok {
		if chain, err = strconv.ParseUint(c, 10, 64); err == nil {
			segment, err = strconv.ParseUint(m, 10, 64)
		}
	}
	if !ok || err != nil {
		return 0, 0, fault.Invalidf("funcd restore kv", "KV point %q: want <chain>/<segment>", s)
	}
	return chain, segment, nil
}

// pickKVPoint is the point args names, else the newest restorable one; one not listed is fault.NotFound.
func pickKVPoint(ps []kvbadger.Point, args []string) (kvbadger.Point, error) {
	const op = "funcd restore kv"
	if len(args) == 0 {
		for i := len(ps) - 1; i >= 0; i-- {
			if ps[i].Restorable {
				return ps[i], nil
			}
		}
		return kvbadger.Point{}, fault.NotFoundf(op, "the backup target holds no restorable KV point")
	}
	n, m, err := parseKVPoint(args[0])
	if err != nil {
		return kvbadger.Point{}, err
	}
	for _, p := range ps {
		if p.Chain == n && p.Segment == m {
			return p, nil
		}
	}
	return kvbadger.Point{}, fault.NotFoundf(op, "the backup target lists no KV point %d/%d", n, m)
}

// newRestoreKVCmd is `funcd restore kv [<chain>/<segment>]` (ADR-0209 Decision 6): a point of the kv/ chains
// (default: the newest restorable) into the empty kvstore.dataDir, read with the operator's credential and identities.
// It applies ADR-0206's CheckVersion to the point's funcd and leaves the platform held, as restore run does (ADR-0206
// Decision 5).
func newRestoreKVCmd(out io.Writer) *cobra.Command {
	var f restoreFlags
	cmd := &cobra.Command{
		Use:   "kv [<chain>/<segment>]",
		Short: "Restore the KV store: a point of the kv/ chains into the empty kvstore.dataDir (default: the newest restorable)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			const op = "funcd restore kv"
			ctx := cmd.Context()
			cfg, err := f.load()
			if err != nil {
				return err
			}
			if cfg.Storage.Mode == "memory" || cfg.Kvstore.Engine != "badger" {
				return fault.Invalidf(op, "the KV store is in memory (storage.mode memory or kvstore.engine not badger): "+
					"there is no KV store to restore")
			}
			src, ids, err := f.source(ctx, "kvstore.backup.target", kvTarget(cfg))
			if err != nil {
				return err
			}
			defer func() { _ = src.Close() }()
			ps, err := kvbadger.ListPoints(ctx, src)
			if err != nil {
				return err
			}
			p, err := pickKVPoint(ps, args)
			if err != nil {
				return err
			}
			if err := restore.CheckVersion(p.Funcd, version.Version); err != nil {
				return err
			}
			h, err := beginRestoreHold(cfg.Storage.DataDir, "kv")
			if err != nil {
				return err
			}
			if err := kvbadger.RestoreDir(ctx, cfg.Kvstore.DataDir, src, p, envelope.Opener(ids)); err != nil {
				return errors.Join(err, h.undo())
			}
			if err := h.end(cfg.Kvstore.DataDir); err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "restored KV point %d/%d (%s) into %s; the platform is held: start funcd, read "+
				"`funcdctl hold status`, then `funcdctl hold release`\n", p.Chain, p.Segment,
				v1.NewTimestamp(p.At).String(), cfg.Kvstore.DataDir)
			return err
		},
	}
	f.register(cmd)
	return cmd
}

// kvInspectReport is what `restore inspect --kv` prints: the point and its version verdict.
type kvInspectReport struct {
	Chain      uint64 `json:"chain"`
	Segment    uint64 `json:"segment"`
	Class      string `json:"class,omitempty"`
	At         string `json:"at"`
	Funcd      string `json:"funcd"`
	Restorable bool   `json:"restorable"`
	Version    string `json:"version"`
}

func inspectKV(ctx context.Context, out io.Writer, f *restoreFlags, o restore.Options, point, output string) error {
	ps, err := kvPoints(ctx, f, o)
	if err != nil {
		return err
	}
	p, err := pickKVPoint(ps, []string{point})
	if err != nil {
		return err
	}
	rep := kvInspectReport{Chain: p.Chain, Segment: p.Segment, Class: string(p.Class), At: v1.NewTimestamp(p.At).String(),
		Funcd: p.Funcd, Restorable: p.Restorable, Version: "restores"}
	if err := restore.CheckVersion(p.Funcd, version.Version); err != nil {
		rep.Version = err.Error()
	}
	data, err := json.Marshal(rep)
	return render(out, data, err, output)
}
