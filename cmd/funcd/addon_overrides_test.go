package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// ADR-0344 Decision 4 at the daemon: a key of runtime.containerd.imageOverride is the operator's runtime, so the
// platform that cmd/funcd assembles from the config refuses an Addon that ships a runtime of that name, and stores
// one that ships another.
func TestAddonRefusesAnImageOverrideKey(t *testing.T) {
	dir := shortDataDir(t)
	path := filepath.Join(dir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \"127.0.0.1:0\"\n"+
			"storage:\n  mode: memory\n  dataDir: \""+dir+"\"\n"+
			"auth:\n  credentials:\n"+entry(tokenFile(t, t.TempDir(), "admin", adminToken+"\n", 0o600), "admin")+
			"addons:\n  registries:\n    - registry.example.com\n"+
			"runtime:\n  containerd:\n    imageOverride:\n      ruby3: registry.example.com/ruby:3\n"), 0o600))
	cfg, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	opts, closeExec, _, _, err := buildOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeExec() })
	p, err := funcd.New(opts...)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return")
		}
	})
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(adminToken))
	require.NoError(t, err)

	addon := func(name, runtime string) *v1.Addon {
		obj, _ := v1.NewObject(v1.KindAddon)
		a := obj.(*v1.Addon)
		a.Name = v1.ObjectName(name)
		a.Spec.Runtimes = []v1.ShippedRuntime{{
			Name:      v1.RuntimeName(runtime),
			Image:     "registry.example.com/" + runtime + "@sha256:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			Workloads: []v1.Workload{v1.WorkloadFunction},
		}}
		return a
	}
	_, err = c.Create(context.Background(), addon("ruby", "ruby3"))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, `"ruby3" is a key of runtime.containerd.imageOverride`)
	_, err = c.Create(context.Background(), addon("deno", "deno2"))
	require.NoError(t, err, "a runtime that no key maps is stored")
}
