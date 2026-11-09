package kv

import (
	"context"
	"log/slog"
	"strings"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/quota"
)

// sweepBatch is how many due keys one driver call takes, so a burst of expiries shares the gateway with the other
// writers between calls; a pass calls again while more are due.
const sweepBatch = 1024

// SweepDeps configures RunSweep.
type SweepDeps struct {
	// KV is the driver; one that is no kvstore.Sweeper runs no sweep.
	KV kvstore.KV
	// Quota hears of each store a pass deleted from, as of a delete, so the store's next write that does not fit
	// measures again (ADR-0292 Decision 4); nil is quota.None.
	Quota quota.Checker
	// Hold keeps a namespace's keys while it or the platform is held, as its timers wait (ADR-0290 Decision 4); nil is
	// hold.NeverNamespace.
	Hold     hold.NamespaceGate
	Interval time.Duration
	Logger   *slog.Logger
}

// RunSweep deletes the keys that expired, a pass every Interval, until ctx ends.
func RunSweep(ctx context.Context, d SweepDeps) {
	s, ok := d.KV.(kvstore.Sweeper)
	if !ok || d.Interval <= 0 {
		return
	}
	if d.Quota == nil {
		d.Quota = quota.None
	}
	if d.Hold == nil {
		d.Hold = hold.NeverNamespace
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	t := time.NewTicker(d.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := Sweep(ctx, s, d.Quota, d.Hold, time.Now()); err != nil && ctx.Err() == nil {
			d.Logger.Warn("kv sweep failed; the next pass tries again", "error", err)
		}
	}
}

// Sweep is one pass: it deletes the keys that expired by now outside the held namespaces, sweepBatch at a time while
// more are due, and reports each store it deleted from to the quota. It returns the keys it deleted.
func Sweep(ctx context.Context, s kvstore.Sweeper, q quota.Checker, gate hold.NamespaceGate, now time.Time) ([]string, error) {
	held := func(key string) bool {
		ns, _, _ := strings.Cut(key, "/")
		return gate.HeldIn(v1.NamespaceName(ns))
	}
	var swept []string
	var err error
	for more := true; more && err == nil && ctx.Err() == nil; {
		var batch []string
		batch, more, err = s.Sweep(ctx, now, sweepBatch, held)
		swept = append(swept, batch...)
	}
	seen := map[quota.Subject]bool{}
	for _, k := range swept {
		parts := strings.SplitN(k, "/", 3)
		if len(parts) < 3 || strings.HasPrefix(k, ".") {
			continue // not a store's key: a quarantine entry's, say
		}
		sub := quota.Subject{Kind: v1.KindKVStore, Namespace: v1.NamespaceName(parts[0]), Name: v1.ObjectName(parts[1])}
		if seen[sub] {
			continue
		}
		seen[sub] = true
		if settle, qerr := q.Admit(ctx, quota.Request{Subject: sub, Deletes: true}); qerr == nil {
			settle(true)
		}
	}
	return swept, err
}
