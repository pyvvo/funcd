package controlplane

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
)

// HoldPlatform is what the hold's status and release read from the assembled platform (ADR-0206 Decisions 1, 8):
// the objects per kind, the data the boot reclaims would drop, and the blob watcher's seen lists.
type HoldPlatform interface {
	Counts(ctx context.Context) (map[v1.Kind]int, error)
	Orphans(ctx context.Context) ([]string, error)
	BucketOrphans(ctx context.Context) ([]v1.ObjectRef, error)
	Advance(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error
	Pending(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) (map[string]int, error)
}

// HoldDeps are the hold service's dependencies; Hold nil is a platform never held. Clock nil ⇒ clock.System().
type HoldDeps struct {
	Hold        *hold.Hold
	Store       store.Store
	DeadLetters deadletter.Store
	Platform    HoldPlatform
	Clock       clock.Clock
}

// Holder is the hold's status and release the routes call.
type Holder interface {
	Status(ctx context.Context) (hold.Evidence, error)
	Release(ctx context.Context, advance []string) error
}

// HoldService reads the hold's evidence and releases it (ADR-0206 Decision 8).
type HoldService struct{ d HoldDeps }

// NewHoldService builds the hold service.
func NewHoldService(d HoldDeps) *HoldService {
	if d.Clock == nil {
		d.Clock = clock.System()
	}
	return &HoldService{d: d}
}

func (s *HoldService) held() bool { return s.d.Hold != nil && s.d.Hold.Held() }

// Status is the hold's evidence (Decision 1): the marker, the restore report, the counts now, the paused runs, the
// dead letters, the blob keys a release replays, the data with no object, and the Functions not Ready.
func (s *HoldService) Status(ctx context.Context) (hold.Evidence, error) {
	var st hold.Evidence
	var err error
	if h := s.d.Hold; h != nil {
		if m, ok := h.Marker(); ok {
			st.Held, st.Marker = true, &m
		}
		if t := h.ReleasedAt(); !t.IsZero() {
			at := v1.NewTimestamp(t)
			st.ReleasedAt = &at
		}
		if st.Report, err = h.Report(); err != nil {
			return hold.Evidence{}, err
		}
	}
	p := s.d.Platform
	if st.Counts, err = p.Counts(ctx); err != nil {
		return hold.Evidence{}, err
	}
	if st.PausedRuns, err = s.pausedRuns(ctx); err != nil {
		return hold.Evidence{}, err
	}
	if st.DeadLetters, err = s.deadLetters(ctx); err != nil {
		return hold.Evidence{}, err
	}
	if st.Pending, err = s.pending(ctx); err != nil {
		return hold.Evidence{}, err
	}
	if st.Orphans, err = p.Orphans(ctx); err != nil {
		return hold.Evidence{}, err
	}
	buckets, err := p.BucketOrphans(ctx)
	if err != nil {
		return hold.Evidence{}, err
	}
	for _, b := range buckets {
		st.BucketOrphans = append(st.BucketOrphans, string(b.Namespace)+"/"+string(b.Name))
	}
	if st.FunctionsNotReady, err = s.functionsNotReady(ctx); err != nil {
		return hold.Evidence{}, err
	}
	return st, nil
}

func (s *HoldService) pausedRuns(ctx context.Context) ([]string, error) {
	l, err := s.d.Store.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range l.Items {
		run, ok := o.(*v1.WorkflowRun)
		if ok && run.Spec.Paused && !slices.Contains([]v1.RunPhase{v1.RunSucceeded, v1.RunFailed, v1.RunCancelled}, run.Status.Phase) {
			out = append(out, string(run.Namespace)+"/"+string(run.Name))
		}
	}
	return out, nil
}

func (s *HoldService) deadLetters(ctx context.Context) ([]string, error) {
	if s.d.DeadLetters == nil {
		return nil, nil
	}
	l, err := s.d.Store.List(ctx, v1.KindNamespace.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range l.Items {
		ns := v1.NamespaceName(o.GetName())
		dls, err := s.d.DeadLetters.List(ctx, ns)
		if err != nil {
			return nil, err
		}
		for _, dl := range dls {
			out = append(out, string(ns)+"/"+dl.ID)
		}
	}
	return out, nil
}

// pending maps each blob EventSource, <ns>/<name>, to its events' keys that a release replays.
func (s *HoldService) pending(ctx context.Context) (map[string]map[string]int, error) {
	l, err := s.d.Store.List(ctx, v1.KindEventSource.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]int{}
	for _, o := range l.Items {
		es, ok := o.(*v1.EventSource)
		if !ok || es.Spec.Blob == nil {
			continue
		}
		counts, err := s.d.Platform.Pending(ctx, es.Namespace, es.Name)
		if err != nil {
			return nil, err
		}
		if len(counts) > 0 {
			out[string(es.Namespace)+"/"+string(es.Name)] = counts
		}
	}
	return out, nil
}

func (s *HoldService) functionsNotReady(ctx context.Context) ([]string, error) {
	l, err := s.d.Store.List(ctx, v1.KindFunction.GVK(), store.ListOptions{})
	if err != nil {
		return nil, err
	}
	var out []string
	for _, o := range l.Items {
		if fn, ok := o.(*v1.Function); ok && fn.Status.Phase != v1.PhaseReady {
			out = append(out, string(fn.Namespace)+"/"+string(fn.Name))
		}
	}
	return out, nil
}

// Release lifts the hold (Decision 8): not held ⇒ fault.Conflict; an advance naming no blob event of an existing
// EventSource ⇒ fault.Invalid; both before any change. Then each source advances, rerun-safe (a failure returns with
// the marker kept), and the hold writes the release time and deletes the marker.
func (s *HoldService) Release(ctx context.Context, advance []string) error {
	const op = "controlplane.ReleaseHold"
	if !s.held() {
		return fault.Conflictf(op, "the platform is not held")
	}
	refs := make([]v1.ObjectRef, 0, len(advance))
	for _, a := range advance {
		ref, err := s.blobSource(ctx, a)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	for _, ref := range refs {
		if err := s.d.Platform.Advance(ctx, ref.Namespace, ref.Name); err != nil {
			return fault.Wrapf(err, fault.KindOf(err), op, "advance %s/%s; the platform stays held, rerun the release",
				ref.Namespace, ref.Name)
		}
	}
	return s.d.Hold.Release(s.d.Clock.Now())
}

// blobSource resolves an --advance <ns>/<source> to an EventSource with a blob event.
func (s *HoldService) blobSource(ctx context.Context, a string) (v1.ObjectRef, error) {
	const op = "controlplane.ReleaseHold"
	nsPart, name, ok := strings.Cut(a, "/")
	ns, src := v1.NamespaceName(nsPart), v1.ObjectName(name)
	if !ok || ns.Validate() != nil || src.Validate() != nil {
		return v1.ObjectRef{}, fault.Invalidf(op, "advance %q: want <namespace>/<eventsource>", a)
	}
	obj, err := s.d.Store.Get(ctx, v1.KindEventSource.GVK(), ns, src)
	if fault.KindOf(err) == fault.NotFound {
		return v1.ObjectRef{}, fault.Invalidf(op, "advance %q: no EventSource %s/%s", a, ns, src)
	}
	if err != nil {
		return v1.ObjectRef{}, err
	}
	if es, ok := obj.(*v1.EventSource); !ok || es.Spec.Blob == nil || len(es.Spec.Blob.Events) == 0 {
		return v1.ObjectRef{}, fault.Invalidf(op, "advance %q: EventSource %s/%s has no blob event", a, ns, src)
	}
	return v1.ObjectRef{Kind: v1.KindEventSource, Namespace: ns, Name: src}, nil
}

type holdStatusOutput struct {
	Body hold.Evidence
}

type holdReleaseInput struct {
	Body struct {
		Advance []string `json:"advance,omitempty" doc:"blob EventSources, <namespace>/<name>, whose listed keys are marked seen instead of replayed"`
	}
}

// RegisterHold registers the hold routes (ADR-0206 Decision 8), cluster-scoped and admin-only as ADR-0205's
// backup status: GET …/hold authorizes get on WorkerNode, POST …/hold/release update.
func RegisterHold(api huma.API, h Holder, authz auth.Authorizer) {
	const base = "/apis/funcd.io/v1alpha1/hold"
	huma.Register(api, huma.Operation{
		OperationID: "getHold", Method: http.MethodGet, Path: base, Tags: []string{"Hold"},
	}, func(ctx context.Context, _ *struct{}) (*holdStatusOutput, error) {
		if err := authorizeHold(ctx, authz, auth.VerbGet); err != nil {
			return nil, wrapFaultError(err)
		}
		st, err := h.Status(ctx)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &holdStatusOutput{Body: st}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "releaseHold", Method: http.MethodPost, Path: base + "/release", Tags: []string{"Hold"},
	}, func(ctx context.Context, in *holdReleaseInput) (*struct{}, error) {
		if err := authorizeHold(ctx, authz, auth.VerbUpdate); err != nil {
			return nil, wrapFaultError(err)
		}
		return nil, wrapFaultError(h.Release(ctx, in.Body.Advance))
	})
}

func authorizeHold(ctx context.Context, authz auth.Authorizer, verb auth.Verb) error {
	const op = "controlplane.hold"
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf(op, "no authenticated identity")
	}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: verb, Kind: v1.KindWorkerNode})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "authorize hold %s", verb)
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "hold %s denied: %s", verb, dec.Reason)
	}
	return nil
}

// stubHolder backs RegisterStubHold (spec generation): inert, shape only.
type stubHolder struct{}

func (stubHolder) Status(context.Context) (hold.Evidence, error) { return hold.Evidence{}, nil }
func (stubHolder) Release(context.Context, []string) error       { return nil }

// RegisterStubHold registers the hold routes with inert deps for spec generation.
func RegisterStubHold(api huma.API) { RegisterHold(api, stubHolder{}, stubLogAuthorizer{}) }
