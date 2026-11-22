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
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/provider/docker"
	"github.com/pyvvo/funcd/internal/provider/nerdctl"
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
// the platform's stop is followed by its StopAll.
func devAddonOptions(ctx context.Context, stateDir string, cfg devConfig, inst *devInstance) ([]funcd.Option, error) {
	key, err := devSessionKey(stateDir)
	if err != nil {
		return nil, err
	}
	rt := chooseAddonRuntime(ctx, cfg, key, inst.logger)
	inst.addons = rt
	inst.cleanup = append(inst.cleanup, rt.StopAll)
	return []funcd.Option{funcd.WithAddonRuntime(rt, v1.AddonRuntimeContainerd), funcd.WithAnyAddonRegistry(),
		funcd.WithAddonsFollowingCurrent()}, nil
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
