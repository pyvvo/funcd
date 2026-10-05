package config_test

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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

// Every config key has a FUNCD_* env override (ADR-0062 Decision 4), the ADR-0111/0114/0115 keys too;
// lists split on "," and maps on "," + "=" as FUNCD_IMAGE_OVERRIDE does (issue #438).
func TestIssue438_EveryKeyHasEnvOverride(t *testing.T) {
	var missing []string
	var walk func(prefix string, typ reflect.Type)
	walk = func(prefix string, typ reflect.Type) {
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			switch {
			case f.Type.Kind() == reflect.Struct:
				walk(prefix+name+".", f.Type)
			case prefix == "" && (name == "apiVersion" || name == "kind"):
			case f.Tag.Get("env") == "" || f.Tag.Get("env") == "-":
				missing = append(missing, prefix+name)
			}
		}
	}
	walk("", reflect.TypeFor[config.Config]())
	require.Equalf(t, []string{"auth.credentials"}, missing, "config keys without a FUNCD_* env override (auth.credentials is file-only, ADR-0171)")

	t.Setenv("FUNCD_TLS_HOSTS", "a.example,b.example")
	t.Setenv("FUNCD_SHAPING_CORS_ALLOW_ORIGINS", "https://a.example,https://b.example")
	t.Setenv("FUNCD_SHAPING_CORS_ALLOW_METHODS", "GET,POST")
	t.Setenv("FUNCD_SHAPING_CORS_ALLOW_HEADERS", "X-A,X-B")
	t.Setenv("FUNCD_SHAPING_CORS_MAX_AGE_SECONDS", "600")
	t.Setenv("FUNCD_SHAPING_HEADERS_SET", "X-Frame-Options=DENY,Strict-Transport-Security=max-age=31536000")
	t.Setenv("FUNCD_SHAPING_HEADERS_REMOVE", "Server,X-Powered-By")
	t.Setenv("FUNCD_NETWORK_INTERNAL_ALLOW", "10.63.0.1:9000,10.63.0.1:4317")
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, []string{"a.example", "b.example"}, c.Server.TLS.Hosts)
	cors := c.Server.Shaping.CORS
	require.Equal(t, []string{"https://a.example", "https://b.example"}, cors.AllowOrigins)
	require.Equal(t, []string{"GET", "POST"}, cors.AllowMethods)
	require.Equal(t, []string{"X-A", "X-B"}, cors.AllowHeaders)
	require.Equal(t, 600, cors.MaxAgeSeconds)
	require.Equal(t, map[string]string{"X-Frame-Options": "DENY", "Strict-Transport-Security": "max-age=31536000"}, c.Server.Shaping.Headers.Set)
	require.Equal(t, []string{"Server", "X-Powered-By"}, c.Server.Shaping.Headers.Remove)
	require.Equal(t, []string{"10.63.0.1:9000", "10.63.0.1:4317"}, c.Server.Network.InternalAllow)

	t.Setenv("FUNCD_SHAPING_CORS_MAX_AGE_SECONDS", "-1")
	_, err = config.Load("", config.Flags{})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an env-sourced negative maxAgeSeconds is rejected")
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

// A negative server.limits value is rejected from the file and from env; 0 (ADR-0112's "off") still loads.
func TestIssue164_NegativeLimitsRejected(t *testing.T) {
	for key, envName := range map[string]string{
		"ratePerMin":   "FUNCD_LIMITS_RATE_PER_MIN",
		"burst":        "FUNCD_LIMITS_BURST",
		"maxBodyBytes": "FUNCD_LIMITS_MAX_BODY_BYTES",
		"maxInFlight":  "FUNCD_LIMITS_MAX_IN_FLIGHT",
	} {
		t.Run(key+"/file", func(t *testing.T) {
			_, err := config.Load(writeCfg(t, "server:\n  limits:\n    "+key+": -1\n"), config.Flags{})
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a negative %s is rejected", key)
			require.ErrorContains(t, err, "server.limits."+key)
		})
		t.Run(key+"/env", func(t *testing.T) {
			t.Setenv(envName, "-5")
			_, err := config.Load("", config.Flags{})
			require.Equal(t, fault.Invalid, fault.KindOf(err), "a negative %s is rejected", envName)
		})
		t.Run(key+"/zero", func(t *testing.T) {
			_, err := config.Load(writeCfg(t, "server:\n  limits:\n    "+key+": 0\n"), config.Flags{})
			require.NoError(t, err, "0 is the documented off")
		})
	}
}

// scenario: max-keys-from-config
func TestScenarioMaxKeysFromConfig(t *testing.T) {
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 4096, c.Server.Limits.MaxKeys, "unset gives the default table size")

	c, err = config.Load(writeCfg(t, "server:\n  limits:\n    maxKeys: 2\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 2, c.Server.Limits.MaxKeys, "from the file")

	t.Run("env", func(t *testing.T) {
		t.Setenv("FUNCD_LIMITS_MAX_KEYS", "2")
		c, err := config.Load("", config.Flags{})
		require.NoError(t, err)
		require.Equal(t, 2, c.Server.Limits.MaxKeys)
	})
	for _, v := range []string{"0", "-1"} {
		t.Run("file/"+v, func(t *testing.T) {
			_, err := config.Load(writeCfg(t, "server:\n  limits:\n    maxKeys: "+v+"\n"), config.Flags{})
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, "server.limits.maxKeys")
		})
		t.Run("env/"+v, func(t *testing.T) {
			t.Setenv("FUNCD_LIMITS_MAX_KEYS", v)
			_, err := config.Load("", config.Flags{})
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, "server.limits.maxKeys")
		})
	}
}

// The zero-config workflow.payloadLimit is ADR-0094's 256 KiB; an explicit value still overrides it.
func TestIssue343_WorkflowPayloadLimitDefault256KiB(t *testing.T) {
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, int64(256<<10), c.Workflow.PayloadLimit, "ADR-0094 default payload cap")

	t.Setenv("FUNCD_WORKFLOW_PAYLOAD_LIMIT", "1048576")
	c, err = config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, int64(1<<20), c.Workflow.PayloadLimit, "an explicit cap overrides the default")
}

// workflow.maxStepsInFlight defaults to 64 (ADR-0146); the env key sets it, 0 included (no cap), and a
// negative value is rejected.
func TestWorkflowMaxStepsInFlight(t *testing.T) {
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 64, c.Workflow.MaxStepsInFlight)

	t.Setenv("FUNCD_WORKFLOW_MAX_STEPS_IN_FLIGHT", "0")
	c, err = config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 0, c.Workflow.MaxStepsInFlight, "0 is the no-cap off switch")

	t.Setenv("FUNCD_WORKFLOW_MAX_STEPS_IN_FLIGHT", "-1")
	_, err = config.Load("", config.Flags{})
	require.Error(t, err)
}

//go:embed config.go
var configSource string

// The Workflow block's doc describes Retention and PayloadLimit as enforced, not as reserved for later gates.
func TestIssue345_WorkflowDocSaysRetentionAndPayloadLimitEnforced(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "config.go", configSource, parser.ParseComments)
	require.NoError(t, err)
	var doc string
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Config" {
			return true
		}
		for _, fld := range ts.Type.(*ast.StructType).Fields.List {
			if len(fld.Names) == 1 && fld.Names[0].Name == "Workflow" {
				doc = fld.Doc.Text()
			}
		}
		return false
	})
	require.NotEmpty(t, doc, "Config.Workflow carries a doc comment")
	for _, key := range []string{"Retention", "PayloadLimit"} {
		require.Contains(t, doc, key, "the doc names what %s does", key)
	}
	for _, stale := range []string{"not yet enforced", "reserved"} {
		require.NotContains(t, strings.ToLower(doc), stale, "Retention and PayloadLimit are enforced (ADR-0094)")
	}
}

// An out-of-range port, or a negative size or count, is rejected with a field error instead of
// wrapping in a uint16 conversion or turning a cap off; a negative maxStoresPerNamespace keeps its
// documented meaning (quota off, ADR-0072).
func TestIssue326_OutOfRangeNumbersRejected(t *testing.T) {
	yamlAt := func(key, val string) string {
		var b strings.Builder
		parts := strings.Split(key, ".")
		for i, p := range parts {
			b.WriteString(strings.Repeat("  ", i) + p + ":")
			if i == len(parts)-1 {
				b.WriteString(" " + val)
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	for _, tc := range []struct {
		key       string
		bad, good []string
	}{
		{"server.network.egressGatewayPort", []string{"-1", "65536"}, []string{"0", "65535"}},
		{"server.network.dnsForwarderPort", []string{"-1", "65536"}, []string{"0", "65535"}},
		{"server.shaping.cors.maxAgeSeconds", []string{"-1"}, []string{"0"}},
		{"kvstore.backup.chunkBytes", []string{"-1"}, []string{"0"}},
		{"funclog.segmentMaxBytes", []string{"-1"}, []string{"0"}},
		{"s3gateway.maxUploadBytes", []string{"-1"}, []string{"0"}},
		{"workflow.defaultRetry", []string{"-1"}, []string{"0"}},
		{"workflow.payloadLimit", []string{"-1"}, []string{"0"}},
		{"eventing.deliveryAttempts", []string{"-1"}, []string{"0"}},
		{"eventing.deadletter.maxEntries", []string{"-1"}, []string{"0"}},
		{"kvstore.maxStoresPerNamespace", nil, []string{"-1", "0"}},
	} {
		for _, v := range tc.bad {
			t.Run(tc.key+"="+v, func(t *testing.T) {
				_, err := config.Load(writeCfg(t, yamlAt(tc.key, v)), config.Flags{})
				require.Equal(t, fault.Invalid, fault.KindOf(err), "%s=%s is rejected", tc.key, v)
				require.ErrorContains(t, err, tc.key)
			})
		}
		for _, v := range tc.good {
			t.Run(tc.key+"="+v, func(t *testing.T) {
				_, err := config.Load(writeCfg(t, yamlAt(tc.key, v)), config.Flags{})
				require.NoError(t, err, "%s=%s is in range", tc.key, v)
			})
		}
	}
	t.Run("env", func(t *testing.T) {
		t.Setenv("FUNCD_NETWORK_DNS_FORWARDER_PORT", "65536")
		_, err := config.Load("", config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), "an out-of-range port from env is rejected")
	})
}

// scenario: unknown-key-rejected — a misspelled key ⇒ strict-decode fault.Invalid.
func TestScenarioUnknownKeyRejected(t *testing.T) {
	_, err := config.Load(writeCfg(t, "server:\n  listen: \"0.0.0.0:9000\"\n"), config.Flags{}) // typo: listen
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an unknown key is rejected, not ignored")
}

type vc struct {
	value string
	valid bool
}

type matrixField struct {
	name  string
	set   func(*config.Config, string)
	cases []vc
}

// validateMatrix is TestValidateMatrix's case table; TestIssue435_MatrixCoversEveryEnumField holds it
// to every enum field.
func validateMatrix() []matrixField {
	return []matrixField{
		// omitempty,eq — empty is VALID (the envelope is optional), only the exact tag passes.
		{"apiVersion", func(c *config.Config, v string) { c.APIVersion = v }, []vc{
			{"", true}, {"funcd.io/v1alpha1", true}, {"funcd.io/v2", false},
		}},
		{"kind", func(c *config.Config, v string) { c.Kind = v }, []vc{
			{"", true}, {"FuncdConfig", true}, {"Function", false},
		}},
		// omitempty,oneof — empty is VALID: every member passes, everything else fails.
		{"server.tls.mode", func(c *config.Config, v string) { c.Server.TLS.Mode = v }, []vc{
			{"", true}, {"selfsigned", true}, {"provided", true}, {"acme", true},
			{"letsencrypt", false}, {"ACME", false},
		}},
		{"server.limits.key", func(c *config.Config, v string) { c.Server.Limits.Key = v }, []vc{
			{"", true}, {"clientIP", true}, {"function", true}, {"clientip", false}, {"header", false},
		}},
		{"kvstore.engine", func(c *config.Config, v string) { c.Kvstore.Engine = v }, []vc{
			{"", true}, {"memory", true}, {"badger", true}, {"file", false}, {"Badger", false},
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
	}
}

// the validator matrix — Config.Validate() is the single value gate (source-agnostic, ADR-0062).
// For every enum field (an `eq` or `oneof` validate tag), enumerate its ACCEPTED set (must pass) and
// representative REJECTED values (must be fault.Invalid), mutating one field off a valid base. The
// numeric bounds are covered by TestIssue164_NegativeLimitsRejected and
// TestIssue326_OutOfRangeNumbersRejected, storage.dataDir by TestIssue332_EmptyDataDirRejected and
// site.defaultIndex by TestScenarioSiteDefaultIndexConfig.
// This is the parametrized form: each {field, value, valid} is one case (the analog of pytest's
// @parametrize). It subsumes the old TestConfigValidate (good passes + a bad enum is rejected).
func TestValidateMatrix(t *testing.T) {
	base, err := config.Load("", config.Flags{})
	require.NoError(t, err)

	for _, field := range validateMatrix() {
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

// Every enum field has matrix cases with a rejected value, so a field added without them fails here.
func TestIssue435_MatrixCoversEveryEnumField(t *testing.T) {
	rejected := map[string]bool{}
	for _, field := range validateMatrix() {
		for _, c := range field.cases {
			rejected[field.name] = rejected[field.name] || !c.valid
		}
	}
	var missing []string
	for _, key := range enumKeys("", reflect.TypeFor[config.Config]()) {
		if !rejected[key] {
			missing = append(missing, key)
		}
	}
	require.Empty(t, missing, "enum fields without a rejected value in TestValidateMatrix")
}

// enumKeys returns the dotted yaml key of every field of t whose validate tag has an eq or oneof rule.
func enumKeys(prefix string, t reflect.Type) []string {
	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Type.Kind() == reflect.Struct {
			keys = append(keys, enumKeys(prefix+name+".", f.Type)...)
			continue
		}
		for rule := range strings.SplitSeq(f.Tag.Get("validate"), ",") {
			if strings.HasPrefix(rule, "eq=") || strings.HasPrefix(rule, "oneof=") {
				keys = append(keys, prefix+name)
			}
		}
	}
	return keys
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

// An explicit empty storage.dataDir is rejected at Load, naming the key, instead of deriving relative
// store paths ("store", "kv", …) that fail later at startup with a message that does not name it.
func TestIssue332_EmptyDataDirRejected(t *testing.T) {
	_, err := config.Load(writeCfg(t, "storage:\n  dataDir: \"\"\n"), config.Flags{})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "an empty storage.dataDir is rejected")
	require.ErrorContains(t, err, "storage.dataDir")
}

// A server.shaping.headers.set key that is not an HTTP header field name is rejected from the env and
// the file. The env form splits on ",", so a comma in a value used to load a bogus " max-age" key that
// net/http then dropped from the response.
func TestIssue509_InvalidHeaderSetNameRejected(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		t.Setenv("FUNCD_SHAPING_HEADERS_SET", "Cache-Control=no-store, max-age=0")
		_, err := config.Load("", config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), "a comma inside an env header value is rejected")
		require.ErrorContains(t, err, "server.shaping.headers.set")
	})
	t.Run("file", func(t *testing.T) {
		_, err := config.Load(writeCfg(t, "server:\n  shaping:\n    headers:\n      set:\n        \"X Bad\": v\n"), config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err), "a header name with a space is rejected")
		require.ErrorContains(t, err, "server.shaping.headers.set")
	})
	t.Run("comma value in file", func(t *testing.T) {
		c, err := config.Load(writeCfg(t, "server:\n  shaping:\n    headers:\n      set:\n        Cache-Control: \"no-store, max-age=0\"\n"), config.Flags{})
		require.NoError(t, err)
		require.Equal(t, map[string]string{"Cache-Control": "no-store, max-age=0"}, c.Server.Shaping.Headers.Set)
	})
}

// invoke.maxNestedInFlight (ADR-0147) loads from the file and from FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT; 0 stays 0
// (the facade's default 10) and a negative value is Invalid at load.
func TestInvokeMaxNestedInFlightConfig(t *testing.T) {
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Zero(t, c.Invoke.MaxNestedInFlight, "zero-config leaves the facade default")

	c, err = config.Load(writeCfg(t, "invoke:\n  maxNestedInFlight: 20\n"), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 20, c.Invoke.MaxNestedInFlight)

	t.Setenv("FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT", "7")
	c, err = config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, 7, c.Invoke.MaxNestedInFlight)

	t.Setenv("FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT", "-1")
	_, err = config.Load("", config.Flags{})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a negative env value is rejected")

	t.Setenv("FUNCD_INVOKE_MAX_NESTED_IN_FLIGHT", "")
	_, err = config.Load(writeCfg(t, "invoke:\n  maxNestedInFlight: -1\n"), config.Flags{})
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a negative file value is rejected")
	require.ErrorContains(t, err, "invoke.maxNestedInFlight")
}

// ADR-0151: invoke.defaultTimeout is set from the config file and from FUNCD_INVOKE_DEFAULT_TIMEOUT.
func TestInvokeDefaultTimeoutConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte("invoke:\n  defaultTimeout: 90s\n"), 0o600))
	c, err := config.Load(path, config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "90s", c.Invoke.DefaultTimeout)

	t.Setenv("FUNCD_INVOKE_DEFAULT_TIMEOUT", "2m")
	c, err = config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Equal(t, "2m", c.Invoke.DefaultTimeout)
}

// scenario: invalid-delivery-setting-refused (ADR-0156) — each delivery queue size is at least 1 and the
// per-target cap at most the workers; a bad value fails the load naming its key.
func TestScenarioInvalidDeliverySettingRefused(t *testing.T) {
	for _, tc := range []struct {
		yaml, key, value string
	}{
		{"maxInFlightPerTarget: 40", "eventing.maxInFlightPerTarget", "40"},
		{"maxDeliveriesInFlight: 2", "eventing.maxInFlightPerTarget", "4"},
		{"maxDeliveriesInFlight: 0", "eventing.maxDeliveriesInFlight", "0"},
	} {
		t.Run(tc.yaml, func(t *testing.T) {
			_, err := config.Load(writeCfg(t, "eventing:\n  "+tc.yaml+"\n"), config.Flags{})
			require.Equal(t, fault.Invalid, fault.KindOf(err))
			require.ErrorContains(t, err, `config key "`+tc.key+`" has invalid value "`+tc.value+`"`)
		})
	}
	t.Run("FUNCD_EVENTING_MAX_QUEUED_PER_SENSOR=0", func(t *testing.T) {
		t.Setenv("FUNCD_EVENTING_MAX_QUEUED_PER_SENSOR", "0")
		_, err := config.Load("", config.Flags{})
		require.Equal(t, fault.Invalid, fault.KindOf(err))
		require.ErrorContains(t, err, `config key "eventing.maxQueuedPerSensor" has invalid value "0"`)
	})
	t.Run("defaults", func(t *testing.T) {
		c, err := config.Load("", config.Flags{})
		require.NoError(t, err)
		require.Equal(t, []int{32, 4, 4096}, []int{c.Eventing.MaxDeliveriesInFlight, c.Eventing.MaxInFlightPerTarget, c.Eventing.MaxQueuedPerSensor})
	})
}

const twoCredentials = `auth:
  credentials:
    - tokenFile: /etc/funcd/tokens/ops
      role: admin
    - tokenFile: tokens/team-a
      role: developer
      namespaces:
        - team-a
`

// ADR-0171 Decision 1: the two-entry list decodes; namespaces stay unset until cmd/funcd fills in default.
func TestCredentialsDecode(t *testing.T) {
	c, err := config.Load(writeCfg(t, twoCredentials), config.Flags{})
	require.NoError(t, err)
	require.Equal(t, config.CredentialList{
		{TokenFile: "/etc/funcd/tokens/ops", Role: "admin"},
		{TokenFile: "tokens/team-a", Role: "developer", Namespaces: []string{"team-a"}},
	}, c.Auth.Credentials)

	c, err = config.Load(writeCfg(t, twoCredentials+"  namespaces:\n    - default\n"), config.Flags{})
	require.NoError(t, err, "an explicit auth.namespaces of default is the default value, so it passes")
	require.Len(t, c.Auth.Credentials, 2)

	c, err = config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Nil(t, c.Auth.Credentials, "absent key ⇒ nil, the shorthand path")
}

// ADR-0171 Decision 1: auth.credentials has no env var, so a stray indexed variable adds no entry.
func TestCredentialsIgnoreIndexedEnv(t *testing.T) {
	for _, k := range []string{"0_TOKENFILE", "0_ROLE", "1_TOKENFILE", "1_ROLE", "0_NAMESPACES"} {
		t.Setenv(k, "admin")
	}
	c, err := config.Load("", config.Flags{})
	require.NoError(t, err)
	require.Nil(t, c.Auth.Credentials)
}

// ADR-0171 Decisions 1 and 4: a malformed list or a list beside the shorthand fails naming its key.
func TestCredentialsRejected(t *testing.T) {
	for _, tc := range []struct {
		name, body, envKey, envVal, key string
	}{
		{"missing-tokenFile", "auth:\n  credentials:\n    - role: admin\n", "", "", "auth.credentials[0].tokenFile"},
		{"unknown-role", "auth:\n  credentials:\n    - tokenFile: t\n      role: root\n", "", "", "auth.credentials[0].role"},
		{"unknown-entry-key", "auth:\n  credentials:\n    - tokenFile: t\n      role: admin\n      token: inline-secret\n", "", "", "auth.credentials[0]"},
		{"unquoted-numeric-namespace", "auth:\n  credentials:\n    - tokenFile: t\n      role: viewer\n      namespaces:\n        - 123\n", "", "", "auth.credentials[0]"},
		{"empty-list", "auth:\n  credentials: []\n", "", "", "auth.credentials"},
		{"commented-out-entries", "auth:\n  credentials:\n    # - tokenFile: t\n    #   role: admin\n", "", "", "auth.credentials"},
		{"token-in-file", twoCredentials + "  token: shorthand-secret\n", "", "", "auth.token (FUNCD_TOKEN)"},
		{"token-in-env", twoCredentials, "FUNCD_TOKEN", "shorthand-secret", "auth.token (FUNCD_TOKEN)"},
		{"namespaces-in-file", twoCredentials + "  namespaces:\n    - team-a\n", "", "", "auth.namespaces (FUNCD_AUTH_NAMESPACES)"},
		{"namespaces-in-env", twoCredentials, "FUNCD_AUTH_NAMESPACES", "team-a,team-b", "auth.namespaces (FUNCD_AUTH_NAMESPACES)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envKey != "" {
				t.Setenv(tc.envKey, tc.envVal)
			}
			_, err := config.Load(writeCfg(t, tc.body), config.Flags{})
			require.Error(t, err)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.Contains(t, err.Error(), tc.key)
			require.NotContains(t, err.Error(), "secret", "no token value in the error")
		})
	}
}
