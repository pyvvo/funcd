package reference

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/config"
)

//nolint:gochecknoglobals // the -update flag of this package's tests
var update = flag.Bool("update", false, "rewrite testdata/undescribed-config.txt from config.Config")

const listFile = "testdata/undescribed-config.txt"

// TestEveryConfigKeyIsDescribed holds every key of funcdconfig.yaml to a doc tag that names no ADR, but the keys that
// testdata/undescribed-config.txt lists; that list only shrinks (ADR-0324 Decision 2). go test -run
// TestEveryConfigKeyIsDescribed -update rewrites it.
func TestEveryConfigKeyIsDescribed(t *testing.T) {
	keys := ConfigKeys(config.Defaults())
	got := undescribed(keys)
	if *update {
		require.NoError(t, os.WriteFile(listFile, []byte(strings.Join(got, "\n")+"\n"), 0o644))
		return
	}
	require.NoError(t, checkList(got, readList(t)))
	for _, k := range keys {
		require.NotRegexp(t, `\bADR-\d{4}\b`, k.Description, "%s: a description is written for users and names no ADR",
			k.Path)
	}
}

// scenario: undescribed-config-key-refused
func TestScenarioUndescribedConfigKeyRefused(t *testing.T) {
	got := undescribed(keysOf(reflect.ValueOf(testConfig{}), ""))
	require.ErrorContains(t, checkList(got, nil), "server.newKey")
	require.ErrorContains(t, checkList(got, []string{"server.newKey", "server.described"}), "server.described")
}

type testConfig struct {
	Server struct {
		Described string `json:"described" doc:"A key with a description."`
		NewKey    string `json:"newKey"`
	} `json:"server"`
	Credentials []struct {
		Role string `json:"role" validate:"oneof=admin viewer"`
	} `json:"credentials"`
	Headers map[string]string `json:"headers" env:"TEST_HEADERS" envSeparator:"," envKeyValSeparator:"="`
	Port    int               `json:"port" env:"TEST_PORT" validate:"min=0,max=65535"`
}

// scenario: stale-command-spec-fails
func TestScenarioStaleCommandSpecFails(t *testing.T) {
	root := &cobra.Command{Use: "tool", Short: "A tool."}
	run := &cobra.Command{Use: "run", Short: "Run it.", Run: func(*cobra.Command, []string) {}}
	root.AddCommand(run)
	page := func() []byte {
		cmds, err := Commands(root)
		require.NoError(t, err)
		return Page("tool", "Its commands.", CommandTables(cmds))
	}
	path := filepath.Join(t.TempDir(), "spec.md")
	require.NoError(t, os.WriteFile(path, page(), 0o644))
	require.NoError(t, Stale(path, page()))
	run.Short = "Run it now."
	require.ErrorContains(t, Stale(path, page()), path)
}

// scenario: host-default-refused
func TestScenarioHostDefaultRefused(t *testing.T) {
	t.Setenv("FUNCD_REFERENCE_TEST", "value-from-env")
	for name, def := range map[string]string{
		"variable":     "http://value-from-env:8080",
		"architecture": "linux/" + runtime.GOARCH,
		"system":       runtime.GOOS + "/amd64",
	} {
		root := &cobra.Command{Use: "tool"}
		root.Flags().String("opt", def, "an option")
		_, err := Commands(root)
		require.ErrorContains(t, err, "tool --opt", name)
	}
	root := &cobra.Command{Use: "tool"}
	root.Flags().String("opt", "http://localhost:8080", "an option")
	_, err := Commands(root)
	require.NoError(t, err)
}

func TestCommandsRefuseAnADRNumber(t *testing.T) {
	root := &cobra.Command{Use: "tool", Short: "A tool (ADR-0042)."}
	_, err := Commands(root)
	require.ErrorContains(t, err, "tool: its help text names an ADR")
	root = &cobra.Command{Use: "tool"}
	root.Flags().Bool("dry-run", false, "store nothing (ADR-0220)")
	_, err = Commands(root)
	require.ErrorContains(t, err, "tool --dry-run: its usage names an ADR")
}

func TestCommandsMarkDevOnly(t *testing.T) {
	root := &cobra.Command{Use: "tool"}
	dev := &cobra.Command{Use: "dev", Annotations: map[string]string{DevAnnotation: "dev"}, Run: func(*cobra.Command, []string) {}}
	dev.AddCommand(&cobra.Command{Use: "app", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(dev)
	cmds, err := Commands(root)
	require.NoError(t, err)
	devOnly := map[string]bool{}
	for _, c := range cmds {
		devOnly[c.Path] = c.DevOnly
	}
	require.Equal(t, map[string]bool{
		"tool": false, "tool dev": true, "tool dev app": true, "tool help": false, "tool completion": false,
		"tool completion bash": false, "tool completion fish": false, "tool completion powershell": false,
		"tool completion zsh": false,
	}, devOnly)
}

func TestConfigKeyRows(t *testing.T) {
	keys := keysOf(reflect.ValueOf(testConfig{Port: 8080}), "")
	byPath := map[string]ConfigKey{}
	for _, k := range keys {
		byPath[k.Path] = k
	}
	require.Equal(t, "one of `admin` or `viewer`", byPath["credentials[].role"].Limits)
	require.Equal(t, ConfigKey{Path: "headers", Type: "map of string to string", Env: "TEST_HEADERS=k1=v1,k2=v2"},
		byPath["headers"])
	require.Equal(t, ConfigKey{Path: "port", Type: "integer", Default: "8080", Limits: "0 to 65535", Env: "TEST_PORT"},
		byPath["port"])
	require.Equal(t, "required; at least 1", limitsOf("required,min=1"))
	require.Equal(t, "must be `FuncdConfig`", limitsOf("omitempty,eq=FuncdConfig"))
	require.Empty(t, limitsOf("dive,keys,header_name,endkeys"))
}

func undescribed(keys []ConfigKey) []string {
	var out []string
	for _, k := range keys {
		if k.Description == "" {
			out = append(out, k.Path)
		}
	}
	return out
}

// checkList fails on an undescribed key that the list does not hold, and on a listed key that is no longer
// undescribed: the list only shrinks.
func checkList(got, listed []string) error {
	var problems []string
	if missing := slices.DeleteFunc(slices.Clone(got), func(k string) bool { return slices.Contains(listed, k) }); len(missing) > 0 {
		problems = append(problems, "keys with no description, which need a doc tag written for users: "+strings.Join(missing, ", "))
	}
	if stale := slices.DeleteFunc(slices.Clone(listed), func(k string) bool { return slices.Contains(got, k) }); len(stale) > 0 {
		problems = append(problems, "listed keys that have a description or are gone, to remove from "+listFile+": "+strings.Join(stale, ", "))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

func readList(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(listFile)
	require.NoError(t, err)
	return slices.DeleteFunc(strings.Split(strings.TrimSpace(string(b)), "\n"), func(s string) bool { return s == "" })
}
