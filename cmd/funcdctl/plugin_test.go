package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/version"
)

// asFuncdctl makes the test binary run funcdctl's main instead of its tests. A plugin replaces funcdctl's process,
// which only a test that starts funcdctl as a process of its own can see.
const asFuncdctl = "FUNCDCTL_TEST_AS_MAIN"

// sysPath holds the tools the hello plugin runs (sh, basename, cat, sleep).
const sysPath = "/usr/bin:/bin"

func TestMain(m *testing.M) {
	if os.Getenv(asFuncdctl) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// pluginDir is a new directory holding a copy of the hello plugin under each name.
func pluginDir(t *testing.T, names ...string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("testdata", "plugins", "funcdctl-hello"))
	require.NoError(t, err)
	dir := t.TempDir()
	for _, n := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), src, 0o755)) //nolint:gosec // a plugin must be executable
	}
	return dir
}

// funcdctlCmd is funcdctl started as a process with PATH set to path, env added to a bare environment, and cwd.
func funcdctlCmd(t *testing.T, path, cwd string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...) //nolint:gosec // the test binary itself, run as funcdctl
	cmd.Env = append([]string{asFuncdctl + "=1", "PATH=" + path, "HOME=" + t.TempDir()}, env...)
	cmd.Dir = cwd
	return cmd
}

// runFuncdctl runs funcdctl with stdin and returns its stdout, its stderr and its exit code.
func runFuncdctl(t *testing.T, path string, env []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := funcdctlCmd(t, path, "", env, args...)
	var out, errOut bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil {
		require.ErrorAs(t, err, &exit, "funcdctl %v did not start", args)
	}
	return out.String(), errOut.String(), cmd.ProcessState.ExitCode()
}

// A word that is no built-in command runs funcdctl-<word> from PATH with the words after it. Only the first word
// names the plugin, so funcdctl-hello-world does not run.
//
// scenario: plugin-runs-for-a-word-that-is-no-command
func TestPlugin_RunsTheExecutableNamedByTheFirstWord(t *testing.T) {
	t.Parallel()
	path := pluginDir(t, "funcdctl-hello", "funcdctl-hello-world") + ":" + sysPath
	out, errOut, code := runFuncdctl(t, path, nil, "", "hello", "world")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, "hello from funcdctl-hello\narg=world\n", out)
}

// A word with no plugin and no command fails as it did before plugins existed.
//
// scenario: a-word-with-no-plugin-is-an-unknown-command
func TestPlugin_AWordWithNoPluginIsAnUnknownCommand(t *testing.T) {
	t.Parallel()
	_, errOut, code := runFuncdctl(t, pluginDir(t)+":"+sysPath, nil, "", "nosuch")
	require.Equal(t, 1, code)
	require.Equal(t, "funcdctl: unknown command \"nosuch\" for \"funcdctl\"\n", errOut)
}

// A plugin named like a built-in command never runs, cobra's help and completion commands included.
//
// scenario: built-in-commands-win
func TestPlugin_BuiltInCommandsWin(t *testing.T) {
	t.Parallel()
	path := pluginDir(t, "funcdctl-get", "funcdctl-help", "funcdctl-completion", "funcdctl-plugin", "funcdctl-__complete") +
		":" + sysPath
	for _, args := range [][]string{{"get"}, {"help"}, {"completion", "bash"}, {"plugin", "list"}, {"__complete", "ge"}} {
		out, errOut, _ := runFuncdctl(t, path, nil, "", args...)
		require.NotContains(t, out+errOut, "hello from", "funcdctl %v ran a plugin", args)
	}
}

// Flags before the plugin's name are funcdctl's own and set what the plugin is handed. Every word after the name goes
// to the plugin unchanged, flags included.
//
// scenario: flags-before-the-name-are-funcdctls
func TestPlugin_FlagsBeforeTheNameAreFuncdctls(t *testing.T) {
	t.Parallel()
	path := pluginDir(t, "funcdctl-hello") + ":" + sysPath

	out, errOut, code := runFuncdctl(t, path, nil, "", "--server", "http://a.example:9", "--token", "t0k", "hello", "env")
	require.Equal(t, 0, code, errOut)
	require.Contains(t, out, "FUNCD_SERVER=http://a.example:9\n")
	require.Contains(t, out, "FUNCD_TOKEN=set\n")

	out, errOut, code = runFuncdctl(t, path, nil, "", "hello", "--server", "http://b.example", "-h", "--", "two words")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, "hello from funcdctl-hello\narg=--server\narg=http://b.example\narg=-h\narg=--\narg=two words\n", out)

	_, errOut, code = runFuncdctl(t, path, nil, "", "--bogus", "hello")
	require.Equal(t, 1, code)
	require.Equal(t, "funcdctl: unknown flag: --bogus\n", errOut)

	out, errOut, _ = runFuncdctl(t, path, nil, "", "-h", "hello")
	require.NotContains(t, out+errOut, "hello from")
}

// The plugin is handed the funcdctl that called it, its version, and the server and token funcdctl resolved from its
// flags or its environment. A token that `--token ""` clears is not handed on.
//
// scenario: the-plugin-is-handed-what-funcdctl-resolved
func TestPlugin_EnvNamesTheCallingFuncdctlAndWhatItResolved(t *testing.T) {
	t.Parallel()
	path := pluginDir(t, "funcdctl-hello") + ":" + sysPath

	out, errOut, code := runFuncdctl(t, path, nil, "", "hello", "env")
	require.Equal(t, 0, code, errOut)
	exe, err := filepath.EvalSymlinks(os.Args[0])
	require.NoError(t, err)
	line, ok := strings.CutPrefix(strings.Split(out, "\n")[0], "FUNCDCTL=")
	require.True(t, ok, out)
	got, err := filepath.EvalSymlinks(line)
	require.NoError(t, err)
	require.Equal(t, exe, got)
	require.Contains(t, out, "FUNCDCTL_VERSION="+version.Version+"\n")
	require.Contains(t, out, "FUNCD_SERVER=http://localhost:8080\n")
	require.Contains(t, out, "FUNCD_TOKEN=unset\n")

	// A shell starts funcdctl by its bare name, which argv[0] then holds; FUNCDCTL is still the binary's own path.
	cmd := funcdctlCmd(t, path, "", nil, "hello", "env")
	cmd.Args[0] = "funcdctl"
	bare, err := cmd.Output()
	require.NoError(t, err)
	line, ok = strings.CutPrefix(strings.Split(string(bare), "\n")[0], "FUNCDCTL=")
	require.True(t, ok, string(bare))
	require.True(t, filepath.IsAbs(line), "FUNCDCTL is %q when argv[0] is a bare name", line)
	got, err = filepath.EvalSymlinks(line)
	require.NoError(t, err)
	require.Equal(t, exe, got)

	out, _, _ = runFuncdctl(t, path, []string{"FUNCD_SERVER=http://env.example", "FUNCD_TOKEN=from-env"}, "", "hello", "env")
	require.Contains(t, out, "FUNCD_SERVER=http://env.example\n")
	require.Contains(t, out, "FUNCD_TOKEN=set\n")

	out, _, _ = runFuncdctl(t, path, []string{"FUNCD_TOKEN=from-env"}, "", "--token", "", "hello", "env")
	require.Contains(t, out, "FUNCD_TOKEN=unset\n")
}

// A plugin that calls $FUNCDCTL reaches the control plane funcdctl was pointed at, with the token funcdctl resolved:
// the right token lists, and a wrong one is refused.
//
// scenario: the-plugin-calls-back-with-the-resolved-token
func TestPlugin_CallsBackThroughFuncdctlWithTheResolvedToken(t *testing.T) {
	t.Parallel()
	_, url, _ := newTestServerVia(t, nil)
	path := pluginDir(t, "funcdctl-hello") + ":" + sysPath
	get := []string{"hello", "call", "get", "functions", "-n", "team-a"}

	_, errOut, code := runFuncdctl(t, path, nil, "", append([]string{"--server", url, "--token", devToken}, get...)...)
	require.Equal(t, 0, code, errOut)

	_, errOut, code = runFuncdctl(t, path, nil, "", append([]string{"--server", url, "--token", "wrong"}, get...)...)
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "invalid credential")
}

// On Unix the plugin replaces funcdctl's process: it runs under funcdctl's PID, so its exit code, its stdio and the
// signals sent to that PID are its own. An executable that cannot be executed fails as funcdctl's own error.
//
// scenario: the-plugin-replaces-funcdctl
func TestPlugin_ReplacesTheFuncdctlProcess(t *testing.T) {
	t.Parallel()
	path := pluginDir(t, "funcdctl-hello") + ":" + sysPath

	cmd := funcdctlCmd(t, path, "", nil, "hello", "pid")
	var out bytes.Buffer
	cmd.Stdout = &out
	require.NoError(t, cmd.Run())
	require.Equal(t, fmt.Sprintf("pid=%d\n", cmd.Process.Pid), out.String())

	_, _, code := runFuncdctl(t, path, nil, "", "hello", "exit", "7")
	require.Equal(t, 7, code)

	stdout, _, _ := runFuncdctl(t, path, nil, "ping\n", "hello", "stdin")
	require.Equal(t, "ping\n", stdout)
	_, errOut, _ := runFuncdctl(t, path, nil, "", "hello", "stderr", "oops")
	require.Equal(t, "oops\n", errOut)

	cmd = funcdctlCmd(t, path, "", nil, "hello", "wait-signal")
	pipe, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	r := bufio.NewReader(pipe)
	ready, err := r.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", ready)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	rest, err := io.ReadAll(r)
	require.NoError(t, err)
	_ = cmd.Wait()
	require.Equal(t, "got TERM\n", string(rest))
	require.Equal(t, 143, cmd.ProcessState.ExitCode())

	broken := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(broken, "funcdctl-broken"), []byte("echo no interpreter line\n"), 0o755)) //nolint:gosec // a plugin must be executable
	_, errOut, code = runFuncdctl(t, broken+":"+sysPath, nil, "", "broken")
	require.Equal(t, 1, code)
	require.Contains(t, errOut, "funcdctl: funcdctl broken: run the plugin "+broken+"/funcdctl-broken: exec format error")
}

// A versioned funcdctl, or a name with an upper-case letter, on PATH is no plugin: it neither runs nor lists.
//
// scenario: only-a-plain-word-names-a-plugin
func TestPlugin_OnlyAPlainWordNamesAPlugin(t *testing.T) {
	t.Parallel()
	path := pluginDir(t, "funcdctl-v0.39.3", "funcdctl-Hello") + ":" + sysPath
	for _, args := range [][]string{{"v0.39.3", "get"}, {"Hello"}} {
		out, errOut, code := runFuncdctl(t, path, nil, "", args...)
		require.Equal(t, 1, code, "funcdctl %v", args)
		require.NotContains(t, out, "hello from")
		require.Contains(t, errOut, "unknown command")
	}
	out, _, code := runFuncdctl(t, path, nil, "", "plugin", "list")
	require.Equal(t, 0, code)
	require.Equal(t, "no plugins on PATH\n", out)
}

// A plugin found only through a relative PATH entry, or named with a slash, never runs: either would run a file of the
// working directory.
//
// scenario: nothing-runs-from-the-working-directory
func TestPlugin_NothingRunsFromTheWorkingDirectory(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	src, err := os.ReadFile(filepath.Join("testdata", "plugins", "funcdctl-hello"))
	require.NoError(t, err)
	for _, f := range []string{"rel/funcdctl-hello", "funcdctl-x/y"} {
		require.NoError(t, os.MkdirAll(filepath.Join(cwd, filepath.Dir(f)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(cwd, f), src, 0o755)) //nolint:gosec // a plugin must be executable
	}
	for _, args := range [][]string{{"hello"}, {"x/y"}} {
		cmd := funcdctlCmd(t, "rel:"+sysPath, cwd, nil, args...)
		var out, errOut bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errOut
		_ = cmd.Run()
		require.Equal(t, 1, cmd.ProcessState.ExitCode(), "funcdctl %v", args)
		require.NotContains(t, out.String(), "hello from")
		require.Contains(t, errOut.String(), "unknown command")
	}
}

// `funcdctl plugin list` names each plugin on PATH with its path, in PATH order, and warns about each one that never
// runs because a built-in command or an earlier PATH entry takes its name. A file that is not executable is no plugin.
//
// scenario: plugin-list
func TestPluginList_NamesEachPluginAndWarnsAboutTheOnesThatNeverRun(t *testing.T) {
	t.Parallel()
	a := pluginDir(t, "funcdctl-hello", "funcdctl-get")
	b := pluginDir(t, "funcdctl-hello", "funcdctl-zz")
	require.NoError(t, os.WriteFile(filepath.Join(b, "funcdctl-noexec"), []byte("#!/bin/sh\n"), 0o600))

	out, errOut, code := runFuncdctl(t, a+":"+b+":relative:"+sysPath, nil, "", "plugin", "list")
	require.Equal(t, 0, code, errOut)
	require.Equal(t, fmt.Sprintf("NAME    PATH\nget     %[1]s/funcdctl-get\nhello   %[1]s/funcdctl-hello\nhello   %[2]s/funcdctl-hello\n"+
		"zz      %[2]s/funcdctl-zz\n", a, b), out)
	require.Equal(t, fmt.Sprintf("warning: %[1]s/funcdctl-get never runs: \"get\" is a built-in command\n"+
		"warning: %[2]s/funcdctl-hello never runs: %[1]s/funcdctl-hello comes first on PATH\n", a, b), errOut)

	out, _, code = runFuncdctl(t, sysPath, nil, "", "plugin", "list")
	require.Equal(t, 0, code)
	require.Equal(t, "no plugins on PATH\n", out)
}
