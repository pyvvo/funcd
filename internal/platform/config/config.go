// Package config is the funcd daemon config — funcdconfig.yaml (ADR-0061, ADR-0062). It is ONE
// struct populated from file + env + default and validated once:
//
//	defaults() → yaml.UnmarshalStrict (file) → env.Parse (FUNCD_* overlay) → --memory flag → Validate
//
// precedence flag > env > file > default. A key is defined in one place — a single Config field with
// three tags: `json` is the yaml key (sigs.k8s.io/yaml maps yaml→json→struct, reusing json tags as in
// ADR-0061), `env` is the FUNCD_* overlay var (caarlos0/env, which leaves a field untouched when its
// var is unset — no clobber), `validate` is the rule (go-playground/validator, the lib huma already
// pulls). Validation runs on the MERGED struct, so a bad value from any source is caught. It is a leaf
// (no driver imports); cmd/funcd maps Config → []funcd.Option.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/caarlos0/env/v11"
	"github.com/go-playground/validator/v10"

	"github.com/green-0-rabbit/funcd/api/fault"
	"sigs.k8s.io/yaml"
)

// APIVersion / Kind are the optional funcdconfig.yaml envelope (validated only when present).
const (
	APIVersion = "funcd.io/v1alpha1"
	Kind       = "FuncdConfig"
)

// Config is the funcd daemon config — the single source of truth (ADR-0062). Each field's tags
// declare its yaml key (`json`), its FUNCD_* overlay var (`env`), and its validation rule (`validate`).
type Config struct {
	APIVersion string `json:"apiVersion,omitempty" validate:"omitempty,eq=funcd.io/v1alpha1"`
	Kind       string `json:"kind,omitempty" validate:"omitempty,eq=FuncdConfig"`
	Server     struct {
		ListenAddr    string `json:"listenAddr,omitempty" env:"FUNCD_LISTEN_ADDR"`
		DataPlaneAddr string `json:"dataPlaneAddr,omitempty" env:"FUNCD_DATA_PLANE_ADDR"`
	} `json:"server,omitempty"`
	Storage struct {
		Mode    string `json:"mode,omitempty" env:"FUNCD_STORAGE_MODE" validate:"oneof=file memory"`
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_DATA_DIR"`
	} `json:"storage,omitempty"`
	Kvstore struct {
		// Engine for the function-facing KV service (ADR-0066/0069): memory (default, ephemeral) or
		// badger (durable, at <Storage.DataDir>/kv). DataDir overrides the default location.
		Engine  string `json:"engine,omitempty" env:"FUNCD_KVSTORE_ENGINE" validate:"omitempty,oneof=memory badger"`
		DataDir string `json:"dataDir,omitempty" env:"FUNCD_KVSTORE_DATA_DIR"`
		// MaxStoresPerNamespace is the per-namespace KVStore count cap enforced at admission (ADR-0072);
		// 0 ⇒ the default (100); a negative value disables the quota.
		MaxStoresPerNamespace int `json:"maxStoresPerNamespace,omitempty" env:"FUNCD_KVSTORE_MAX_STORES_PER_NAMESPACE"`
		// Backup is the opt-in DR export of the KV instance to object storage (ADR-0067), off by default.
		// Enabled without a Target ⇒ fault.Invalid at startup. Interval/Rebaseline are Go durations ("30s").
		Backup struct {
			Enabled    bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_BACKUP_ENABLED"`
			Target     string `json:"target,omitempty" env:"FUNCD_KVSTORE_BACKUP_TARGET"`
			Interval   string `json:"interval,omitempty" env:"FUNCD_KVSTORE_BACKUP_INTERVAL"`
			Rebaseline string `json:"rebaseline,omitempty" env:"FUNCD_KVSTORE_BACKUP_REBASELINE"`
			ChunkBytes int    `json:"chunkBytes,omitempty" env:"FUNCD_KVSTORE_BACKUP_CHUNK_BYTES"`
		} `json:"backup,omitempty"`
		// Cdc is the opt-in change-feed of the KV instance to the bus (ADR-0068), off by default. Enabled
		// without a Sink ⇒ fault.Invalid at startup. Retention is a Go duration ("24h").
		Cdc struct {
			Enabled   bool   `json:"enabled,omitempty" env:"FUNCD_KVSTORE_CDC_ENABLED"`
			Sink      string `json:"sink,omitempty" env:"FUNCD_KVSTORE_CDC_SINK"`
			Retention string `json:"retention,omitempty" env:"FUNCD_KVSTORE_CDC_RETENTION"`
		} `json:"cdc,omitempty"`
	} `json:"kvstore,omitempty"`
	Auth struct {
		Token      string   `json:"token,omitempty" env:"FUNCD_TOKEN"`
		Namespaces []string `json:"namespaces,omitempty" env:"FUNCD_AUTH_NAMESPACES" envSeparator:","`
	} `json:"auth,omitempty"`
	Secrets struct {
		EncryptionKeyFile string `json:"encryptionKeyFile,omitempty" env:"FUNCD_SECRETS_ENCRYPTION_KEY_FILE"`
	} `json:"secrets,omitempty"`
	Runtime struct {
		Mode       string `json:"mode,omitempty" env:"FUNCD_RUNTIME" validate:"oneof=process containerd"`
		Containerd struct {
			Socket        string            `json:"socket,omitempty" env:"FUNCD_CONTAINERD_SOCKET"`
			Root          string            `json:"root,omitempty" env:"FUNCD_CONTAINERD_ROOT"`
			Snapshotter   string            `json:"snapshotter,omitempty" env:"FUNCD_SNAPSHOTTER"`
			CNIBinDir     string            `json:"cniBinDir,omitempty" env:"FUNCD_CNI_BIN_DIR"`
			CNIConfDir    string            `json:"cniConfDir,omitempty" env:"FUNCD_CNI_CONF_DIR"`
			SubnetCIDR    string            `json:"subnetCIDR,omitempty" env:"FUNCD_SUBNET_CIDR"`
			ImagePrefix   string            `json:"imagePrefix,omitempty" env:"FUNCD_IMAGE_PREFIX"`
			ImageOverride map[string]string `json:"imageOverride,omitempty" env:"FUNCD_IMAGE_OVERRIDE" envSeparator:"," envKeyValSeparator:"="`
		} `json:"containerd,omitempty"`
	} `json:"runtime,omitempty"`
	Log struct {
		Format string `json:"format,omitempty" env:"FUNCD_LOG_FORMAT" validate:"oneof=json text"`
		Level  string `json:"level,omitempty" env:"FUNCD_LOG_LEVEL" validate:"oneof=debug info warn error"`
	} `json:"log,omitempty"`
	Telemetry struct {
		Endpoint string `json:"endpoint,omitempty" env:"FUNCD_TELEMETRY_ENDPOINT"`
		Insecure bool   `json:"insecure,omitempty" env:"FUNCD_TELEMETRY_INSECURE"`
	} `json:"telemetry,omitempty"`
}

// Flags are the top precedence tier (CLI flags with no env). MemoryOnly nil ⇒ --memory not set.
type Flags struct{ MemoryOnly *bool }

// defaults returns the Config with every built-in default set (the bottom precedence tier; identical
// to ADR-0061's defaults). The dataDir-relative containerd paths (Root, CNIConfDir) are derived in
// Load after the merge, when DataDir is final.
func defaults() Config {
	var c Config
	c.Server.ListenAddr = "0.0.0.0:8080"
	c.Server.DataPlaneAddr = "127.0.0.1:0"
	c.Storage.Mode = "file"
	c.Storage.DataDir = "/var/lib/funcd"
	c.Auth.Namespaces = []string{"default"}
	c.Runtime.Mode = "process"
	c.Runtime.Containerd.Snapshotter = "overlayfs"
	c.Runtime.Containerd.CNIBinDir = "/opt/cni/bin"
	c.Runtime.Containerd.SubnetCIDR = "10.63.0.0/16"
	c.Runtime.Containerd.ImagePrefix = "funcd/runtime-"
	c.Log.Format = "json"
	c.Log.Level = "info"
	return c
}

// Locate returns the config file path to load: explicit (--config; must exist → else fault.NotFound)
// → $FUNCD_CONFIG (must exist) → first existing of ./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml →
// "" (none; zero-config). An explicit/env path that doesn't exist is an error; the implicit search
// paths may be absent.
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

// Load builds the effective Config (ADR-0062): defaults() → strict-decode the file at path ("" ⇒
// skip) → overlay env (caarlos0/env; an unset FUNCD_* var leaves the field untouched) → apply the
// --memory flag → derive the dataDir-relative containerd defaults → Validate. Any
// read/parse/unknown-key/enum error ⇒ fault.Invalid. The returned Config is fully populated + valid.
func Load(path string, flags Flags) (Config, error) {
	const op = "config.Load"
	c := defaults()
	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // operator-supplied config location
		if err != nil {
			return Config{}, fault.Invalidf(op, "read config %q: %v", path, err)
		}
		if err := yaml.UnmarshalStrict(data, &c); err != nil { // file overrides defaults; unknown key ⇒ error
			return Config{}, fault.Invalidf(op, "parse config %q: %v", path, err)
		}
	}
	if err := env.Parse(&c); err != nil { // env overrides file, only where set (no clobber)
		return Config{}, fault.Invalidf(op, "parse FUNCD_* env: %v", err)
	}
	if flags.MemoryOnly != nil && *flags.MemoryOnly { // the flag tier (top precedence)
		c.Storage.Mode = "memory"
	}
	// dataDir-relative containerd defaults — derived after the merge, when DataDir is final.
	if c.Runtime.Containerd.Root == "" {
		c.Runtime.Containerd.Root = filepath.Join(c.Storage.DataDir, "containerd")
	}
	if c.Runtime.Containerd.CNIConfDir == "" {
		c.Runtime.Containerd.CNIConfDir = filepath.Join(c.Storage.DataDir, "cni")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate runs the struct's `validate` tags (go-playground/validator) over the merged values,
// returning a fault.Invalid naming the offending yaml key + the allowed set. Load calls it on the
// effective config (so a bad value from the file, a FUNCD_* env, or the flag is all caught — the
// ADR-0061 env edge is closed); it is exported so a caller can validate a hand-built Config.
func (c Config) Validate() error {
	const op = "config.Config.Validate"
	v := validator.New()
	// Report yaml keys ("storage.mode") in errors, not Go field names (the json tag is the yaml key).
	v.RegisterTagNameFunc(func(fld reflect.StructField) string {
		name, _, _ := strings.Cut(fld.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	err := v.Struct(c)
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
		key := strings.TrimPrefix(fe.Namespace(), "Config.")
		return fault.Invalidf(op, "config key %q has invalid value %q (want %s)", key, fmt.Sprint(fe.Value()), want)
	}
	return fault.Wrapf(err, fault.Invalid, op, "invalid config")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
