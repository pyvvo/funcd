package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/api/types/v1alpha1"
)

// LoopbackHost is the host a worker on the host's network reaches funcd's listeners by (ADR-0346 Decision 5).
const LoopbackHost = "127.0.0.1"

// ImageRunner is a worker runtime that may run a worker from an image (ADR-0346 Decision 1). RunsImages says whether it
// runs one now and, when not, why; EndpointHost names the host such a worker reaches funcd's listeners by: LoopbackHost
// on the host's network, or a name that resolves to the host's gateway on an engine in a VM; ImagePlatform is the
// platform such a worker runs on, its container engine's, "" when none runs (ADR-0145 Decision 6).
type ImageRunner interface {
	RunsImages() (ok bool, why string)
	EndpointHost() string
	ImagePlatform() v1alpha1.OCIPlatform
}

// NoImages is the image side of a server that runs no image, and says why: funcdctl dev's when it finds no container
// engine, the engine search's reason (ADR-0346 Decision 1). Create fails as an absent image does (ADR-0149 Decision 3);
// it holds no instance.
func NoImages(why string) Runtime { return noImages{why: why} }

type noImages struct{ why string }

var _ ImageRunner = noImages{}

func (n noImages) RunsImages() (bool, string)        { return false, n.why }
func (noImages) EndpointHost() string                { return LoopbackHost }
func (noImages) ImagePlatform() v1alpha1.OCIPlatform { return "" }

func (n noImages) Create(context.Context, WorkerSpec) (Instance, error) {
	return Instance{}, fault.Wrapf(ErrImageUnavailable, fault.NotFound, "runtime.NoImages", "%s", n.why)
}

func (noImages) Start(_ context.Context, id InstanceID) error {
	return noInstance("runtime.NoImages", id)
}
func (noImages) Stop(_ context.Context, id InstanceID) error {
	return noInstance("runtime.NoImages", id)
}
func (noImages) Status(_ context.Context, id InstanceID) (Instance, error) {
	return Instance{}, noInstance("runtime.NoImages", id)
}
func (noImages) Logs(_ context.Context, id InstanceID) (io.ReadCloser, error) {
	return nil, noInstance("runtime.NoImages", id)
}
func (noImages) Exec(_ context.Context, id InstanceID, _ []string) error {
	return noInstance("runtime.NoImages", id)
}
func (noImages) List(context.Context, v1alpha1.NamespaceName) ([]Instance, error) { return nil, nil }
func (noImages) Remove(context.Context, InstanceID) error                         { return nil }
func (noImages) Close() error                                                     { return nil }

func noInstance(op string, id InstanceID) error {
	return fault.NotFoundf(op, "instance %q not found", id)
}

// Pair runs a Function's or a Job's worker whose spec has an Image on images and any other on process (ADR-0346
// Decision 3), and records which driver holds each instance. Both drivers reap what an earlier run left at their start,
// so the record starts empty; on a miss it asks each driver's Status, since instance IDs are deterministic. List merges
// both. It runs images as images does (ImageRunner), hands raw output capture to both drivers, each for the workers it
// holds, and structured logs to process alone, stops with the longer grace, and enforces limits only when both do.
func Pair(process, images Runtime) Runtime {
	return &pair{process: process, images: images, held: map[InstanceID]Runtime{}}
}

type pair struct {
	process, images Runtime
	mu              sync.Mutex
	held            map[InstanceID]Runtime
}

var (
	_ ImageRunner    = (*pair)(nil)
	_ OutputCapturer = (*pair)(nil)
	_ LogCapturer    = (*pair)(nil)
	_ StopGracer     = (*pair)(nil)
	_ LimitEnforcer  = (*pair)(nil)
)

func (p *pair) Create(ctx context.Context, spec WorkerSpec) (Instance, error) {
	rt := p.process
	if spec.Image != "" {
		rt = p.images
	}
	inst, err := rt.Create(ctx, spec)
	if err == nil {
		p.mu.Lock()
		p.held[inst.ID] = rt
		p.mu.Unlock()
	}
	return inst, err
}

// driverOf is the driver that holds id: the recorded one, else the first whose Status knows id, which it records.
// NotFound when neither does.
func (p *pair) driverOf(ctx context.Context, id InstanceID) (Runtime, error) {
	p.mu.Lock()
	rt, ok := p.held[id]
	p.mu.Unlock()
	if ok {
		return rt, nil
	}
	for _, rt := range []Runtime{p.process, p.images} {
		_, err := rt.Status(ctx, id)
		switch {
		case err == nil:
			p.mu.Lock()
			p.held[id] = rt
			p.mu.Unlock()
			return rt, nil
		case fault.KindOf(err) != fault.NotFound:
			return nil, err
		}
	}
	return nil, noInstance("runtime.Pair", id)
}

func (p *pair) Start(ctx context.Context, id InstanceID) error {
	rt, err := p.driverOf(ctx, id)
	if err != nil {
		return err
	}
	return rt.Start(ctx, id)
}

func (p *pair) Stop(ctx context.Context, id InstanceID) error {
	rt, err := p.driverOf(ctx, id)
	if err != nil {
		return err
	}
	return rt.Stop(ctx, id)
}

func (p *pair) Status(ctx context.Context, id InstanceID) (Instance, error) {
	rt, err := p.driverOf(ctx, id)
	if err != nil {
		return Instance{}, err
	}
	return rt.Status(ctx, id)
}

func (p *pair) Logs(ctx context.Context, id InstanceID) (io.ReadCloser, error) {
	rt, err := p.driverOf(ctx, id)
	if err != nil {
		return nil, err
	}
	return rt.Logs(ctx, id)
}

func (p *pair) Exec(ctx context.Context, id InstanceID, cmd []string) error {
	rt, err := p.driverOf(ctx, id)
	if err != nil {
		return err
	}
	return rt.Exec(ctx, id, cmd)
}

func (p *pair) List(ctx context.Context, ns v1alpha1.NamespaceName) ([]Instance, error) {
	procs, err := p.process.List(ctx, ns)
	if err != nil {
		return nil, err
	}
	imgs, err := p.images.List(ctx, ns)
	if err != nil {
		return nil, err
	}
	return append(procs, imgs...), nil
}

// Remove forgets id on the driver that holds it; an unknown id is a no-op, as the port says.
func (p *pair) Remove(ctx context.Context, id InstanceID) error {
	rt, err := p.driverOf(ctx, id)
	switch {
	case fault.KindOf(err) == fault.NotFound:
		return nil
	case err != nil:
		return err
	}
	if err := rt.Remove(ctx, id); err != nil {
		return err
	}
	p.mu.Lock()
	delete(p.held, id)
	p.mu.Unlock()
	return nil
}

func (p *pair) Close() error { return errors.Join(p.process.Close(), p.images.Close()) }

// RunsImages is the image side's; a side that is no ImageRunner runs none.
func (p *pair) RunsImages() (bool, string) {
	if ir, ok := p.images.(ImageRunner); ok {
		return ir.RunsImages()
	}
	return false, "its image side cannot say whether it runs images"
}

// EndpointHost is the image side's, LoopbackHost when it is no ImageRunner.
func (p *pair) EndpointHost() string {
	if ir, ok := p.images.(ImageRunner); ok {
		return ir.EndpointHost()
	}
	return LoopbackHost
}

// ImagePlatform is the image side's, "" when it is no ImageRunner.
func (p *pair) ImagePlatform() v1alpha1.OCIPlatform {
	if ir, ok := p.images.(ImageRunner); ok {
		return ir.ImagePlatform()
	}
	return ""
}

// SetOutputCapture gives the hook to both drivers: each hands it the runs of the workers it holds (ADR-0168).
func (p *pair) SetOutputCapture(fn OutputCaptureFunc) {
	for _, rt := range []Runtime{p.process, p.images} {
		if oc, ok := rt.(OutputCapturer); ok {
			oc.SetOutputCapture(fn)
		}
	}
}

// SetLogCapture gives the structured-log hook to the process driver alone: a container worker has no such channel and
// prints its records (ADR-0346 Decision 4).
func (p *pair) SetLogCapture(fn LogCaptureFunc) {
	if lc, ok := p.process.(LogCapturer); ok {
		lc.SetLogCapture(fn)
	}
}

// StopGrace is the longer of the two drivers' graces.
func (p *pair) StopGrace() time.Duration {
	var grace time.Duration
	for _, rt := range []Runtime{p.process, p.images} {
		if g, ok := rt.(StopGracer); ok {
			grace = max(grace, g.StopGrace())
		}
	}
	return grace
}

// EnforcesLimits holds only when both drivers enforce them.
func (p *pair) EnforcesLimits() bool {
	for _, rt := range []Runtime{p.process, p.images} {
		if e, ok := rt.(LimitEnforcer); !ok || !e.EnforcesLimits() {
			return false
		}
	}
	return true
}
