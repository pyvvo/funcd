package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/config"
)

// scenario: zero-config-defaults — an empty File + no env + no flags ⇒ every field its built-in default.
func TestScenarioZeroConfigDefaults(t *testing.T) {
	r, err := config.Resolve(config.File{}, config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:8080", r.ListenAddr)
	require.Equal(t, "127.0.0.1:0", r.DataPlaneAddr)
	require.Equal(t, "file", r.StorageMode)
	require.Equal(t, "/var/lib/funcd", r.DataDir)
	require.Equal(t, "", r.Token, "empty ⇒ cmd/funcd uses the built-in dev token + warn")
	require.Equal(t, []string{"default"}, r.Namespaces)
	require.Equal(t, "", r.SecretsEncryptionKeyFile)
	require.Equal(t, "process", r.RuntimeMode)
	require.Equal(t, "overlayfs", r.Containerd.Snapshotter)
	require.Equal(t, "/opt/cni/bin", r.Containerd.CNIBinDir)
	require.Equal(t, "10.63.0.0/16", r.Containerd.SubnetCIDR)
	require.Equal(t, "funcd/runtime-", r.Containerd.ImagePrefix)
	require.Equal(t, "/var/lib/funcd/containerd", r.Containerd.Root, "dataDir-derived default")
	require.Equal(t, "/var/lib/funcd/cni", r.Containerd.CNIConfDir, "dataDir-derived default")
	require.Equal(t, "json", r.LogFormat)
	require.Equal(t, "info", r.LogLevel)
	require.Equal(t, "", r.TelemetryEndpoint)
}

// scenario: file-sets-addresses (internal half) — the file sets the previously code-only addresses.
func TestScenarioFileSetsAddresses(t *testing.T) {
	r, err := config.Resolve(config.File{
		APIVersion: config.APIVersion,
		Kind:       config.Kind,
		Server:     config.Server{ListenAddr: "1.2.3.4:9000", DataPlaneAddr: "127.0.0.1:9001"},
	}, config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "1.2.3.4:9000", r.ListenAddr)
	require.Equal(t, "127.0.0.1:9001", r.DataPlaneAddr)
}

// scenario: env-overrides-file — FUNCD_DATA_DIR wins over storage.dataDir (precedence env > file).
func TestScenarioEnvOverridesFile(t *testing.T) {
	t.Setenv("FUNCD_DATA_DIR", "/env/dir")
	r, err := config.Resolve(config.File{Storage: config.Storage{DataDir: "/file/dir"}}, config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "/env/dir", r.DataDir, "env beats the file value")
}

// scenario: partial-file-fills-rest — a file that sets only server: leaves every other group default.
func TestScenarioPartialFileFillsRest(t *testing.T) {
	r, err := config.Resolve(config.File{Server: config.Server{ListenAddr: "0.0.0.0:7000"}}, config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:7000", r.ListenAddr)
	require.Equal(t, "file", r.StorageMode, "unset groups take their default")
	require.Equal(t, "process", r.RuntimeMode)
	require.Equal(t, "json", r.LogFormat)
}

// the --memory flag overrides storage.mode (the top precedence tier).
func TestFlagOverridesStorageMode(t *testing.T) {
	mem := true
	r, err := config.Resolve(config.File{Storage: config.Storage{Mode: "file"}}, config.Flags{MemoryOnly: &mem})
	require.NoError(t, err)
	require.Equal(t, "memory", r.StorageMode, "--memory beats the file's storage.mode: file")
}

// scenario: invalid-enum-rejected — a bad enum value ⇒ fault.Invalid naming the allowed set.
func TestScenarioInvalidEnumRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		file config.File
	}{
		{"storage.mode", config.File{Storage: config.Storage{Mode: "bogus"}}},
		{"runtime.mode", config.File{Runtime: config.Runtime{Mode: "vm"}}},
		{"log.format", config.File{Log: config.Log{Format: "xml"}}},
		{"log.level", config.File{Log: config.Log{Level: "loud"}}},
		{"apiVersion", config.File{APIVersion: "funcd.io/v2"}},
		{"kind", config.File{Kind: "Function"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Resolve(tc.file, config.Flags{})
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a bad %s is fault.Invalid", tc.name)
		})
	}
}

// scenario: unknown-key-rejected — a misspelled/unknown key ⇒ strict-decode fault.Invalid.
func TestScenarioUnknownKeyRejected(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	require.NoError(t, os.WriteFile(good, []byte("server:\n  listenAddr: \"0.0.0.0:9000\"\n"), 0o600))
	bad := filepath.Join(dir, "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("server:\n  listen: \"0.0.0.0:9000\"\n"), 0o600)) // typo: listen

	f, err := config.Load(good)
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:9000", f.Server.ListenAddr)

	_, err = config.Load(bad)
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an unknown key is rejected, not ignored")
}

// Locate: an explicit/env path that doesn't exist is fault.NotFound; no path ⇒ "" (zero-config).
func TestLocate(t *testing.T) {
	_, err := config.Locate(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "an explicit missing path is fault.NotFound")

	present := filepath.Join(t.TempDir(), "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(present, []byte("{}"), 0o600))
	got, err := config.Locate(present)
	require.NoError(t, err)
	require.Equal(t, present, got)

	// no explicit, no FUNCD_CONFIG, cwd has no funcdconfig.yaml ⇒ "" (zero-config). Run from an empty dir.
	t.Setenv("FUNCD_CONFIG", "")
	t.Chdir(t.TempDir())
	got, err = config.Locate("")
	require.NoError(t, err)
	require.Equal(t, "", got, "no file found ⇒ zero-config")
}
