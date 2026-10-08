package main

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// pacingKeys are the 19 ADR-0163 keys with their FUNCD_* variables.
func pacingKeys() []struct{ key, env string } {
	return []struct{ key, env string }{
		{"controller.retryBackoffMax", "FUNCD_CONTROLLER_RETRY_BACKOFF_MAX"},
		{"controller.referentPollInterval", "FUNCD_CONTROLLER_REFERENT_POLL_INTERVAL"},
		{"controller.routeResyncInterval", "FUNCD_CONTROLLER_ROUTE_RESYNC_INTERVAL"},
		{"runtime.supervisionPeriod", "FUNCD_RUNTIME_SUPERVISION_PERIOD"},
		{"runtime.bootTimeout", "FUNCD_RUNTIME_BOOT_TIMEOUT"},
		{"runtime.drainGrace", "FUNCD_RUNTIME_DRAIN_GRACE"},
		{"runtime.handOutSettle", "FUNCD_RUNTIME_HAND_OUT_SETTLE"},
		{"runtime.drainPollInterval", "FUNCD_RUNTIME_DRAIN_POLL_INTERVAL"},
		{"catalog.enginePollInterval", "FUNCD_CATALOG_ENGINE_POLL_INTERVAL"},
		{"catalog.engineProbeTimeout", "FUNCD_CATALOG_ENGINE_PROBE_TIMEOUT"},
		{"workflow.artifactPollInterval", "FUNCD_WORKFLOW_ARTIFACT_POLL_INTERVAL"},
		{"workflow.defaultRetryBackoff", "FUNCD_WORKFLOW_DEFAULT_RETRY_BACKOFF"},
		{"eventing.bucketRecheckInterval", "FUNCD_EVENTING_BUCKET_RECHECK_INTERVAL"},
		{"eventing.deliveryBackoffInitial", "FUNCD_EVENTING_DELIVERY_BACKOFF_INITIAL"},
		{"eventing.deliveryBackoffMax", "FUNCD_EVENTING_DELIVERY_BACKOFF_MAX"},
		{"invoke.activationTimeout", "FUNCD_INVOKE_ACTIVATION_TIMEOUT"},
		{"invoke.reclaimInterval", "FUNCD_INVOKE_RECLAIM_INTERVAL"},
		{"server.shutdownTimeout", "FUNCD_SHUTDOWN_TIMEOUT"},
		{"server.network.workerSyncInterval", "FUNCD_NETWORK_WORKER_SYNC_INTERVAL"},
	}
}

// pacingYAML renders the dotted key with value as nested block-style YAML.
func pacingYAML(key, value string) string {
	var b strings.Builder
	parts := strings.Split(key, ".")
	for i, p := range parts {
		b.WriteString(strings.Repeat("  ", i) + p + ":")
		if i == len(parts)-1 {
			b.WriteString(" \"" + value + "\"")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// loadPacing loads a config whose only content is body, from a file in a fresh directory.
func loadPacing(t *testing.T, body string) config.Config {
	t.Helper()
	dir := shortDataDir(t)
	path := filepath.Join(dir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	cfg, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	return cfg
}

// scenario: defaults-equal-todays-values — with no config file and no FUNCD_* variable every key has its Default.
func TestScenarioDefaultsEqualTodaysValues(t *testing.T) {
	cfg, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	p, err := pacing(cfg)
	require.NoError(t, err)
	require.Equal(t, funcd.Pacing{
		RetryBackoffMax:        time.Second,
		ReferentPollInterval:   2 * time.Second,
		RouteResyncInterval:    10 * time.Second,
		SupervisionPeriod:      10 * time.Second,
		BootTimeout:            time.Minute,
		DrainGrace:             30 * time.Second,
		HandOutSettle:          2 * time.Second,
		DrainPollInterval:      time.Second,
		EnginePollInterval:     2 * time.Second,
		EngineProbeTimeout:     2 * time.Second,
		ArtifactPollInterval:   5 * time.Second,
		DefaultRetryBackoff:    0,
		BucketRecheckInterval:  15 * time.Second,
		DeliveryBackoffInitial: 100 * time.Millisecond,
		DeliveryBackoffMax:     10 * time.Second,
		ActivationTimeout:      30 * time.Second,
		ReclaimInterval:        30 * time.Second,
		ShutdownTimeout:        15 * time.Second,
		WorkerSyncInterval:     2 * time.Second,
		AppUpgradeTimeout:      5 * time.Minute,
	}, p)
}

// scenario: invalid-value-refused-naming-key — a zero (but for workflow.defaultRetryBackoff), negative or malformed
// value in the file or env, or one of the five orderings broken, refuses to start with fault.Invalid naming the key;
// eventing.deliveryBackoffInitial 20s alone is accepted, its max following to 20s.
func TestScenarioInvalidValueRefusedNamingKey(t *testing.T) {
	refused := func(t *testing.T, cfg config.Config, key string) {
		t.Helper()
		_, err := pacing(cfg)
		require.Error(t, err)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, `"`+key+`"`)
	}
	for _, k := range pacingKeys() {
		bad := []string{"-1s", "soon"}
		if k.key != "workflow.defaultRetryBackoff" {
			bad = append(bad, "0s")
		}
		for _, v := range bad {
			t.Run(k.key+"="+v+"/file", func(t *testing.T) {
				refused(t, loadPacing(t, pacingYAML(k.key, v)), k.key)
			})
			t.Run(k.key+"="+v+"/env", func(t *testing.T) {
				t.Setenv(k.env, v)
				refused(t, loadPacing(t, ""), k.key)
			})
		}
	}
	t.Run("workflow.defaultRetryBackoff=0s", func(t *testing.T) {
		p, err := pacing(loadPacing(t, pacingYAML("workflow.defaultRetryBackoff", "0s")))
		require.NoError(t, err)
		require.Zero(t, p.DefaultRetryBackoff)
	})

	orderings := []struct{ body, key, want string }{
		{pacingYAML("invoke.activationTimeout", "2m"), "runtime.bootTimeout", "more than invoke.activationTimeout, 2m0s"},
		{pacingYAML("runtime.handOutSettle", "1m"), "runtime.handOutSettle", "at most runtime.drainGrace, 30s"},
		{pacingYAML("eventing.deliveryBackoffMax", "50ms"), "eventing.deliveryBackoffMax", "at least eventing.deliveryBackoffInitial, 100ms"},
		{pacingYAML("controller.retryBackoffMax", "1ms"), "controller.retryBackoffMax", "at least 5ms"},
		{pacingYAML("workflow.defaultRetryBackoff", "2h"), "workflow.defaultRetryBackoff", "[0s, 1h]"},
	}
	for _, o := range orderings {
		t.Run("ordering/"+o.key, func(t *testing.T) {
			cfg := loadPacing(t, o.body)
			refused(t, cfg, o.key)
			_, err := pacing(cfg)
			require.ErrorContains(t, err, o.want)
		})
	}
	t.Run("ordering/env", func(t *testing.T) {
		t.Setenv("FUNCD_INVOKE_ACTIVATION_TIMEOUT", "2m")
		refused(t, loadPacing(t, ""), "runtime.bootTimeout")
	})

	t.Run("deliveryBackoffInitial 20s alone", func(t *testing.T) {
		p, err := pacing(loadPacing(t, pacingYAML("eventing.deliveryBackoffInitial", "20s")))
		require.NoError(t, err)
		require.Equal(t, 20*time.Second, p.DeliveryBackoffInitial)
		require.Equal(t, 20*time.Second, p.DeliveryBackoffMax)
	})

	t.Run("exits before serving", func(t *testing.T) {
		dir := shortDataDir(t)
		cfg := loadPacing(t, "storage:\n  mode: memory\n  dataDir: \""+dir+"\"\n"+pacingYAML("invoke.activationTimeout", "2m"))
		_, _, _, _, err := buildOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, `"runtime.bootTimeout"`)
	})
}

// Every key reaches its own Pacing field.
func TestPacingMapsEveryKey(t *testing.T) {
	values := map[string]string{
		"controller.retryBackoffMax":        "101ms",
		"controller.referentPollInterval":   "102ms",
		"controller.routeResyncInterval":    "103ms",
		"runtime.supervisionPeriod":         "104ms",
		"runtime.bootTimeout":               "2s",
		"runtime.drainGrace":                "106ms",
		"runtime.handOutSettle":             "105ms",
		"runtime.drainPollInterval":         "107ms",
		"catalog.enginePollInterval":        "108ms",
		"catalog.engineProbeTimeout":        "109ms",
		"workflow.artifactPollInterval":     "110ms",
		"workflow.defaultRetryBackoff":      "111ms",
		"eventing.bucketRecheckInterval":    "112ms",
		"eventing.deliveryBackoffInitial":   "113ms",
		"eventing.deliveryBackoffMax":       "114ms",
		"invoke.activationTimeout":          "1s",
		"invoke.reclaimInterval":            "115ms",
		"server.shutdownTimeout":            "116ms",
		"server.network.workerSyncInterval": "117ms",
	}
	require.Len(t, values, len(pacingKeys()))
	for _, k := range pacingKeys() {
		t.Setenv(k.env, values[k.key])
	}
	p, err := pacing(loadPacing(t, ""))
	require.NoError(t, err)
	ms := func(n int) time.Duration { return time.Duration(n) * time.Millisecond }
	require.Equal(t, funcd.Pacing{
		RetryBackoffMax:        ms(101),
		ReferentPollInterval:   ms(102),
		RouteResyncInterval:    ms(103),
		SupervisionPeriod:      ms(104),
		BootTimeout:            2 * time.Second,
		DrainGrace:             ms(106),
		HandOutSettle:          ms(105),
		DrainPollInterval:      ms(107),
		EnginePollInterval:     ms(108),
		EngineProbeTimeout:     ms(109),
		ArtifactPollInterval:   ms(110),
		DefaultRetryBackoff:    ms(111),
		BucketRecheckInterval:  ms(112),
		DeliveryBackoffInitial: ms(113),
		DeliveryBackoffMax:     ms(114),
		ActivationTimeout:      time.Second,
		ReclaimInterval:        ms(115),
		ShutdownTimeout:        ms(116),
		WorkerSyncInterval:     ms(117),
		AppUpgradeTimeout:      5 * time.Minute,
	}, p)
}

// ADR-0200 Decision 10: an unset app.upgradeTimeout is max(5m, twice runtime.bootTimeout); a set one, from the file or
// FUNCD_APP_UPGRADE_TIMEOUT, is a positive duration (ADR-0194) more than runtime.bootTimeout.
func TestAppUpgradeTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		body string
		want time.Duration
	}{
		{"", 5 * time.Minute},
		{pacingYAML("runtime.bootTimeout", "2m"), 5 * time.Minute},
		{pacingYAML("runtime.bootTimeout", "3m"), 6 * time.Minute},
		{pacingYAML("runtime.bootTimeout", "2000000h"), math.MaxInt64},
		{pacingYAML("app.upgradeTimeout", "61s"), 61 * time.Second},
		{pacingYAML("runtime.bootTimeout", "10s") + pacingYAML("invoke.activationTimeout", "5s") + pacingYAML("app.upgradeTimeout", "20s"), 20 * time.Second},
	} {
		t.Run(strings.ReplaceAll(tc.body, "\n", " "), func(t *testing.T) {
			p, err := pacing(loadPacing(t, tc.body))
			require.NoError(t, err)
			require.Equal(t, tc.want, p.AppUpgradeTimeout)
			require.Greater(t, p.AppUpgradeTimeout, p.BootTimeout)
		})
	}
	t.Run("env", func(t *testing.T) {
		t.Setenv("FUNCD_APP_UPGRADE_TIMEOUT", "90s")
		p, err := pacing(loadPacing(t, ""))
		require.NoError(t, err)
		require.Equal(t, 90*time.Second, p.AppUpgradeTimeout)
	})

	refused := func(t *testing.T, cfg config.Config, want string) {
		t.Helper()
		_, err := pacing(cfg)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, want)
	}
	// scenario: app-upgrade-timeout-config
	t.Run("at the default bootTimeout", func(t *testing.T) {
		refused(t, loadPacing(t, pacingYAML("app.upgradeTimeout", "1m")),
			`config key "app.upgradeTimeout" has invalid value "1m0s" (want more than runtime.bootTimeout, 1m0s)`)
	})
	t.Run("below a set bootTimeout", func(t *testing.T) {
		refused(t, loadPacing(t, pacingYAML("runtime.bootTimeout", "10m")+pacingYAML("app.upgradeTimeout", "6m")),
			`(want more than runtime.bootTimeout, 10m0s)`)
	})
	for _, v := range []string{"0s", "-1s", "soon"} {
		t.Run(v+"/file", func(t *testing.T) {
			refused(t, loadPacing(t, pacingYAML("app.upgradeTimeout", v)), `"app.upgradeTimeout"`)
		})
		t.Run(v+"/env", func(t *testing.T) {
			t.Setenv("FUNCD_APP_UPGRADE_TIMEOUT", v)
			refused(t, loadPacing(t, ""), `"app.upgradeTimeout"`)
		})
	}
	t.Run("exits before serving", func(t *testing.T) {
		dir := shortDataDir(t)
		cfg := loadPacing(t, "storage:\n  mode: memory\n  dataDir: \""+dir+"\"\n"+pacingYAML("app.upgradeTimeout", "1m"))
		_, _, _, _, err := buildOptions(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, `"app.upgradeTimeout"`)
	})
}
