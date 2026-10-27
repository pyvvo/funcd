// Package site reconciles the Site resource (ADR-0139, F103): it resolves the site bundle's digest
// once per spec generation, materializes the bundle under a digest-scoped Bucket prefix (index written
// last, so its presence means complete), materializes the owned Bucket + Route, and publishes a status
// DERIVED from the owned Route — the durable record of what is served. A failed deploy leaves the
// previous digest serving.
package site

import (
	"context"
	"errors"
	"fmt"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/route/owned"
	"github.com/pyvvo/funcd/internal/store"
)

const (
	op        = "site.Reconcile"
	condReady = v1.ConditionType("Ready")
	// reasonDisabled is the owned Route's verdict while the Site's own address is off (ADR-0241 Decision 6).
	reasonDisabled = "Disabled"
	// routeRequeue is the poll interval while the owned Route is pending (the ADR-0091 precedent); a
	// later change to the Route re-runs the Site through owned.MapOwner.
	routeRequeue = 2 * time.Second
)

// BucketResolver resolves a (namespace, bucket) to its per-namespace blob view — the SAME s3BucketFor
// the S3 frontend and the ADR-0120 static handler use, so one substrate serves all three.
type BucketResolver func(ns v1.NamespaceName, bucket string) (blob.Bucket, bool)

// Deps are the reconciler's dependencies; Store and Buckets are required.
type Deps struct {
	Store   store.Store
	Buckets BucketResolver
	// DefaultIndex is the platform config's site.defaultIndex: the document served for "/" (and asserted
	// present) when a Site's spec.index is empty. "" ⇒ "index.html".
	DefaultIndex string
	Logger       *slog.Logger
	// ReferentPollInterval is controller.referentPollInterval (ADR-0163); 0 ⇒ routeRequeue.
	ReferentPollInterval time.Duration
}

// Reconciler drives a Site to its desired state (controller.Reconciler).
type Reconciler struct {
	store        store.Store
	buckets      BucketResolver
	defaultIndex string
	logger       *slog.Logger
	routeRequeue time.Duration
}

// New builds the Site reconciler.
func New(d Deps) *Reconciler {
	l := d.Logger
	if l == nil {
		l = slog.Default()
	}
	poll := d.ReferentPollInterval
	if poll <= 0 {
		poll = routeRequeue
	}
	return &Reconciler{store: d.Store, buckets: d.Buckets, defaultIndex: d.DefaultIndex, logger: l.With("component", "site"), routeRequeue: poll}
}

// outcome is one reconcile's publishable result: the digest the owned Route holds (serving), whether
// this generation's target is now programmed on it, the Ready condition, and the requeue.
type outcome struct {
	digest     string
	view       blob.Bucket
	programmed bool
	cond       v1.Condition
	result     controller.Result
}

func notReady(reason, message string) v1.Condition {
	return v1.Condition{Type: condReady, Status: v1.ConditionFalse, Reason: reason, Message: message}
}

// Reconcile, in order: (1) reads the owned Route (if present and owned) and takes its bundle rule's
// prefix as the CURRENT serving digest — the durable record; (2) resolves spec.image → a digest only
// when status.observedGeneration != metadata.generation, else keeps the serving digest; (3) adopts or
// creates the Bucket (PrefixOwned if the site prefix is already declared with an owner); (4) if the
// target digest's index object is absent, unpacks the bundle under <prefix>/<digest-slug>/ over
// blob.Bucket — every non-index entry first, the index object LAST; an index already present means
// complete, nothing is re-uploaded; (5) asserts the index, then materializes/updates the owned Route
// (RouteNotOwned if foreign) with the bundle rule at the target digest; (6) reads the Route's Ready
// condition — pending (absent or stale ObservedGeneration) ⇒ NotReady RouteNotReady + RequeueAfter 2s;
// (7) publishes a status DERIVED from the Route, advancing status.observedGeneration only once this
// generation's digest is the one programmed on the Route. A failure before (5) leaves the Route —
// hence the PREVIOUS digest — serving, and the generation unobserved so the next reconcile retries.
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) {
	obj, err := r.store.Get(ctx, v1.KindSite.GVK(), req.Namespace, req.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return controller.Result{}, nil // deleted; nothing is reclaimed here (Decision §5)
		}
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "get site %s/%s", req.Namespace, req.Name)
	}
	s := obj.(*v1.Site)

	rt, err := r.routeOf(ctx, s)
	if err != nil {
		return controller.Result{}, err
	}
	if rt != nil && !v1.ControlledBy(rt.OwnerReferences, v1.KindSite, s.UID) {
		return r.publish(ctx, s, routeNotOwned(s))
	}
	serving := servingDigest(rt, s)

	target := serving
	if target == "" || s.Status.ObservedGeneration != s.Generation {
		d, rerr := artifact.ResolveSite(ctx, s.Spec.Image)
		if rerr != nil {
			switch fault.KindOf(rerr) {
			case fault.NotFound:
				return r.publish(ctx, s, outcome{digest: serving, cond: notReady("ArtifactNotFound", rerr.Error())})
			case fault.Invalid:
				return r.publish(ctx, s, outcome{digest: serving, cond: notReady("DigestUnresolved", rerr.Error())})
			default:
				return controller.Result{}, fault.Wrapf(rerr, fault.KindOf(rerr), op, "resolve %q", s.Spec.Image)
			}
		}
		target = d
	}

	reason, msg, err := r.ensureBucket(ctx, s)
	if err != nil {
		return controller.Result{}, err
	}
	if reason != "" {
		return r.publish(ctx, s, outcome{digest: serving, cond: notReady(reason, msg)})
	}
	view, ok := r.buckets(s.Namespace, string(s.Spec.ServedBucket()))
	if !ok {
		return controller.Result{}, fault.Internalf(op, "bucket %s/%s has no substrate view", s.Namespace, s.Spec.ServedBucket())
	}

	sp, index := servingPrefix(s.Spec.ServedPrefix(), target), indexOf(s, r.defaultIndex)
	complete, err := view.Exists(ctx, sp+index)
	if err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "probe index %q", sp+index)
	}
	if !complete {
		reason, msg, uerr := r.unpack(ctx, view, s, target, sp, index)
		if uerr != nil {
			return controller.Result{}, uerr
		}
		if reason != "" {
			return r.publish(ctx, s, outcome{digest: serving, view: view, cond: notReady(reason, msg)})
		}
	}

	// ADR-0239 Decision 10: with baseHref the bundle rule serves a copy of the index carrying the mount, written into the
	// digest folder before the Route, under a key that names the mount, so a new digest or mount switches with the Route.
	served := index
	if s.Spec.Ingress.BaseHref {
		rel := indexCopyRel(s, index)
		reason, msg, werr := writeIndexCopy(ctx, view, sp, index, rel, baseHrefOf(s))
		if werr != nil {
			return controller.Result{}, werr
		}
		if reason != "" {
			return r.publish(ctx, s, outcome{digest: serving, view: view, cond: notReady(reason, msg)})
		}
		served = rel
	}

	// ADR-0239 Decision 7: the config step runs once the bundle is complete and before the Route. A config failure
	// rewrites nothing and never holds the bundle back; the rule is compiled exactly when the object exists. Removal
	// deletes the object first, while the current Route still carries the rule.
	config, cfgReason, cfgMsg := false, "", ""
	switch {
	case s.Spec.RuntimeConfig != nil:
		reason, msg, werr := r.writeRuntimeConfig(ctx, s, view)
		if werr != nil {
			return controller.Result{}, werr
		}
		cfgReason, cfgMsg, config = reason, msg, reason == ""
		if !config {
			if config, err = view.Exists(ctx, runtimeConfigKey(s)); err != nil {
				return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "probe %q", runtimeConfigKey(s))
			}
		}
	case servesRuntimeConfig(rt, s):
		if derr := dropRuntimeConfig(ctx, s, view); derr != nil {
			return controller.Result{}, derr
		}
	}

	rt, err = owned.Ensure(ctx, r.store, s, s.Name, compileRoute(s, target, served, config))
	if errors.Is(err, owned.ErrNotOwned) {
		return r.publish(ctx, s, routeNotOwned(s))
	}
	if err != nil {
		return controller.Result{}, err
	}
	out := outcome{digest: target, view: view, programmed: true}
	cnd, ok := rt.Status.Conditions.Get(condReady)
	switch {
	case !ok || cnd.ObservedGeneration < rt.Generation:
		out.cond = notReady("RouteNotReady", fmt.Sprintf("route %q is pending", rt.Name))
		out.result = controller.Result{RequeueAfter: r.routeRequeue}
	case cnd.Status != v1.ConditionTrue && cnd.Reason != reasonDisabled: // a Disabled Route is off by choice (ADR-0241)
		out.cond = notReady("RouteNotReady", fmt.Sprintf("route %q: %s: %s", rt.Name, cnd.Reason, cnd.Message))
	case cfgReason != "":
		out.cond = notReady(cfgReason, cfgMsg)
	default:
		out.cond = v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "Materialized"}
	}
	return r.publish(ctx, s, out)
}

// MapBucket is the controller.MapFunc that re-runs the reconcile of every Site declaring a changed Bucket:
// a bundle its maxObjectBytes refused can deploy once the cap is raised, and no Site event follows a Bucket
// write.
func (r *Reconciler) MapBucket(ctx context.Context, obj v1.Object) []controller.Request {
	meta := obj.GetObjectMeta()
	list, err := r.store.List(ctx, v1.KindSite.GVK(), store.ListOptions{Namespace: meta.Namespace})
	if err != nil {
		r.logger.WarnContext(ctx, "list sites of a changed bucket", "namespace", string(meta.Namespace), "bucket", string(meta.Name), "error", err)
		return nil
	}
	var reqs []controller.Request
	for _, o := range list.Items {
		if s, ok := o.(*v1.Site); ok && s.Spec.ServedBucket() == meta.Name {
			reqs = append(reqs, controller.Request{GVK: v1.KindSite.GVK(), Namespace: s.Namespace, Name: s.Name})
		}
	}
	return reqs
}

// routeOf reads the Site's same-named Route; nil when absent.
func (r *Reconciler) routeOf(ctx context.Context, s *v1.Site) (*v1.Route, error) {
	obj, err := r.store.Get(ctx, v1.KindRoute.GVK(), s.Namespace, s.Name)
	if err != nil {
		if fault.KindOf(err) == fault.NotFound {
			return nil, nil
		}
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "get route %s/%s", s.Namespace, s.Name)
	}
	return obj.(*v1.Route), nil
}

// ensureBucket applies ADR-0248 Decision 7 and returns the NotReady reason that stops the Site, "" when it may serve.
// By name it creates the Bucket with the Site's marker, or adds only what is absent to the one that carries it; an
// existing Bucket without the marker is StoreNotOwned. By ref the Bucket and its served prefix must exist and are never
// edited. Either way a served prefix with an owner is PrefixOwned: a site never takes over a written prefix.
func (r *Reconciler) ensureBucket(ctx context.Context, s *v1.Site) (reason, message string, err error) {
	name := s.Spec.ServedBucket()
	ref := s.Spec.BucketEntry() != nil && s.Spec.BucketEntry().Ref != ""
	obj, gerr := r.store.Get(ctx, v1.KindBucket.GVK(), s.Namespace, name)
	switch {
	case fault.KindOf(gerr) == fault.NotFound && ref:
		return reasonBucketNotFound, fmt.Sprintf("bucket %q, which spec.buckets refers to, does not exist", name), nil
	case fault.KindOf(gerr) == fault.NotFound:
		if _, cerr := r.store.Create(ctx, newBucket(s)); cerr != nil {
			return "", "", fault.Wrapf(cerr, fault.KindOf(cerr), op, "create bucket %q", name)
		}
		return "", "", nil
	case gerr != nil:
		return "", "", fault.Wrapf(gerr, fault.KindOf(gerr), op, "get bucket %q", name)
	}
	b := obj.(*v1.Bucket)
	if !ref && !ownedstore.Marked(b.OwnerReferences, s) {
		return reasonStoreNotOwned, fmt.Sprintf("bucket %q exists and was not made by this site; bind it with ref", name), nil
	}
	if ref {
		p, ok := prefixOf(b, s.Spec.ServedPrefix())
		switch {
		case !ok:
			return reasonBucketNotFound, fmt.Sprintf("bucket %q, which spec.buckets refers to, has no prefix %q", name, s.Spec.ServedPrefix()), nil
		case !p.Owner.IsZero():
			return reasonPrefixOwned, prefixOwnedMessage(s), nil
		}
		return "", "", nil
	}
	changed, owned := fillOwnBucket(b, s)
	if owned {
		return reasonPrefixOwned, prefixOwnedMessage(s), nil
	}
	if changed {
		if _, uerr := r.store.Update(ctx, b); uerr != nil {
			return "", "", fault.Wrapf(uerr, fault.KindOf(uerr), op, "update bucket %q", name)
		}
	}
	return "", "", nil
}

// The reasons a Site's Bucket stops it (ADR-0139, ADR-0248).
const (
	reasonStoreNotOwned  = "StoreNotOwned"
	reasonPrefixOwned    = "PrefixOwned"
	reasonBucketNotFound = "BucketNotFound"
)

func prefixOwnedMessage(s *v1.Site) string {
	return fmt.Sprintf("bucket %q already declares prefix %q with an owner; a site never takes over a written prefix", s.Spec.ServedBucket(), s.Spec.ServedPrefix())
}

// prefixOf is the prefix of b named name, and whether it has one.
func prefixOf(b *v1.Bucket, name string) (v1.BucketPrefix, bool) {
	for _, p := range b.Spec.Prefixes {
		if p.Name == name {
			return p, true
		}
	}
	return v1.BucketPrefix{}, false
}

// reasonQuotaExceeded is a Site whose writes its Bucket's quota refuses (ADR-0292).
const reasonQuotaExceeded = "QuotaExceeded"

// unpack pulls the digest-pinned bundle into a scratch dir and Puts every entry under sp — non-index
// entries first, the index LAST so its presence is the completeness marker. A bundle without the index
// is not uploaded at all (IndexMissing); a NotFound/Invalid pull, an entry over the Bucket's
// maxObjectBytes, or one the substrate cannot store under its key, is a NotReady reason; anything else
// is a transient error for the controller to retry.
func (r *Reconciler) unpack(ctx context.Context, view blob.Bucket, s *v1.Site, digest, sp, index string) (reason, message string, err error) {
	tmp, terr := os.MkdirTemp("", "funcd-site-")
	if terr != nil {
		return "", "", fault.Wrapf(terr, fault.Internal, op, "scratch dir")
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if perr := artifact.PullSite(ctx, s.Spec.Image, digest, tmp); perr != nil {
		switch fault.KindOf(perr) {
		case fault.NotFound:
			return "ArtifactNotFound", perr.Error(), nil
		case fault.Invalid:
			return "MaterializeFailed", perr.Error(), nil
		default:
			pinned := s.Spec.Image
			if !strings.HasSuffix(pinned, "@"+digest) { // a digest-pinned image resolves to its own digest
				pinned += "@" + digest
			}
			return "", "", fault.Wrapf(perr, fault.KindOf(perr), op, "pull %s", pinned)
		}
	}
	var files []string
	werr := filepath.WalkDir(tmp, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(tmp, p)
		if rerr != nil {
			return rerr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if werr != nil {
		return "", "", fault.Wrapf(werr, fault.Internal, op, "walk bundle")
	}
	if !slices.Contains(files, index) {
		return "IndexMissing", fmt.Sprintf("bundle %s has no %q; nothing materialized", digest, index), nil
	}
	if i := slices.IndexFunc(files, isRuntimeEntry); i >= 0 {
		return "MaterializeFailed", fmt.Sprintf("bundle %s holds %q, under the _runtime/ that funcd reserves for a Site's generated files (ADR-0239); nothing materialized", digest, files[i]), nil
	}
	slices.Sort(files)
	put := func(rel string) (reason, message string, err error) {
		data, rerr := os.ReadFile(filepath.Join(tmp, filepath.FromSlash(rel))) //nolint:gosec // rel is a walked entry under the scratch dir
		if rerr != nil {
			return "", "", fault.Wrapf(rerr, fault.Internal, op, "read %q", rel)
		}
		if perr := view.Put(ctx, sp+rel, data, blob.PutOptions{}); perr != nil {
			// An object over the Bucket's maxObjectBytes (blob.Capped: PayloadTooLarge), or a key the
			// substrate cannot store (Invalid), fails every retry until the spec or the Bucket changes
			// (MapBucket); so does a Bucket at its quota (ADR-0292).
			if k := fault.KindOf(perr); k == fault.PayloadTooLarge || k == fault.Invalid {
				return "MaterializeFailed", perr.Error(), nil
			}
			if fault.KindOf(perr) == fault.InsufficientStorage {
				return reasonQuotaExceeded, perr.Error(), nil
			}
			return "", "", fault.Wrapf(perr, fault.KindOf(perr), op, "put %q", sp+rel)
		}
		return "", "", nil
	}
	for _, rel := range files {
		if rel == index {
			continue
		}
		if reason, msg, perr := put(rel); reason != "" || perr != nil {
			return reason, msg, perr
		}
	}
	if reason, msg, perr := put(index); reason != "" || perr != nil {
		return reason, msg, perr
	}
	r.logger.Info("site bundle materialized", "site", s.Name, "namespace", s.Namespace, "digest", digest, "objects", len(files))
	return "", "", nil
}

// routeNotOwned is the outcome while a Route of the Site's name is not controlled by this Site with its current UID:
// the first writer wins (ADR-0241 Decision 5).
func routeNotOwned(s *v1.Site) outcome {
	return outcome{cond: notReady("RouteNotOwned", fmt.Sprintf("route %q exists and is not controlled by this site", s.Name))}
}

// publish derives the Site's status from the outcome — digest/servingPrefix from what the Route holds,
// objects/bytes from the substrate — and writes it (a byte-identical status is a store no-op).
func (r *Reconciler) publish(ctx context.Context, s *v1.Site, out outcome) (controller.Result, error) {
	s.Status.Digest, s.Status.ServingPrefix, s.Status.Objects, s.Status.Bytes = "", "", 0, 0
	if out.digest != "" {
		s.Status.Digest = out.digest
		s.Status.ServingPrefix = servingPrefix(s.Spec.ServedPrefix(), out.digest)
		if out.view != nil {
			attrs, lerr := out.view.List(ctx, s.Status.ServingPrefix)
			if lerr != nil {
				return controller.Result{}, fault.Wrapf(lerr, fault.KindOf(lerr), op, "list %q", s.Status.ServingPrefix)
			}
			for _, a := range attrs {
				if isRuntimeEntry(strings.TrimPrefix(a.Key, s.Status.ServingPrefix)) {
					continue // a Site's generated copies are not the bundle (ADR-0239 Decision 10)
				}
				s.Status.Objects++
				s.Status.Bytes += a.Size
			}
		}
	}
	if out.programmed {
		s.Status.ObservedGeneration = s.Generation
	}
	out.cond.ObservedGeneration = s.Generation
	s.Status.Conditions.Set(out.cond)
	if out.cond.Status == v1.ConditionTrue {
		s.Status.Phase = v1.PhaseReady
	} else {
		s.Status.Phase = v1.PhasePending
	}
	if _, err := r.store.Update(ctx, s); err != nil {
		return controller.Result{}, fault.Wrapf(err, fault.KindOf(err), op, "update site status %q", s.Name)
	}
	return out.result, nil
}
