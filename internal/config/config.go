// Package config is the funcd daemon config file — funcdconfig.yaml (ADR-0061). It LOCATES,
// LOADS, and RESOLVES the optional operator config into a flat, fully-defaulted Resolved value
// with precedence flag > env (FUNCD_*) > file > built-in default. It is a leaf: it imports no
// drivers (only stdlib + sigs.k8s.io/yaml + api/fault); cmd/funcd maps Resolved → []funcd.Option,
// keeping the pkg/funcd Option surface + presets untouched.
//
// The file is OPTIONAL — every key has a default, so zero-config startup is unchanged. Decoding is
// strict (sigs.k8s.io/yaml UnmarshalStrict, reusing the repo's json tags): an unknown key is an
// operator typo and is rejected, not silently dropped.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/green-0-rabbit/funcd/api/fault"
	"sigs.k8s.io/yaml"
)

// APIVersion / Kind are the optional funcdconfig.yaml envelope (validated only when present).
const (
	APIVersion = "funcd.io/v1alpha1"
	Kind       = "FuncdConfig"
)

// built-in defaults (the bottom precedence tier; identical to today's preset behavior).
const (
	defaultListenAddr    = "0.0.0.0:8080"
	defaultDataPlaneAddr = "127.0.0.1:0"
	defaultStorageMode   = "file"
	defaultDataDir       = "/var/lib/funcd"
	defaultRuntimeMode   = "process"
	defaultSnapshotter   = "overlayfs"
	defaultCNIBinDir     = "/opt/cni/bin"
	defaultSubnetCIDR    = "10.63.0.0/16"
	defaultImagePrefix   = "funcd/runtime-"
	defaultLogFormat     = "json"
	defaultLogLevel      = "info"
	defaultNamespace     = "default"
)

// File is the decoded funcdconfig.yaml (zero value ⇒ nothing set; all defaults apply). json tags,
// so sigs.k8s.io/yaml (yaml→json→struct) reuses them.
type File struct {
	APIVersion string    `json:"apiVersion,omitempty" validate:"omitempty,eq=funcd.io/v1alpha1"`
	Kind       string    `json:"kind,omitempty" validate:"omitempty,eq=FuncdConfig"`
	Server     Server    `json:"server,omitempty"`
	Storage    Storage   `json:"storage,omitempty"`
	Auth       Auth      `json:"auth,omitempty"`
	Secrets    Secrets   `json:"secrets,omitempty"`
	Runtime    Runtime   `json:"runtime,omitempty"`
	Log        Log       `json:"log,omitempty"`
	Telemetry  Telemetry `json:"telemetry,omitempty"`
}

// Server is the control-plane + data-plane bind addresses.
type Server struct {
	ListenAddr    string `json:"listenAddr,omitempty"`
	DataPlaneAddr string `json:"dataPlaneAddr,omitempty"`
}

// Storage selects the blob+bus substrate (ADR-0043) + the data root.
type Storage struct {
	Mode    string `json:"mode,omitempty" validate:"omitempty,oneof=file memory"`
	DataDir string `json:"dataDir,omitempty"`
}

// Auth is the control-plane dev credential + its namespaces.
type Auth struct {
	Token      string   `json:"token,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
}

// Secrets references the at-rest AES-256 key by file path (never inline, ADR-0022).
type Secrets struct {
	EncryptionKeyFile string `json:"encryptionKeyFile,omitempty"`
}

// Runtime selects the execution lane + (for containerd) its settings.
type Runtime struct {
	Mode       string     `json:"mode,omitempty" validate:"omitempty,oneof=process containerd"`
	Containerd Containerd `json:"containerd,omitempty"`
}

// Containerd holds the containerd-lane settings (used only when Runtime.Mode == "containerd").
type Containerd struct {
	Socket        string            `json:"socket,omitempty"`
	Root          string            `json:"root,omitempty"`
	Snapshotter   string            `json:"snapshotter,omitempty"`
	CNIBinDir     string            `json:"cniBinDir,omitempty"`
	CNIConfDir    string            `json:"cniConfDir,omitempty"`
	SubnetCIDR    string            `json:"subnetCIDR,omitempty"`
	ImagePrefix   string            `json:"imagePrefix,omitempty"`
	ImageOverride map[string]string `json:"imageOverride,omitempty"`
}

// Log is the structured-log format + level.
type Log struct {
	Format string `json:"format,omitempty" validate:"omitempty,oneof=json text"`
	Level  string `json:"level,omitempty" validate:"omitempty,oneof=debug info warn error"`
}

// Telemetry configures the OTel pipeline (empty Endpoint ⇒ disabled).
type Telemetry struct {
	Endpoint string `json:"endpoint,omitempty"`
	Insecure bool   `json:"insecure,omitempty"`
}

// Flags are the CLI-flag overrides — the top precedence tier. A nil pointer ⇒ "flag not set".
type Flags struct {
	MemoryOnly *bool // --memory; non-nil+true ⇒ overrides storage.mode to "memory"
}

// Resolved is the effective config: precedence applied (flag > env > file > default), every field a
// concrete value, enums validated. cmd/funcd consumes it; it carries no driver types.
type Resolved struct {
	ListenAddr               string
	DataPlaneAddr            string
	StorageMode              string // file | memory
	DataDir                  string
	Token                    string // "" ⇒ cmd/funcd uses the built-in dev token (+ warn)
	Namespaces               []string
	SecretsEncryptionKeyFile string // "" ⇒ no at-rest encryption (warned)
	RuntimeMode              string // process | containerd
	Containerd               Containerd
	LogFormat                string // json | text
	LogLevel                 string // debug | info | warn | error
	TelemetryEndpoint        string
	TelemetryInsecure        bool
}

// Locate returns the config file path to load: explicit (--config; must exist → else fault.NotFound)
// → $FUNCD_CONFIG (must exist) → first existing of ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml →
// "" (none; zero-config). An explicit/env path that doesn't exist is an error (a typo must not
// silently fall through to defaults); the implicit search paths may be absent.
func Locate(explicit string) (string, error) {
	const op = "config.Locate"
	if explicit != "" {
		if !fileExists(explicit) {
			return "", fault.NotFoundf(op, "config file %q does not exist", explicit)
		}
		return explicit, nil
	}
	if env := os.Getenv("FUNCD_CONFIG"); env != "" {
		if !fileExists(env) {
			return "", fault.NotFoundf(op, "FUNCD_CONFIG %q does not exist", env)
		}
		return env, nil
	}
	for _, p := range []string{"funcdconfig.yaml", "/etc/funcd/funcdconfig.yaml"} {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", nil
}

// Load strict-decodes the file at path ("" ⇒ a zero File, zero-config). A read/parse error or an
// unknown key ⇒ fault.Invalid naming the problem.
func Load(path string) (File, error) {
	const op = "config.Load"
	if path == "" {
		return File{}, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is an operator-supplied config location
	if err != nil {
		return File{}, fault.Invalidf(op, "read config %q: %v", path, err)
	}
	var f File
	if err := yaml.UnmarshalStrict(data, &f); err != nil {
		return File{}, fault.Invalidf(op, "parse config %q: %v", path, err)
	}
	return f, nil
}

// Resolve validates the file against its `validate` tags, then applies precedence
// (flag > env > file > default) + defaults, reading the FUNCD_* vars internally. A bad enum / a
// present-but-wrong apiVersion/kind ⇒ fault.Invalid naming the key + the allowed set.
func Resolve(file File, flags Flags) (Resolved, error) {
	if err := file.Validate(); err != nil {
		return Resolved{}, err
	}

	r := Resolved{
		ListenAddr:               firstNonEmpty(file.Server.ListenAddr, defaultListenAddr),
		DataPlaneAddr:            firstNonEmpty(file.Server.DataPlaneAddr, defaultDataPlaneAddr),
		DataDir:                  resolveStr("FUNCD_DATA_DIR", file.Storage.DataDir, defaultDataDir),
		Token:                    resolveStr("FUNCD_TOKEN", file.Auth.Token, ""),
		Namespaces:               file.Auth.Namespaces,
		SecretsEncryptionKeyFile: file.Secrets.EncryptionKeyFile,
		LogFormat:                firstNonEmpty(file.Log.Format, defaultLogFormat),
		LogLevel:                 firstNonEmpty(file.Log.Level, defaultLogLevel),
		TelemetryEndpoint:        file.Telemetry.Endpoint,
		TelemetryInsecure:        file.Telemetry.Insecure,
	}
	if len(r.Namespaces) == 0 {
		r.Namespaces = []string{defaultNamespace}
	}

	// storage.mode: flag (--memory) > file > default.
	r.StorageMode = firstNonEmpty(file.Storage.Mode, defaultStorageMode)
	if flags.MemoryOnly != nil && *flags.MemoryOnly {
		r.StorageMode = "memory"
	}

	// runtime.mode: env > file > default.
	r.RuntimeMode = resolveStr("FUNCD_RUNTIME", file.Runtime.Mode, defaultRuntimeMode)

	// containerd lane: env > file > default; root/cniConfDir are dataDir-derived when unset.
	c := file.Runtime.Containerd
	r.Containerd = Containerd{
		Socket:        resolveStr("FUNCD_CONTAINERD_SOCKET", c.Socket, ""),
		Root:          resolveStr("FUNCD_CONTAINERD_ROOT", c.Root, filepath.Join(r.DataDir, "containerd")),
		Snapshotter:   resolveStr("FUNCD_SNAPSHOTTER", c.Snapshotter, defaultSnapshotter),
		CNIBinDir:     resolveStr("FUNCD_CNI_BIN_DIR", c.CNIBinDir, defaultCNIBinDir),
		CNIConfDir:    resolveStr("FUNCD_CNI_CONF_DIR", c.CNIConfDir, filepath.Join(r.DataDir, "cni")),
		SubnetCIDR:    resolveStr("FUNCD_SUBNET_CIDR", c.SubnetCIDR, defaultSubnetCIDR),
		ImagePrefix:   resolveStr("FUNCD_IMAGE_PREFIX", c.ImagePrefix, defaultImagePrefix),
		ImageOverride: resolveImageOverride(c.ImageOverride),
	}

	return r, nil
}

// Validate checks the file's fields against their declared `validate` tags (go-playground/validator,
// the same lib huma already uses) — the optional apiVersion/kind envelope and the storage/runtime/log
// enums — returning a fault.Invalid naming the offending key + the allowed set. Unset fields are
// allowed (`omitempty`); Resolve fills defaults afterward. It is exported so a caller can validate a
// File directly (the project's resource-`Validate()` convention, ADR-0048).
func (f File) Validate() error {
	const op = "config.File.Validate"
	v := validator.New()
	// Report json keys ("storage.mode") in errors, not Go field names.
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name, _, _ := strings.Cut(fld.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	err := v.Struct(f)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if errors.As(err, &verrs) && len(verrs) > 0 {
		fe := verrs[0]
		want := fe.Tag()
		if fe.Param() != "" {
			want += "=" + fe.Param()
		}
		key := strings.TrimPrefix(fe.Namespace(), "File.")
		return fault.Invalidf(op, "config key %q has invalid value %q (want %s)", key, fmt.Sprint(fe.Value()), want)
	}
	return fault.Wrapf(err, fault.Invalid, op, "invalid config")
}

// resolveImageOverride: FUNCD_IMAGE_OVERRIDE ("rt=ref,rt=ref") parsed > the file map > nil.
func resolveImageOverride(fileMap map[string]string) map[string]string {
	raw := os.Getenv("FUNCD_IMAGE_OVERRIDE")
	if raw == "" {
		if len(fileMap) == 0 {
			return nil
		}
		return fileMap
	}
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		if rt, ref, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && rt != "" && ref != "" {
			out[rt] = ref
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// resolveStr returns the env var (if set), else the file value (if non-empty), else def.
func resolveStr(envKey, fileVal, def string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return firstNonEmpty(fileVal, def)
}

func firstNonEmpty(a, def string) string {
	if a != "" {
		return a
	}
	return def
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
