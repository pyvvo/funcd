package config_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return p
}

// scenario: zero-config-defaults — no file + no env ⇒ every field its built-in default (nested struct).
func TestScenarioZeroConfigDefaults(t *testing.T) {
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:8080", c.Server.ListenAddr)
	require.Equal(t, "127.0.0.1:0", c.Server.DataPlaneAddr)
	require.Equal(t, "file", c.Storage.Mode)
	require.Equal(t, "/var/lib/funcd", c.Storage.DataDir)
	require.Equal(t, []string{"default"}, c.Auth.Namespaces)
	require.Equal(t, "process", c.Runtime.Mode)
	require.Equal(t, "overlayfs", c.Runtime.Containerd.Snapshotter)
	require.Equal(t, "/opt/cni/bin", c.Runtime.Containerd.CNIBinDir)
	require.Equal(t, "10.63.0.0/16", c.Runtime.Containerd.SubnetCIDR)
	require.Equal(t, "funcd/runtime-", c.Runtime.Containerd.ImagePrefix)
	require.Equal(t, "/var/lib/funcd/containerd", c.Runtime.Containerd.Root, "dataDir-derived")
	require.Equal(t, "/var/lib/funcd/cni", c.Runtime.Containerd.CNIConfDir, "dataDir-derived")
	require.Equal(t, "json", c.Log.Format)
	require.Equal(t, "info", c.Log.Level)
	require.Equal(t, "", c.Telemetry.Endpoint)
	require.Equal(t, "index.html", c.Site.DefaultIndex, "ADR-0139 site default index")
	require.True(t, c.Funclog.Enabled, "ADR-0081 capture on by default")
	require.True(t, c.Funclog.Traces, "ADR-0101 traces on by default")
	require.Equal(t, "funcd-system", c.Funclog.Bucket)
}

// scenario: site-default-index-config (ADR-0139) — site.defaultIndex is a config-level knob with the web
// convention as its default, overridable by FUNCD_SITE_DEFAULT_INDEX, and a leading '/' is rejected.
func TestScenarioSiteDefaultIndexConfig(t *testing.T) {
	t.Setenv("FUNCD_SITE_DEFAULT_INDEX", "home.htm")
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "home.htm", c.Site.DefaultIndex)

	t.Setenv("FUNCD_SITE_DEFAULT_INDEX", "/index.html")
	_, err = config.Load("", config.Flags{})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an absolute default index is rejected")
}

// the precedence matrix — flag > env > file > default — parameterized across the tiers (ADR-0062).
// Each row activates a subset of the source tiers and asserts the highest active one wins.
func TestPrecedence(t *testing.T) {
	flag := func(b bool) *bool { return &b }
	dataDir := func(c config.Config) string { return c.Storage.DataDir }
	mode := func(c config.Config) string { return c.Storage.Mode }

	for _, tc := range []struct {
		name string
		file string            // funcdconfig.yaml body ("" ⇒ no file)
		env  map[string]string // FUNCD_* vars to set
		flag *bool             // --memory
		get  func(config.Config) string
		want string
	}{
		{"default", "", nil, nil, dataDir, "/var/lib/funcd"},
		{"file > default", "storage:\n  dataDir: /file\n", nil, nil, dataDir, "/file"},
		{"env > default", "", map[string]string{"FUNCD_DATA_DIR": "/env"}, nil, dataDir, "/env"},
		{"env > file", "storage:\n  dataDir: /file\n", map[string]string{"FUNCD_DATA_DIR": "/env"}, nil, dataDir, "/env"},
		{"flag > default", "", nil, flag(true), mode, "memory"},
		{"flag > file", "storage:\n  mode: file\n", nil, flag(true), mode, "memory"},
		{"flag > env", "", map[string]string{"FUNCD_STORAGE_MODE": "file"}, flag(true), mode, "memory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			path := ""
			if tc.file != "" {
				path = writeCfg(t, tc.file)
			}
			c, err := config.Load(path, config.Flags{MemoryOnly: tc.flag})
			require.NoError(t, err)
			require.Equal(t, tc.want, tc.get(c), "the highest active source tier wins")
		})
	}
}

// no-clobber: an env var overrides ONLY its own key — the file's other values survive (the property
// the whole approach hinges on: caarlos0/env leaves a field untouched when its var is unset).
func TestEnvDoesNotClobberFile(t *testing.T) {
	path := writeCfg(t, "server:\n  listenAddr: \"1.2.3.4:9000\"\nstorage:\n  dataDir: /file-dir\nlog:\n  level: debug\n")
	t.Setenv("FUNCD_DATA_DIR", "/env-dir") // set only this one env
	c, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "/env-dir", c.Storage.DataDir, "the env key is overridden")
	require.Equal(t, "1.2.3.4:9000", c.Server.ListenAddr, "the file's listenAddr is NOT clobbered")
	require.Equal(t, "debug", c.Log.Level, "the file's log.level is NOT clobbered")
}

// scenario: env-value-validated (the closed edge) — a bad value from an env var is validated, not
// silently defaulted (the ADR-0061 gap, closed by validating the merged struct).
func TestScenarioEnvValueValidated(t *testing.T) {
	t.Setenv("FUNCD_RUNTIME", "bogus")
	_, err := config.Load("", config.Flags{})
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a bad env-sourced enum is rejected")
}

// every FUNCD_* env var maps to its field (the env overlay covers the whole surface uniformly).
func TestEnvVarsMapToFields(t *testing.T) {
	cases := map[string]struct {
		val string
		get func(config.Config) string
	}{
		"FUNCD_LISTEN_ADDR":                 {"1.1.1.1:1", func(c config.Config) string { return c.Server.ListenAddr }},
		"FUNCD_DATA_PLANE_ADDR":             {"2.2.2.2:2", func(c config.Config) string { return c.Server.DataPlaneAddr }},
		"FUNCD_STORAGE_MODE":                {"memory", func(c config.Config) string { return c.Storage.Mode }},
		"FUNCD_DATA_DIR":                    {"/d", func(c config.Config) string { return c.Storage.DataDir }},
		"FUNCD_TOKEN":                       {"tok", func(c config.Config) string { return c.Auth.Token }},
		"FUNCD_SECRETS_ENCRYPTION_KEY_FILE": {"/k", func(c config.Config) string { return c.Secrets.EncryptionKeyFile }},
		"FUNCD_RUNTIME":                     {"containerd", func(c config.Config) string { return c.Runtime.Mode }},
		"FUNCD_CONTAINERD_SOCKET":           {"/s", func(c config.Config) string { return c.Runtime.Containerd.Socket }},
		"FUNCD_SNAPSHOTTER":                 {"native", func(c config.Config) string { return c.Runtime.Containerd.Snapshotter }},
		"FUNCD_CNI_BIN_DIR":                 {"/cni", func(c config.Config) string { return c.Runtime.Containerd.CNIBinDir }},
		"FUNCD_SUBNET_CIDR":                 {"10.0.0.0/8", func(c config.Config) string { return c.Runtime.Containerd.SubnetCIDR }},
		"FUNCD_IMAGE_PREFIX":                {"my/", func(c config.Config) string { return c.Runtime.Containerd.ImagePrefix }},
		"FUNCD_LOG_FORMAT":                  {"text", func(c config.Config) string { return c.Log.Format }},
		"FUNCD_LOG_LEVEL":                   {"warn", func(c config.Config) string { return c.Log.Level }},
		"FUNCD_TELEMETRY_ENDPOINT":          {"otel:4317", func(c config.Config) string { return c.Telemetry.Endpoint }},
		"FUNCD_FUNCLOG_SEGMENT_MAX_AGE":     {"2s", func(c config.Config) string { return c.Funclog.SegmentMaxAge }},
		"FUNCD_FUNCLOG_ENABLED":             {"false", func(c config.Config) string { return strconv.FormatBool(c.Funclog.Enabled) }},
	}
	for envName, tc := range cases {
		t.Run(envName, func(t *testing.T) {
			t.Setenv(envName, tc.val)
			c, err := config.Load("", config.Flags{})
			require.NoError(t, err)
			require.Equal(t, tc.val, tc.get(c))
		})
	}
}

// FUNCD_IMAGE_OVERRIDE ("rt=ref,rt=ref") is parsed into the map by the lib (envSeparator/KeyVal).
func TestImageOverrideEnvParsed(t *testing.T) {
	t.Setenv("FUNCD_IMAGE_OVERRIDE", "nodejs22=reg/node:1,python314=reg/py:2")
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "reg/node:1", c.Runtime.Containerd.ImageOverride["nodejs22"])
	require.Equal(t, "reg/py:2", c.Runtime.Containerd.ImageOverride["python314"])
}

// FUNCD_AUTH_NAMESPACES is a comma-separated list (envSeparator).
func TestNamespacesEnvList(t *testing.T) {
	t.Setenv("FUNCD_AUTH_NAMESPACES", "a,b,c")
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, c.Auth.Namespaces)
}

// scenario: invalid-enum-rejected — a bad enum from the file ⇒ fault.Invalid naming the key.
func TestScenarioInvalidEnumRejected(t *testing.T) {
	for name, body := range map[string]string{
		"storage.mode": "storage:\n  mode: bogus\n",
		"runtime.mode": "runtime:\n  mode: vm\n",
		"log.format":   "log:\n  format: xml\n",
		"log.level":    "log:\n  level: loud\n",
		"apiVersion":   "apiVersion: funcd.io/v2\n",
		"kind":         "kind: Function\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := config.Load(writeCfg(t, body), config.Flags{})
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a bad %s is fault.Invalid", name)
		})
	}
}

// scenario: unknown-key-rejected — a misspelled key ⇒ strict-decode fault.Invalid.
func TestScenarioUnknownKeyRejected(t *testing.T) {
	_, err := config.Load(writeCfg(t, "server:\n  listen: \"0.0.0.0:9000\"\n"), config.Flags{}) // typo: listen
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an unknown key is rejected, not ignored")
}

// the validator matrix — Config.Validate() is the single value gate (source-agnostic, ADR-0062).
// For every field carrying a `validate` tag, enumerate its ACCEPTED set (must pass) and
// representative REJECTED values (must be fault.Invalid), mutating one field off a valid base.
// This is the parametrized form: each {field, value, valid} is one case (the analog of pytest's
// @parametrize). It subsumes the old TestConfigValidate (good passes + a bad enum is rejected).
func TestValidateMatrix(t *testing.T) {
	base, err := config.Load("", config.Flags{})
	require.NoError(t, err)

	type vc struct {
		value string
		valid bool
	}
	for _, field := range []struct {
		name  string
		set   func(*config.Config, string)
		cases []vc
	}{
		// omitempty,eq — empty is VALID (the envelope is optional), only the exact tag passes.
		{"apiVersion", func(c *config.Config, v string) { c.APIVersion = v }, []vc{
			{"", true}, {"funcd.io/v1alpha1", true}, {"funcd.io/v2", false},
		}},
		{"kind", func(c *config.Config, v string) { c.Kind = v }, []vc{
			{"", true}, {"FuncdConfig", true}, {"Function", false},
		}},
		// oneof — empty is INVALID (no omitempty): every member passes, everything else fails.
		{"storage.mode", func(c *config.Config, v string) { c.Storage.Mode = v }, []vc{
			{"file", true}, {"memory", true}, {"", false}, {"disk", false},
		}},
		{"runtime.mode", func(c *config.Config, v string) { c.Runtime.Mode = v }, []vc{
			{"process", true}, {"containerd", true}, {"", false}, {"vm", false},
		}},
		{"log.format", func(c *config.Config, v string) { c.Log.Format = v }, []vc{
			{"json", true}, {"text", true}, {"xml", false}, {"JSON", false}, {"", false},
		}},
		{"log.level", func(c *config.Config, v string) { c.Log.Level = v }, []vc{
			{"debug", true}, {"info", true}, {"warn", true}, {"error", true},
			{"loud", false}, {"INFO", false}, {"", false},
		}},
		{"funclog.bucket", func(c *config.Config, v string) { c.Funclog.Bucket = v }, []vc{
			{"funcd-system", true}, {"", true}, {"logs", false},
		}},
	} {
		for _, c := range field.cases {
			t.Run(field.name+"="+strconv.Quote(c.value), func(t *testing.T) {
				cfg := base
				field.set(&cfg, c.value)
				err := cfg.Validate()
				if c.valid {
					require.NoError(t, err, "%s=%q must be accepted", field.name, c.value)
				} else {
					require.Equal(t, fault.Invalid, fault.KindOf(err),
						"%s=%q must be rejected", field.name, c.value)
				}
			})
		}
	}
}

// Locate: an explicit/env path that doesn't exist ⇒ fault.NotFound; none ⇒ "" (zero-config).
func TestLocate(t *testing.T) {
	_, err := config.Locate(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "an explicit missing path is fault.NotFound")

	present := writeCfg(t, "{}")
	got, err := config.Locate(present)
	require.NoError(t, err)
	require.Equal(t, present, got)

	t.Setenv("FUNCD_CONFIG", "")
	t.Chdir(t.TempDir())
	got, err = config.Locate("")
	require.NoError(t, err)
	require.Equal(t, "", got, "no file found ⇒ zero-config")
}
