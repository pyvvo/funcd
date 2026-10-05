package function_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/content/oci"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/scheduler"
)

// ADR-0145 tests. The harness's single-node scheduler runs on the host platform; an artifact that provides only
// "plan9/mips" fits no node.

const (
	digestHere      = "sha256:here"
	digestElsewhere = "sha256:elsewhere"
	digestOutage    = "sha256:outage"
)

// fakePlatforms is a function.PlatformResolver keyed by digest.
type fakePlatforms struct {
	mu    sync.Mutex
	calls int
}

func (f *fakePlatforms) Platforms(_ context.Context, _, digest string) ([]v1.OCIPlatform, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	switch digest {
	case digestHere:
		return []v1.OCIPlatform{"plan9/mips", v1.HostPlatform()}, nil
	case digestElsewhere:
		return []v1.OCIPlatform{"plan9/mips"}, nil
	case digestOutage:
		return nil, fault.Unavailablef("fake.Platforms", "registry unreachable")
	}
	return nil, nil
}

func withPlatforms(p *fakePlatforms) func(*function.Deps) {
	return func(d *function.Deps) { d.Platforms = p }
}

// scenario: function-no-matching-platform (ADR-0145) — a new Function whose artifact provides no platform the node
// runs is Failed with NoMatchingPlatform, naming both sides, and no worker is created.
func TestScenarioFunctionNoMatchingPlatform(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestElsewhere })
	h.reconcile(t, "reader")

	fn := h.getFn(t, "reader")
	require.Equal(t, v1.PhaseFailed, fn.Status.Phase)
	ready := h.condition(t, "reader", "Ready")
	require.Equal(t, v1.ConditionFalse, ready.Status)
	require.Equal(t, "NoMatchingPlatform", ready.Reason)
	require.Equal(t, "artifact provides [plan9/mips]; node local runs "+string(v1.HostPlatform()), ready.Message)
	creates, _ := h.rt.counts()
	require.Zero(t, creates, "no worker is created")
}

// An index that provides the node's platform among others reconciles Ready.
func TestIndexWithTheNodePlatformIsReady(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
	h.reconcile(t, "reader")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "reader").Status.Phase)
}

// scenario: redeploy-no-matching-platform-keeps-serving (ADR-0145) — a redeploy to an artifact no node can run leaves
// the serving revision serving: Ready stays True, RevisionReady is False with the reason.
func TestScenarioRedeployNoMatchingPlatformKeepsServing(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}))
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere })
	h.reconcile(t, "reader")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "reader").Status.Phase)
	creates, _ := h.rt.counts()

	h.apply(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestElsewhere })
	h.reconcile(t, "reader")

	fn := h.getFn(t, "reader")
	require.Equal(t, "reader-1", fn.Status.ServingRevision, "the old revision keeps the calls")
	require.Equal(t, v1.ConditionTrue, h.condition(t, "reader", "Ready").Status)
	rr := h.condition(t, "reader", "RevisionReady")
	require.Equal(t, v1.ConditionFalse, rr.Status)
	require.Equal(t, "NoMatchingPlatform", rr.Reason)
	after, _ := h.rt.counts()
	require.Equal(t, creates, after, "no worker of the new revision is created")
}

// scenario: pooled-member-no-matching-platform (ADR-0145) — of two pooled Functions sharing a pool key, the one whose
// artifact no node can run is Failed with NoMatchingPlatform, and the pool serves the other; with the pool at its cap
// of one, the mismatched member is not counted, so the healthy member is admitted.
func TestScenarioPooledMemberNoMatchingPlatform(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{0, 1} {
		h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}), func(d *function.Deps) {
			d.PoolShimCommand = []string{"node", "/opt/funcd/pool.mjs"}
			d.PoolLimit = limit
		})
		// "a-elsewhere" sorts first, so at a cap of one it would take the only slot if it were counted.
		h.create(t, "a-elsewhere", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1"; fn.Spec.ImageDigest = digestElsewhere })
		h.create(t, "b-here", func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1"; fn.Spec.ImageDigest = digestHere })
		h.reconcile(t, "a-elsewhere")
		h.reconcile(t, "b-here")

		require.Equal(t, "NoMatchingPlatform", h.condition(t, "a-elsewhere", "Ready").Reason, "limit %d", limit)
		require.Equal(t, v1.PhaseReady, h.getFn(t, "b-here").Status.Phase, "limit %d: the pool serves the healthy member", limit)
		_, poolFull := h.getFn(t, "b-here").Status.Conditions.Get("PoolFull")
		if poolFull {
			require.NotEqual(t, v1.ConditionTrue, h.condition(t, "b-here", "PoolFull").Status, "limit %d", limit)
		}
	}
}

// recordingScheduler records every placement request the reconciler makes.
type recordingScheduler struct {
	scheduler.Scheduler
	mu   sync.Mutex
	reqs []scheduler.Request
}

func (s *recordingScheduler) Schedule(ctx context.Context, req scheduler.Request) (scheduler.Placement, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.mu.Unlock()
	return s.Scheduler.Schedule(ctx, req)
}

// Every solo replica's Schedule call carries the artifact's platforms (ADR-0145 Decision 5), not only the gate's.
func TestSoloScheduleCallsCarryPlatforms(t *testing.T) {
	t.Parallel()
	rec := &recordingScheduler{}
	h := newShimHarness(t, http.StatusOK, false, withSwitch, withPlatforms(&fakePlatforms{}), func(d *function.Deps) {
		rec.Scheduler = d.Scheduler
		d.Scheduler = rec
	})
	h.create(t, "reader", func(fn *v1.Function) { fn.Spec.ImageDigest = digestHere; fn.Spec.Replicas = 2 })
	h.reconcile(t, "reader")
	require.Equal(t, v1.PhaseReady, h.getFn(t, "reader").Status.Phase)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.GreaterOrEqual(t, len(rec.reqs), 3, "the gate plus one call per replica")
	for _, req := range rec.reqs {
		require.Equal(t, []v1.OCIPlatform{"plan9/mips", v1.HostPlatform()}, req.Platforms, "replica %d", req.Replica)
	}
}

// indexOverUnannotated writes by hand, not with `funcdctl index`, an image index over two unannotated function
// manifests whose descriptors carry platform (nil: none); it returns the index ref and digest.
func indexOverUnannotated(t *testing.T, platform *ocispec.Platform) (ref, digest string) {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "layout")
	tags := []string{"amd64", "arm64"}
	for _, tag := range tags {
		file := filepath.Join(t.TempDir(), "handler.mjs")
		require.NoError(t, os.WriteFile(file, []byte("// "+tag+"\nexport function handle() {}\n"), 0o600))
		_, err := artifact.Push(ctx, "oci-layout://"+dir+":"+tag, file, nil, "nodejs22", "")
		require.NoError(t, err)
	}
	layout, err := oci.New(dir)
	require.NoError(t, err)
	index := ocispec.Index{MediaType: ocispec.MediaTypeImageIndex}
	index.SchemaVersion = 2
	for _, tag := range tags {
		d, rerr := layout.Resolve(ctx, tag)
		require.NoError(t, rerr)
		index.Manifests = append(index.Manifests, ocispec.Descriptor{MediaType: d.MediaType, Digest: d.Digest, Size: d.Size, Platform: platform})
	}
	data, err := json.Marshal(index)
	require.NoError(t, err)
	desc := content.NewDescriptorFromBytes(ocispec.MediaTypeImageIndex, data)
	require.NoError(t, layout.Push(ctx, desc, bytes.NewReader(data)))
	require.NoError(t, layout.Tag(ctx, desc, "fn"))
	return "oci-layout://" + dir + ":fn", desc.Digest.String()
}

// Issue #96: an index none of whose descriptors names a platform provides no platform, not "any platform", so the
// step-2b gate fails the Function with NoMatchingPlatform before any pull instead of retrying the pull forever.
func TestIssue96_IndexWithoutPlatformsIsNoMatchingPlatform(t *testing.T) {
	t.Parallel()
	for name, platform := range map[string]*ocispec.Platform{
		"no platform":     nil,
		"unknown/unknown": {OS: "unknown", Architecture: "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ref, digest := indexOverUnannotated(t, platform)
			oras := artifact.NewOrasMaterializer(t.TempDir(), "")
			h := newShimHarness(t, http.StatusOK, false, withSwitch, func(d *function.Deps) {
				d.Materializer = oras
				d.Platforms = oras
			})
			h.create(t, "reader", func(fn *v1.Function) {
				fn.Spec.Image = ref
				fn.Spec.ImageDigest = digest
			})
			h.reconcile(t, "reader")

			require.Equal(t, v1.PhaseFailed, h.getFn(t, "reader").Status.Phase)
			ready := h.condition(t, "reader", "Ready")
			require.Equal(t, "NoMatchingPlatform", ready.Reason)
			require.Equal(t, "artifact "+digest+" provides no platform: no manifest in its index names one", ready.Message)
			creates, _ := h.rt.counts()
			require.Zero(t, creates, "no worker is created")
		})
	}
}
