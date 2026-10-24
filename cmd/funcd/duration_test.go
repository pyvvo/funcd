package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: config-shares-grammar — a config duration follows the API grammar and its key's bounds (ADR-0194):
// runtime.supervisionPeriod 1.5s or 0s and runtime.process.stopGrace 11s stop startup, naming the key and the grammar
// or the bounds.
func TestScenarioConfigSharesGrammar(t *testing.T) {
	root := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, c := range []struct{ key, value, want string }{
		{"runtime.supervisionPeriod", "1.5s", "units h, m, s and ms"},
		{"runtime.supervisionPeriod", "0s", "0s is out of bounds: want at least 1ms"},
		{"runtime.process.stopGrace", "11s", "11s is out of bounds: want [1ms, 10s]"},
		{"invoke.defaultTimeout", "1h1ms", "1h1ms is out of bounds: want [0s, 1h]"},
		{"controller.gcSweepInterval", "0", "units h, m, s and ms"},
	} {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			cfg := loadPacing(t, "storage:\n  mode: memory\n  dataDir: \""+shortDataDir(t)+"\"\n"+pacingYAML(c.key, c.value))
			_, _, _, _, err := buildOptions(context.Background(), cfg, root, nil)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.ErrorContains(t, err, `config key "`+c.key+`"`)
			require.ErrorContains(t, err, c.want)
		})
	}
}

// Each config key's lo and hi (ADR-0194 Decision 9): 1ms for a positive key, 0s where 0 keeps its meaning,
// minRetryBackoffMax, maxStopGrace, MaxRetryBackoff and MaxDuration as "no upper bound".
func TestConfigDurationBoundsPerKey(t *testing.T) {
	parse := func(key, value string) error {
		cfg := loadPacing(t, pacingYAML(key, value))
		if key == "runtime.process.stopGrace" {
			_, err := processStopGrace(cfg)
			return err
		}
		if key == "runtime.containerd.stopGrace" {
			_, err := containerdStopGrace(cfg)
			return err
		}
		if key == "s3gateway.keyRotationPeriod" {
			_, err := s3KeyRotationPeriod(cfg)
			return err
		}
		_, err := pacing(cfg)
		return err
	}
	ok := func(key, value string) {
		t.Helper()
		require.NoError(t, parse(key, value), "%s=%s", key, value)
	}
	refused := func(key, value string) {
		t.Helper()
		err := parse(key, value)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s=%s: %v", key, value, err)
		require.ErrorContains(t, err, `"`+key+`"`)
	}
	ok("s3gateway.keyRotationPeriod", "1h")
	refused("s3gateway.keyRotationPeriod", "59m")
	ok("controller.retryBackoffMax", "5ms")
	refused("controller.retryBackoffMax", "4ms")
	ok("workflow.defaultRetryBackoff", "0s")
	ok("workflow.defaultRetryBackoff", "1h")
	refused("workflow.defaultRetryBackoff", "1h1ms")
	ok("runtime.process.stopGrace", "1ms")
	ok("runtime.process.stopGrace", "10s")
	refused("runtime.process.stopGrace", "0s")
	refused("runtime.process.stopGrace", "10s1ms")
	ok("runtime.containerd.stopGrace", "1ms")
	ok("runtime.containerd.stopGrace", "10s")
	refused("runtime.containerd.stopGrace", "0s")
	refused("runtime.containerd.stopGrace", "10s1ms")
	ok("server.shutdownTimeout", "1ms")
	refused("server.shutdownTimeout", "0s")
	for _, bad := range []string{"0", "1.5s", "500us", "10ns", "-1s", "2562047h47m16s855ms"} {
		refused("runtime.supervisionPeriod", bad)
	}

	d, err := parseDuration("controller.gcSweepInterval", v1.MaxDuration.String(), 0, minPositive, v1.MaxDuration)
	require.NoError(t, err)
	require.Equal(t, time.Duration(v1.MaxDuration), d)
	d, err = parseDuration("workflow.retention", "0s", time.Hour, 0, v1.MaxDuration)
	require.NoError(t, err)
	require.Zero(t, d)
	d, err = parseDuration("workflow.retention", "", time.Hour, 0, v1.MaxDuration)
	require.NoError(t, err)
	require.Equal(t, time.Hour, d, "empty keeps the default")
}

// A key renewal must land inside its pair's validity (ADR-0270): with the gateway on, s3gateway.keyRotationPeriod 1h and
// runtime.drainGrace 5m stop startup, naming the period and the sum it must exceed; the defaults fit 1h, and with the
// gateway off nothing is checked.
func TestRenewalFitsTheKeyPeriod(t *testing.T) {
	root := slog.New(slog.NewTextHandler(io.Discard, nil))
	build := func(body string) error {
		cfg := loadPacing(t, "storage:\n  mode: memory\n  dataDir: \""+shortDataDir(t)+"\"\n"+body)
		_, closeExec, _, _, err := buildOptions(context.Background(), cfg, root, nil)
		if err == nil {
			_ = closeExec()
		}
		return err
	}
	gateway := "s3gateway:\n  enabled: true\n  listenAddr: \"127.0.0.1:0\"\n  keyRotationPeriod: \"1h\"\n"
	err := build(gateway + "runtime:\n  drainGrace: \"5m\"\n")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, `config key "s3gateway.keyRotationPeriod"`)
	require.ErrorContains(t, err, "1h47m20s") // 20 × (10s + 5m + 2 × 1s + 10s)
	require.NoError(t, build(gateway))
	require.NoError(t, build("runtime:\n  drainGrace: \"5m\"\n"))
}
