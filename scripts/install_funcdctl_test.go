package scripts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// release adds to mux a release in GitHub's layout under prefix (/latest/download, or /download/<tag>): its
// SHA256SUMS and one dev build of funcdctl for this machine, a script that prints its tag. A non-empty sum replaces
// the build's checksum in SHA256SUMS.
func release(mux *http.ServeMux, prefix, tag, sum string) {
	file := fmt.Sprintf("funcdctl-dev-%s-%s-%s", tag, runtime.GOOS, runtime.GOARCH)
	build := []byte("#!/bin/sh\necho funcdctl " + tag + "\n")
	if sum == "" {
		digest := sha256.Sum256(build)
		sum = hex.EncodeToString(digest[:])
	}
	mux.HandleFunc(prefix+"/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", sum, file)
	})
	mux.HandleFunc(prefix+"/"+file, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(build)
	})
}

// installFuncdctl runs install-funcdctl.sh against the releases mux serves, into a fresh directory, and returns that
// directory and the script's output.
func installFuncdctl(t *testing.T, mux *http.ServeMux, env ...string) (string, string, error) {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	dir := filepath.Join(t.TempDir(), "bin")
	cmd := exec.Command("sh", "install-funcdctl.sh")
	cmd.Env = append(os.Environ(), append(env, "FUNCDCTL_RELEASES="+srv.URL, "FUNCDCTL_INSTALL_DIR="+dir)...)
	out, err := cmd.CombinedOutput()
	return dir, string(out), err
}

// The docs' install command runs the script with no settings: it must install the latest release's dev build for this
// machine.
func TestInstallScriptInstallsTheLatestDevBuild(t *testing.T) {
	mux := http.NewServeMux()
	release(mux, "/latest/download", "v9.9.9", "")

	dir, out, err := installFuncdctl(t, mux)
	require.NoError(t, err, out)
	require.Contains(t, out, "Installed funcdctl v9.9.9 in "+filepath.Join(dir, "funcdctl"))
	require.Contains(t, out, "Add "+dir+" to your PATH")
	got, err := exec.Command(filepath.Join(dir, "funcdctl")).Output()
	require.NoError(t, err)
	require.Equal(t, "funcdctl v9.9.9\n", string(got))
}

// FUNCDCTL_VERSION installs that release, not the latest one: the docs of an older version pin it.
func TestInstallScriptInstallsThePinnedRelease(t *testing.T) {
	mux := http.NewServeMux()
	release(mux, "/latest/download", "v9.9.9", "")
	release(mux, "/download/v9.9.8", "v9.9.8", "")

	dir, out, err := installFuncdctl(t, mux, "FUNCDCTL_VERSION=v9.9.8")
	require.NoError(t, err, out)
	got, err := exec.Command(filepath.Join(dir, "funcdctl")).Output()
	require.NoError(t, err)
	require.Equal(t, "funcdctl v9.9.8\n", string(got))
}

// A download that does not match its checksum is refused, and nothing is installed.
func TestInstallScriptRefusesABuildThatFailsItsChecksum(t *testing.T) {
	mux := http.NewServeMux()
	release(mux, "/latest/download", "v9.9.9", strings.Repeat("0", 64))

	dir, out, err := installFuncdctl(t, mux)
	require.Error(t, err)
	require.Contains(t, out, "does not match its checksum in SHA256SUMS")
	require.NoFileExists(t, filepath.Join(dir, "funcdctl"))
}
