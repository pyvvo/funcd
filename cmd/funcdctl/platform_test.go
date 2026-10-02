package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
)

// pushPlatform pushes a single-file function built "for" platform with a funcdctl.yaml beside it (or, without one,
// with --schema) and returns the printed <ref>@<digest>.
func pushPlatform(t *testing.T, layout, tag string, platform v1.OCIPlatform, withManifest bool) string {
	t.Helper()
	dir := t.TempDir()
	bundle := filepath.Join(dir, "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() { return '"+string(platform)+"'; }\n"), 0o600))
	args := []string{"push", bundle, "oci-layout://" + layout + ":" + tag, "--platform", string(platform)}
	if withManifest {
		manifest := "runtime: nodejs22\nhandler: handle\ncontract:\n  input:\n    type: \"null\"\n  output:\n    type: \"null\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "funcdctl.yaml"), []byte(manifest), 0o600))
	} else {
		args = append(args, "--schema", writeSchemaFile(t, `{"input":{"type":"null"},"output":{"type":"null"}}`), "--runtime", "nodejs22")
	}
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, args...))
	return strings.TrimSpace(out.String())
}

// scenario: push-records-platform + index-combines-platforms through the commands (ADR-0145): `push --platform` on
// the funcdctl.yaml path and the --schema path, then `index` over both prints <ref>@<digest> of a two-platform index,
// and `pull --platform` gets each platform's file.
func TestCLIPushPlatformAndIndex(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "layout")
	amd := pushPlatform(t, layout, "amd", v1.PlatformLinuxAMD64, true)
	arm := pushPlatform(t, layout, "arm", v1.PlatformLinuxARM64, false)
	require.Contains(t, amd, "@sha256:")
	require.Contains(t, arm, "@sha256:")

	ref := "oci-layout://" + layout + ":fn"
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "index", ref, "oci-layout://"+layout+":amd", "oci-layout://"+layout+":arm"))
	printed := strings.TrimSpace(out.String())
	require.True(t, strings.HasPrefix(printed, ref+"@sha256:"), "index prints <ref>@<digest>, got %q", printed)
	digest := strings.TrimPrefix(printed, ref+"@")

	ps, err := artifact.Platforms(context.Background(), ref, digest)
	require.NoError(t, err)
	require.ElementsMatch(t, []v1.OCIPlatform{v1.PlatformLinuxAMD64, v1.PlatformLinuxARM64}, ps)

	for _, p := range []v1.OCIPlatform{v1.PlatformLinuxAMD64, v1.PlatformLinuxARM64} {
		out.Reset()
		require.NoError(t, execCLI(&out, nil, "pull", ref, digest, filepath.Join(t.TempDir(), "out"), "--platform", string(p)))
		got, err := os.ReadFile(strings.TrimSpace(out.String())) //nolint:gosec // test-owned
		require.NoError(t, err)
		require.Contains(t, string(got), string(p))
	}
}

// push --platform accepts only the artifact platforms, and never with --site.
func TestCLIPushPlatformRefusals(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(bundle, []byte("export function handle() {}\n"), 0o600))
	schema := writeSchemaFile(t, `{"input":{"type":"null"},"output":{"type":"null"}}`)
	ref := "oci-layout://" + filepath.Join(t.TempDir(), "layout") + ":v1"
	err := execCLI(&bytes.Buffer{}, nil, "push", bundle, ref, "--schema", schema, "--platform", "darwin/arm64")
	require.ErrorContains(t, err, "--platform")

	site := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("<title>x</title>"), 0o600))
	err = execCLI(&bytes.Buffer{}, nil, "push", "--site", site, ref, "--platform", "linux/amd64")
	require.ErrorContains(t, err, "--platform")
}
