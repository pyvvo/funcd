//go:build dev

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// overPolls calls step 24 times, one every 50 ms: a build tool writing for about 1.2 s, over four of dev's polls.
func overPolls(step func(i int)) {
	for i := range 24 {
		step(i)
		time.Sleep(50 * time.Millisecond)
	}
}

// TestDevSettle: a change reloads at the first poll that reads the same as the poll before it, a change seen while
// the inputs still move waits until they settle, and nothing reloads without a change.
func TestDevSettle(t *testing.T) {
	var d devSettle
	for i, poll := range []struct {
		stamp   string
		changed bool
		want    bool
	}{
		{"a", false, false},
		{"a", false, false},
		{"b", true, false},
		{"b", true, true},
		{"b", false, false},
		{"c", true, false},
		{"d", false, false},
		{"d", false, true},
		{"d", false, false},
	} {
		require.Equal(t, poll.want, d.ready(poll.stamp, poll.changed), "poll %d", i)
	}
}

// TestIssue1118_AppDevWaitsForABuildToSettle: a Site build that its tool empties, as vite build empties its outDir,
// then writes over several polls reloads once, after its last write: no reload of the build in progress, one new
// revision, and the Site serves the finished build.
func TestIssue1118_AppDevWaitsForABuildToSettle(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := appCopy(t, "todo")
	r := startApp(t, root, todoCfg(nil))
	r.serves(t)
	n := r.settled(t, "todo")

	web := filepath.Join(root, "web")
	index, err := os.ReadFile(filepath.Join(web, "index.html"))
	require.NoError(t, err)
	script, err := os.ReadFile(filepath.Join(web, "app.js"))
	require.NoError(t, err)
	rebuilt := strings.Replace(string(index), "</body>", "<!-- rebuilt --></body>", 1)
	require.NoError(t, os.Remove(filepath.Join(web, "index.html")))
	require.NoError(t, os.Remove(filepath.Join(web, "app.js")))
	overPolls(func(i int) {
		require.NoError(t, os.WriteFile(filepath.Join(web, fmt.Sprintf("chunk-%02d.js", i)), []byte("// chunk\n"), 0o600))
	})
	require.NoError(t, os.WriteFile(filepath.Join(web, "app.js"), script, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(web, "index.html"), []byte(rebuilt), 0o600))

	r.eventually(t, func() bool {
		code, body := get(t, r.inst.gatewayURL+"/site/todo-web/", "")
		return code == http.StatusOK && body == rebuilt
	}, "the Site serves the finished build")
	require.Equal(t, n+1, r.settled(t, "todo"), "the build reloads once, after its last write")
	require.NotContains(t, r.out.String(), "reload failed", "no reload of a build in progress")
}

// TestIssue1118_FolderDevWaitsForABuildToSettle: a bundle that its tool writes over several polls reloads once, after
// its last write: no reload of the bundle in progress, and the Function moves one generation and serves the finished
// bundle.
func TestIssue1118_FolderDevWaitsForABuildToSettle(t *testing.T) {
	requireNode(t)
	dir := devProject(t, nodeFunction("slow", "", "export function handle() { return { build: 'first' }; }\n"))
	out := &lockedBuffer{}
	inst, err := tryStartDevTo(t, dir, out)
	require.NoError(t, err)
	before := waitFunction(t, inst, "slow").Generation

	f, err := os.Create(filepath.Join(dir, "slow.mjs"))
	require.NoError(t, err)
	overPolls(func(i int) {
		_, werr := fmt.Fprintf(f, "// line %d of a bundle being written\n", i)
		require.NoError(t, werr)
	})
	_, err = f.WriteString("export function handle() { return { build: 'settled' }; }\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	eventuallyServes(t, inst.gatewayURL+"/function/slow", `{"data":{}}`, http.StatusOK, "settled")
	got, err := inst.client.Get(context.Background(), v1.KindFunction, "default", "slow")
	require.NoError(t, err)
	require.Equal(t, before+1, got.(*v1.Function).Generation, "the bundle reloads once, after its last write")
	require.NotContains(t, out.String(), "reload failed", "no reload of a bundle in progress")
}

// TestDevSettleKeepsAConfigReload: a change to the envFile of funcdctl.config.yaml, which no folder stamp covers, still
// reloads when it waits for the inputs to settle. After a reload that read a new Function, the next poll's stamp
// differs from the one devSettle holds, so the change reloads one poll later, when reloadFile no longer reports it.
func TestDevSettleKeepsAConfigReload(t *testing.T) {
	requireNode(t)
	const key = "DEVSETTLE_RELOAD_KEY"
	require.NoError(t, os.Unsetenv(key))
	start := devProject(t, merge(secretFunction(key), map[string]string{devConfigFile: "dev:\n  envFile: .env\n", ".env": key + "=one\n"}))
	var logs lockedBuffer
	inst, err := startDevFrom(t, start, &logs, devConfig{})
	require.NoError(t, err)
	url := inst.gatewayURL + "/function/api"
	eventuallyServes(t, url, `{"data":{}}`, http.StatusOK, `"key":"one"`)

	writeProject(t, start, nodeFunction("more", "", "export function handle() { return {}; }\n"))
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), addedAfterStart) }, 10*time.Second,
		50*time.Millisecond, "the reload reads the new Function")
	writeProject(t, start, map[string]string{".env": key + "=two\n"})
	eventuallyServes(t, url, `{"data":{}}`, http.StatusOK, `"key":"two"`)
}

// addedAfterStart is the warning of a Function added after dev started.
const addedAfterStart = "restart funcdctl dev to run a Function added after it started"

// TestDevReloadLeavesAFunctionAddedAfterStartAlone: a Function added after dev started is read once, with its warning,
// and does not reload the folder at every poll, though dev never delivers its build.
func TestDevReloadLeavesAFunctionAddedAfterStartAlone(t *testing.T) {
	requireNode(t)
	dir := devProject(t, nodeFunction("a", "", "export function handle() { return {}; }\n"))
	var logs lockedBuffer
	inst, err := tryStartDevTo(t, dir, &logs)
	require.NoError(t, err)
	waitFunction(t, inst, "a")
	writeProject(t, dir, nodeFunction("more", "", "export function handle() { return {}; }\n"))
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), addedAfterStart) }, 10*time.Second,
		50*time.Millisecond, "the reload reads the new Function")
	time.Sleep(6 * devReloadPoll)
	require.Equal(t, 1, strings.Count(logs.String(), addedAfterStart), "the folder reloads once; dev printed:\n%s", &logs)
}
