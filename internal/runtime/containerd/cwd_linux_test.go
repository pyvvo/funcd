//go:build linux

package containerd

import (
	"context"
	"net"
	"testing"

	gocni "github.com/containerd/go-cni"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// TestWithAbsoluteCwd checks the cwd guard (the ADR-0052 footprint-lane fix): crun rejects a
// non-absolute process.cwd, which WithImageConfig leaves empty for a WORKDIR-less image — so the
// guard defaults it to "/", while preserving an image that does declare an absolute WORKDIR.
// Non-integration (no containerd needed): it exercises the SpecOpt against a bare spec.
func TestWithAbsoluteCwd(t *testing.T) {
	cases := map[string]string{
		"":     "/", // WORKDIR-less image (the bug)
		".":    "/", // crun's normalized form of the same
		"/app": "/app",
		"/":    "/",
	}
	for in, want := range cases {
		s := &specs.Spec{Process: &specs.Process{Cwd: in}}
		if err := withAbsoluteCwd(context.Background(), nil, nil, s); err != nil {
			t.Fatalf("withAbsoluteCwd(cwd=%q): %v", in, err)
		}
		if s.Process.Cwd != want {
			t.Errorf("cwd %q → %q, want %q", in, s.Process.Cwd, want)
		}
	}
	// nil Process must not panic.
	if err := withAbsoluteCwd(context.Background(), nil, nil, &specs.Spec{}); err != nil {
		t.Fatalf("withAbsoluteCwd(nil process): %v", err)
	}
}

// ipcfg is a tiny helper to build a *gocni.Config interface entry.
func ipcfg(sandbox string, ips ...string) *gocni.Config {
	c := &gocni.Config{Sandbox: sandbox}
	for _, s := range ips {
		c.IPConfigs = append(c.IPConfigs, &gocni.IPConfig{IP: net.ParseIP(s)})
	}
	return c
}

// TestExtractIP pins the footprint-lane fix: gocni.Result.Interfaces is a map (randomized
// iteration) holding the container's sandbox eth0, the sandbox loopback "lo" (127.0.0.1, from
// gocni.WithLoNetwork), and the host-side bridge/veth ends. extractIP must deterministically
// return the SANDBOX, non-loopback IPv4 (the address the shim is reachable on) — never lo and
// never a host-side interface. The old "first IP in the map" logic returned a random winner, so
// on a fraction of containers the reconciler probed 127.0.0.1 (or the gateway) and the function
// never reached Ready. The pod-shaped case is run many times to defeat map-iteration randomness.
func TestExtractIP(t *testing.T) {
	const sb = "/proc/1234/ns/net"

	t.Run("sandbox eth0 chosen over lo and host-side ifaces", func(t *testing.T) {
		// The exact shape observed live: a host-side bridge + veth (Sandbox==""), the sandbox
		// loopback, and the sandbox eth0. extractIP must always pick eth0's 10.63.0.58.
		for i := 0; i < 200; i++ {
			res := &gocni.Result{Interfaces: map[string]*gocni.Config{
				"funcd0":       ipcfg(""), // host-side bridge, no IP in result
				"vethf2b8df83": ipcfg(""), // host-side veth end, no IP
				"lo":           ipcfg(sb, "127.0.0.1", "::1"),
				"eth0":         ipcfg(sb, "10.63.0.58"),
			}}
			if got := extractIP(res); got != "10.63.0.58" {
				t.Fatalf("iter %d: extractIP = %q, want 10.63.0.58 (the sandbox eth0)", i, got)
			}
		}
	})

	t.Run("loopback-only sandbox yields no address", func(t *testing.T) {
		res := &gocni.Result{Interfaces: map[string]*gocni.Config{
			"lo": ipcfg(sb, "127.0.0.1"),
		}}
		if got := extractIP(res); got != "" {
			t.Fatalf("extractIP = %q, want \"\" (loopback must never be returned)", got)
		}
	})

	t.Run("host-side interface IP is never returned", func(t *testing.T) {
		// Even if the bridge end carried the gateway IP in the result, it has no Sandbox and
		// must be ignored in favor of the container's eth0.
		for i := 0; i < 200; i++ {
			res := &gocni.Result{Interfaces: map[string]*gocni.Config{
				"funcd0": ipcfg("", "10.63.0.1"), // gateway, host side
				"eth0":   ipcfg(sb, "10.63.0.42"),
			}}
			if got := extractIP(res); got != "10.63.0.42" {
				t.Fatalf("iter %d: extractIP = %q, want 10.63.0.42 (not the gateway)", i, got)
			}
		}
	})

	t.Run("nil result", func(t *testing.T) {
		if got := extractIP(nil); got != "" {
			t.Fatalf("extractIP(nil) = %q, want \"\"", got)
		}
	})

	t.Run("IPv6 sandbox address used only when no IPv4", func(t *testing.T) {
		res := &gocni.Result{Interfaces: map[string]*gocni.Config{
			"lo":   ipcfg(sb, "::1"),
			"eth0": ipcfg(sb, "fd00::42"),
		}}
		if got := extractIP(res); got != "fd00::42" {
			t.Fatalf("extractIP = %q, want fd00::42", got)
		}
	})
}
