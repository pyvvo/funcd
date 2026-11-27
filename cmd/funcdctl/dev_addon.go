//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/provider/docker"
	"github.com/pyvvo/funcd/internal/provider/nerdctl"
	fnruntime "github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/embedimg"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The values of dev.addons.runtime (ADR-0334 Decision 1): unset tries Docker, then containerd through nerdctl.
const (
	devAddonsDocker     = "docker"
	devAddonsContainerd = "containerd"
)

// devAddonRuntime is what dev runs add-on engines on: a provider.Runtime that is also provider.Volumes and ADR-0333's
// AddonFiles, and that dev stops once its platform has stopped.
type devAddonRuntime interface {
	provider.Runtime
	provider.Volumes
	workload.AddonFiles
	StopAll()
}

var (
	_ devAddonRuntime = (*docker.Runtime)(nil)
	_ devAddonRuntime = (*nerdctl.Runtime)(nil)
	_ devAddonRuntime = devAddonsUnavailable{}
)

// devAddonDrivers find the two container engines; a test replaces them. nil is docker.Socket and nerdctl.Find.
type devAddonDrivers struct {
	socket  func(context.Context) (string, error)
	nerdctl func() (string, error)
}

// devAddonOptions runs the add-on engines on the developer's own container engine (ADR-0334 Decision 1), under the
// session's key (Decision 6), from any registry (Decision 5). Dev builds the runtime once process.Open holds the
// session's registry, so a second session is refused first, and its start reaps what a killed session left (Decision 7);
// the platform's stop is followed by its StopAll. Each engine's output goes to its log file beside the dev.watch logs,
// which writef prints once, and never to the terminal (ADR-0342 Decision 5). It also returns the image side of dev's
// worker runtime (ADR-0346 Decision 3), which the cleanups close before the engines stop.
func devAddonOptions(ctx context.Context, stateDir string, cfg devConfig, inst *devInstance,
	writef func(string, ...any) error) ([]funcd.Option, fnruntime.Runtime, error) {
	key, err := devSessionKey(stateDir)
	if err != nil {
		return nil, nil, err
	}
	rt := chooseAddonRuntime(ctx, cfg, key, inst.logger)
	if lr, ok := rt.(interface {
		SetLogs(dir string, printf func(string, ...any) error)
	}); ok {
		lr.SetLogs(filepath.Join(stateDir, "logs"), writef)
	}
	inst.addons = rt
	inst.cleanup = append(inst.cleanup, rt.StopAll)
	images := devWorkers(ctx, rt, key, inst.logger)
	inst.cleanup = append(inst.cleanup, func() { _ = images.Close() })
	own, err := devOwnImages(ctx, cfg, rt, images, inst.logger)
	if err != nil {
		return nil, nil, err
	}
	return []funcd.Option{funcd.WithAddonRuntime(rt), funcd.WithAnyAddonRegistry(),
		funcd.WithAddonsFollowingCurrent(), funcd.WithOwnRuntimeImages(own)}, images, nil
}

// devWorkers runs the session's workers on an Addon's runtime on the engine that runs its add-on engines (ADR-0346
// Decision 4); when dev found none, or the engine's workers cannot start, it runs no image and says why, the engine
// search's reason (Decision 1).
func devWorkers(ctx context.Context, addons devAddonRuntime, key string, logger *slog.Logger) fnruntime.Runtime {
	var eng docker.Engine
	switch a := addons.(type) {
	case *docker.Runtime:
		eng = a.Engine()
	case *nerdctl.Runtime:
		eng = a.Engine()
	case devAddonsUnavailable:
		return fnruntime.NoImages(a.why)
	default:
		return fnruntime.NoImages("funcdctl dev runs no worker on this add-on runtime")
	}
	w, err := docker.NewWorkers(ctx, eng, key, logger)
	if err != nil {
		logger.Warn("workers on an Addon's runtime cannot run on the container engine", "err", err)
		return fnruntime.NoImages(says(err))
	}
	return w
}

// devOwnImages makes dev's engine hold the images that funcdctl carries for funcd's own runtimes under --containers,
// loading each one the engine lacks, and names them (ADR-0359 Decisions 3 and 4); nil without --containers. Dev refuses
// to start when the workers cannot run in containers, when the engine runs another platform than the images', and
// when funcdctl carries the committed placeholders.
func devOwnImages(ctx context.Context, cfg devConfig, addons devAddonRuntime, images fnruntime.Runtime,
	logger *slog.Logger) (map[v1.RuntimeName]string, error) {
	const op = "funcdctl dev --containers"
	if !cfg.containers {
		return nil, nil
	}
	if ir, ok := images.(fnruntime.ImageRunner); ok {
		if runs, why := ir.RunsImages(); !runs {
			return nil, fault.Unavailablef(op, "the workers cannot run in containers: %s", why)
		}
	}
	var eng docker.Engine
	switch a := addons.(type) {
	case *docker.Runtime:
		eng = a.Engine()
	case *nerdctl.Runtime:
		eng = a.Engine()
	default:
		return nil, fault.Unavailablef(op, "dev found no container engine to run the workers on")
	}
	return loadOwnImages(ctx, eng, embedimg.WorkerTar, logger)
}

// loadOwnImages is devOwnImages on the engine eng, with tars the embedded images, a parameter so a test can hand it
// images of its own.
func loadOwnImages(ctx context.Context, eng docker.Engine, tars func(string) (io.Reader, string, bool),
	logger *slog.Logger) (map[v1.RuntimeName]string, error) {
	const op = "funcdctl dev --containers"
	p, err := eng.Platform(ctx)
	if err != nil {
		return nil, err
	}
	if want := v1.OCIPlatform("linux/" + goruntime.GOARCH); p != want {
		return nil, fault.Invalidf(op, "the engine runs %s, and this funcdctl carries images for %s", p, want)
	}
	out := map[v1.RuntimeName]string{}
	for _, rt := range []v1.RuntimeName{"nodejs22", "python314"} {
		tar, ref, ok := tars(string(rt))
		if !ok {
			return nil, fault.Invalidf(op, "this funcdctl carries no %s image: it was built without just build-worker-images", rt)
		}
		has, err := eng.HasImage(ctx, ref)
		if err != nil {
			return nil, err
		}
		if !has {
			t0 := time.Now()
			if err := eng.Load(ctx, tar); err != nil {
				return nil, fault.Wrapf(err, fault.KindOf(err), op, "load the %s image", rt)
			}
			logger.Info("loaded a runtime image", "runtime", rt, "ref", ref, "took", time.Since(t0).String())
		}
		out[rt] = ref
	}
	return out, nil
}

// devSessionKey is the session's key (ADR-0334 Decision 6): the first 16 hex digits of the sha256 of the state dir's
// absolute path.
func devSessionKey(stateDir string) (string, error) {
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, "funcdctl dev", "resolve the state dir %q", stateDir)
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:16], nil
}

// chooseAddonRuntime is Docker when a socket answers, unless dev.addons.runtime is containerd; then nerdctl on PATH,
// unless it is docker. With neither, every object waits RuntimeUnavailable, naming what dev looked for; so does an
// engine that answered but whose start failed.
func chooseAddonRuntime(ctx context.Context, cfg devConfig, key string, logger *slog.Logger) devAddonRuntime {
	socket, find := cfg.addonDrivers.socket, cfg.addonDrivers.nerdctl
	if socket == nil {
		socket = docker.Socket
	}
	if find == nil {
		find = nerdctl.Find
	}
	var looked []string
	if cfg.addonsRuntime != devAddonsContainerd {
		sock, err := socket(ctx)
		if err == nil {
			rt, nerr := docker.New(ctx, sock, key, cfg.persist, logger)
			if nerr != nil {
				return devAddonsUnavailable{why: nerr.Error()}
			}
			logger.Info("add-on engines run on Docker", "socket", sock)
			return rt
		}
		looked = append(looked, says(err))
	}
	if cfg.addonsRuntime != devAddonsDocker {
		bin, err := find()
		if err == nil {
			rt, nerr := nerdctl.New(ctx, bin, key, cfg.persist, logger)
			if nerr != nil {
				return devAddonsUnavailable{why: nerr.Error()}
			}
			logger.Info("add-on engines run on containerd through nerdctl", "nerdctl", bin)
			return rt
		}
		looked = append(looked, says(err))
	}
	return devAddonsUnavailable{why: "no container engine runs add-on engines: " + strings.Join(looked, "; ") +
		"; start one, then restart funcdctl dev"}
}

// says is err's message without the operation that raised it.
func says(err error) string {
	var fe *fault.Error
	if errors.As(err, &fe) && fe.Err == nil {
		return fe.Msg
	}
	return err.Error()
}

// devAddonsUnavailable is the add-on runtime of a dev that found no container engine (ADR-0334 Decision 1): every
// object waits Pending, RuntimeUnavailable, naming what dev looked for; it holds no volume.
type devAddonsUnavailable struct{ why string }

func (u devAddonsUnavailable) err() error { return fault.Unavailablef("funcdctl dev", "%s", u.why) }

func (u devAddonsUnavailable) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{Reason: reasonRuntimeUnavailable}, u.err()
}

func (devAddonsUnavailable) Teardown(context.Context, provider.ProviderRef) error { return nil }
func (devAddonsUnavailable) Reclaim(context.Context, provider.ProviderRef) error  { return nil }
func (devAddonsUnavailable) List(context.Context) ([]provider.ProviderRef, error) { return nil, nil }
func (devAddonsUnavailable) StopAll()                                             {}
func (u devAddonsUnavailable) Clear(context.Context, v1.NamespaceName, v1.ObjectName, string) error {
	return u.err()
}

func (devAddonsUnavailable) Quarantine() quarantine.Store {
	return provider.NewDirQuarantine("", v1.KindAddonObject, nil, nil)
}

func (u devAddonsUnavailable) Newest(context.Context, v1.NamespaceName, v1.ObjectName, string, time.Duration) (string, io.ReadCloser, error) {
	return "", nil, u.err()
}

func (u devAddonsUnavailable) Put(context.Context, v1.NamespaceName, v1.ObjectName, string, string, io.Reader, int, int) error {
	return u.err()
}

// reasonRuntimeUnavailable is ADR-0149's reason, which the add-on reconciler holds an object with (ADR-0332 Decision 3).
const reasonRuntimeUnavailable = "RuntimeUnavailable"
