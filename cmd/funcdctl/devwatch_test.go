//go:build dev

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The tests of ADR-0255 run serially: each one forks watch commands.

// loopConfig is ADR-0255's scenario file; extra is appended under dev.
func loopConfig(watch, extra string) string {
	return "dev:\n  dir: app\n  build:\n    - functions/ts\n    - functions/py\n    - web=web\n" +
		"    - api=functions/ts/api-1.0.0.mjs\n    - migrate=functions/ts/migrate-1.0.0.mjs\n  watch:\n" + watch + extra
}

// webWatch is the scenario's watch entry.
const webWatch = "    - name: web\n      dir: web-src\n      run: sh build.sh --watch\n"

// loopBuild is the scenario's web-src/build.sh, a stand-in for vite build --watch: after half a second it copies site/
// into ../web, then, under --watch, again on each change, printing one line on each stream per copy; while FAIL exists
// it exits 3.
const loopBuild = `fail() { echo "build failed" >&2; exit 3; }
copy() { [ -e FAIL ] && fail; mkdir -p ../web && cp -R site/. ../web/ && echo "copied to web" && echo "copy done" >&2; }
stamp() { find site -type f -exec cksum {} + | cksum; }
sleep 0.5
copy
[ "$1" = "--watch" ] || exit 0
last=$(stamp)
while :; do
	sleep 0.2
	[ -e FAIL ] && fail
	now=$(stamp)
	if [ "$now" != "$last" ]; then copy; last=$now; fi
done
`

// loopTodo is a copy of the todo App with its Site moved to web-src/site, web-src/build.sh, and config as its
// funcdctl.config.yaml.
func loopTodo(t *testing.T, config string) string {
	t.Helper()
	root := appCopy(t, "todo")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "web-src"), 0o755))
	require.NoError(t, os.Rename(filepath.Join(root, "web"), filepath.Join(root, "web-src", "site")))
	require.NoError(t, os.WriteFile(filepath.Join(root, "web-src", "build.sh"), []byte(loopBuild), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, devConfigFile), []byte(config), 0o600))
	return root
}

// startLoop runs `funcdctl dev` from root as a developer would; cache is the user cache dir dev uses without --persist.
func startLoop(t *testing.T, root, cache string) (*appRun, devConfig, error) {
	t.Helper()
	var cfg devConfig
	dir, _, err := resolveDevRun("test", root, nil, nil, &cfg)
	require.NoError(t, err)
	cfg.cacheDir = cache
	ctx, cancel := context.WithCancel(context.Background())
	out := &lockedBuffer{}
	inst, err := (&cli{out: out}).startDev(ctx, dir, cfg)
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			cancel()
			if inst != nil {
				_ = inst.stop()
			}
		}
	}
	t.Cleanup(stop)
	return &appRun{inst: inst, out: out, stop: stop}, cfg, err
}

// loopState is the process dir of the run cfg starts from root.
func loopState(t *testing.T, root string, cfg devConfig) string {
	t.Helper()
	state, err := devStateDir(cfg, filepath.Join(root, "app"))
	require.NoError(t, err)
	return state
}

// groupAlive reports whether process group pgid has a member.
func groupAlive(pgid int) bool {
	return pgid > 1 && !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

// watchPID is the pid the watch command wrote to path (its process group), once it did.
func watchPID(t *testing.T, path string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(path) //nolint:gosec // the test's own file
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 1
	}, 60*time.Second, 50*time.Millisecond, "the watch command writes its pid")
	return pid
}

// siteIndex is the Site's index as dev's gateway serves it.
func siteIndex(t *testing.T, r *appRun) string {
	t.Helper()
	var body string
	r.eventually(t, func() bool {
		var code int
		code, body = get(t, r.inst.gatewayURL+"/site/todo-web/", "")
		return code == http.StatusOK
	}, "the Site serves")
	return body
}

// reloadEvents reads dev's reload stream and sends one value per message.
func reloadEvents(t *testing.T, url string) <-chan struct{} {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // dev's loopback stream, read until the test ends
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "*", resp.Header.Get("Access-Control-Allow-Origin"))
	t.Cleanup(func() { _ = resp.Body.Close() })
	ch := make(chan struct{}, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if sc.Text() == "data: reload" {
				ch <- struct{}{}
			}
		}
	}()
	return ch
}

// appendTo appends s to the file at path, as a save does.
func appendTo(t *testing.T, path, s string) {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // the test's own copy
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(b, s...), 0o600))
}

// scenario: dev-watch-waits — dev prints the command's log file, starts it, prints that it waits for images.web
// naming web, and applies the App once web/ holds a settled build with its index; the Site serves it.
func TestScenarioDevWatchWaits(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := loopTodo(t, loopConfig(webWatch, "  liveReload: true\n"))
	r, cfg, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	out := r.out.String()
	logFile := filepath.Join(loopState(t, root, cfg), "logs", "web.log")
	require.Contains(t, out, "watch web: "+logFile)
	require.Contains(t, out, "waiting for images.web: "+filepath.Join(root, "web")+" is absent\n")
	require.Less(t, strings.Index(out, "watch web: "), strings.Index(out, "waiting for "), "the log file is printed first")
	index, err := os.ReadFile(filepath.Join(root, "web-src", "site", "index.html"))
	require.NoError(t, err)
	require.Equal(t, string(withTag(index, []byte(r.inst.live.tag()))), siteIndex(t, r), "the Site serves the build")
}

// scenario: dev-watch-quiet — neither of the command's lines reaches the terminal, both are in its log file, which the
// banner names, and a save prints dev's lines as ADR-0253 says.
func TestScenarioDevWatchQuiet(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := loopTodo(t, loopConfig(webWatch, ""))
	r, cfg, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	r.serves(t)
	logFile := filepath.Join(loopState(t, root, cfg), "logs", "web.log")
	log, err := os.ReadFile(logFile) //nolint:gosec // dev's log file
	require.NoError(t, err)
	require.Contains(t, string(log), "copied to web")
	require.Contains(t, string(log), "copy done")
	var banner strings.Builder
	require.NoError(t, (&cli{out: &banner}).printBanner(r.inst))
	require.Contains(t, banner.String(), "WATCH")
	require.Contains(t, banner.String(), logFile)

	mark := len(r.out.String())
	appendTo(t, filepath.Join(root, "web-src", "site", "app.js"), "\n// saved\n")
	r.eventually(t, func() bool {
		s := r.out.String()[mark:]
		return strings.Contains(s, "pushed images.web ") && strings.Contains(s, "reloaded in ")
	}, "a save prints dev's lines")
	for _, line := range []string{"copied to web", "copy done"} {
		require.NotContains(t, r.out.String(), line, "a command's output stays in its log file")
	}
}

// scenario: dev-watch-restart — a command that exits non-zero is printed with its last lines and started again after
// 1 s, the delay doubling; a command that exits 0 is printed as finished and not started again.
func TestScenarioDevWatchRestart(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	once := "    - name: once\n      dir: web-src\n      run: sh build.sh\n"
	root := loopTodo(t, loopConfig(pidWatch+once, ""))
	r, _, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	r.serves(t)
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "once finished") }, "the one-shot command finishes")

	first := watchPID(t, filepath.Join(root, "watch.pid"))
	fail := filepath.Join(root, "web-src", "FAIL")
	require.NoError(t, os.WriteFile(fail, nil, 0o600))
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "web exited 3; restarting in 1s") }, "web fails")
	require.Contains(t, r.out.String(), "web | build failed", "its last lines")
	require.False(t, groupAlive(first), "the sleep the failed run started went with it")
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "web exited 3; restarting in 2s") }, "the delay doubles")
	require.NoError(t, os.Remove(fail))
	mark := len(r.out.String())
	appendTo(t, filepath.Join(root, "web-src", "site", "app.js"), "\n// fixed\n")
	r.eventually(t, func() bool { return strings.Contains(r.out.String()[mark:], "pushed images.web ") }, "web builds again")
	require.Equal(t, 1, strings.Count(r.out.String(), "once finished"), "a command that exits 0 is not started again")
}

// devLoopEnv makes a re-executed test binary run the real `funcdctl dev` command from its working dir until a stop
// signal, with its output in the file the variable names.
const devLoopEnv = "FUNCDCTL_TEST_DEV_LOOP"

// runDevLoopChild runs that command in the re-executed test binary; it reports whether this process is that child.
func runDevLoopChild(t *testing.T) bool {
	path := os.Getenv(devLoopEnv)
	if path == "" {
		return false
	}
	out, err := os.Create(path) //nolint:gosec // the parent test's file
	require.NoError(t, err)
	defer func() { _ = out.Close() }()
	cmd := (&cli{out: out}).devCmd()
	cmd.SetArgs([]string{})
	require.NoError(t, cmd.Execute())
	return true
}

// startLoopChild starts `funcdctl dev` in a re-executed test binary from root and waits until its banner is out; it
// returns the process and the pid its watch command wrote.
func startLoopChild(t *testing.T, root string) (*exec.Cmd, int) {
	t.Helper()
	outFile := filepath.Join(t.TempDir(), "dev.out")
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$") //nolint:gosec // the test binary itself
	child.Dir = root
	child.Env = append(os.Environ(), devLoopEnv+"="+outFile)
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	pid := watchPID(t, filepath.Join(root, "watch.pid"))
	require.Eventually(t, func() bool {
		b, _ := os.ReadFile(outFile) //nolint:gosec // the test's own file
		return strings.Contains(string(b), "funcd dev")
	}, 60*time.Second, 50*time.Millisecond, "the child's dev serves")
	return child, pid
}

// pidWatch writes the watch command's pid, its process group, beside the project and keeps a sleep in that group, so a
// stop must end every process the command started.
const pidWatch = "    - name: web\n      dir: web-src\n      run: echo $$ > ../watch.pid; sleep 300 & exec sh build.sh --watch\n"

// scenario: dev-watch-stop — on SIGINT, SIGTERM or SIGHUP, or when dev fails after the command started, the command and
// every process it started stop.
func TestScenarioDevWatchStop(t *testing.T) {
	if runDevLoopChild(t) {
		return
	}
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			root := loopTodo(t, loopConfig(pidWatch, "  persist: true\n"))
			child, pgid := startLoopChild(t, root)
			require.True(t, groupAlive(pgid))
			require.NoError(t, child.Process.Signal(sig))
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("dev did not stop on %s", sig)
			}
			require.False(t, groupAlive(pgid), "the command's process group stopped with dev")
		})
	}
	t.Run("a failure after the start", func(t *testing.T) {
		root := loopTodo(t, loopConfig(pidWatch, ""))
		require.NoError(t, os.WriteFile(filepath.Join(root, "functions", "ts", "api-1.0.0.contract.yaml"), []byte("runtime: [\n"), 0o600))
		_, _, err := startLoop(t, root, t.TempDir())
		require.ErrorContains(t, err, "api-1.0.0.contract.yaml")
		require.False(t, groupAlive(watchPID(t, filepath.Join(root, "watch.pid"))), "the command stopped with the failed start")
	})
}

// scenario: dev-watch-crash-reap — after dev was killed with SIGKILL, the next start stops the earlier run's command
// before it starts its own.
func TestScenarioDevWatchCrashReap(t *testing.T) {
	if runDevLoopChild(t) {
		return
	}
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := loopTodo(t, loopConfig(pidWatch, "  persist: true\n"))
	child, old := startLoopChild(t, root)
	require.NoError(t, child.Process.Kill())
	_ = child.Wait()
	require.True(t, groupAlive(old), "the killed run left its command running")
	require.NoError(t, os.Remove(filepath.Join(root, "watch.pid")))

	r, _, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	r.serves(t)
	require.False(t, groupAlive(old), "the earlier run's command was reaped")
	require.NotEqual(t, old, watchPID(t, filepath.Join(root, "watch.pid")), "the new run started its own")
}

// scenario: dev-live-reload — the Site's index carries one tag; a Site change sends one event once its revision is
// current, a function change none; a --persist restart with the same builds stamps no revision.
func TestScenarioDevLiveReload(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := loopTodo(t, loopConfig(webWatch, "  liveReload: true\n  persist: true\n"))
	r, _, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	r.serves(t)
	index := siteIndex(t, r)
	require.Equal(t, 1, strings.Count(index, "new EventSource("), "one tag")
	require.Contains(t, index, r.inst.live.url())
	events := reloadEvents(t, r.inst.live.url())
	r.settled(t, "todo")

	appendTo(t, filepath.Join(root, "web-src", "site", "app.js"), "\n// saved\n")
	select {
	case <-events:
	case <-time.After(60 * time.Second):
		t.Fatalf("no reload event after a Site change; dev printed:\n%s", r.out)
	}
	replaceIn(t, filepath.Join(root, "functions/ts/api-1.0.0.mjs"), `var version = "1.0.0";`, `var version = "1.0.0-saved";`)
	r.eventually(t, func() bool { return r.listing(t).Version == "1.0.0-saved" }, "the function change serves")
	r.settled(t, "todo")
	select {
	case <-events:
		t.Fatal("a function build's change reloaded the pages")
	case <-time.After(time.Second):
	}

	port, rev := r.inst.live.port, r.settled(t, "todo")
	r.stop()
	again, _, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	again.serves(t)
	require.Equal(t, port, again.inst.live.port, "the port is kept")
	require.Equal(t, rev, again.settled(t, "todo"), "a restart with the same builds stamps no revision")
}

// scenario: dev-watch-refusals — dev stops, naming the key, on a watch it cannot run, either key without a template,
// and, under watch, a refusal no command can fix.
func TestScenarioDevWatchRefusals(t *testing.T) {
	template := map[string]string{"app/app.yaml": "name: x\nversion: 1.0.0\n", "app/resources/.keep": ""}
	for name, tc := range map[string]struct {
		files   map[string]string
		wantErr string
	}{
		"not a list":       {files: map[string]string{devConfigFile: "dev:\n  dir: app\n  watch: web\n"}, wantErr: "dev.watch is not a list"},
		"unknown key":      {files: map[string]string{devConfigFile: "dev:\n  dir: app\n  watch:\n    - name: web\n      run: x\n      dirr: web\n"}, wantErr: "dev.watch[0]: unknown key dirr"},
		"name":             {files: map[string]string{devConfigFile: "dev:\n  dir: app\n  watch:\n    - name: Web\n      run: x\n"}, wantErr: "dev.watch[0].name Web is not [a-z0-9][a-z0-9-]*"},
		"name twice":       {files: map[string]string{devConfigFile: "dev:\n  dir: app\n  watch:\n    - name: web\n      run: x\n    - name: web\n      run: y\n"}, wantErr: "dev.watch[1].name web is used twice"},
		"empty run":        {files: map[string]string{devConfigFile: "dev:\n  dir: app\n  watch:\n    - name: web\n      run: \" \"\n"}, wantErr: "dev.watch[0].run is empty"},
		"dir":              {files: map[string]string{devConfigFile: "dev:\n  dir: app\n  watch:\n    - name: web\n      run: x\n      dir: nowhere\n"}, wantErr: "/nowhere is not a directory"},
		"watch on folder":  {files: map[string]string{devConfigFile: "dev:\n  watch:\n    - name: web\n      run: x\n"}, wantErr: "dev.watch needs dev.dir to be an App template"},
		"reload on folder": {files: map[string]string{devConfigFile: "dev:\n  liveReload: true\n"}, wantErr: "dev.liveReload needs dev.dir to be an App template"},
	} {
		t.Run(name, func(t *testing.T) {
			start := devProject(t, merge(template, tc.files))
			if name == "watch on folder" || name == "reload on folder" {
				start = devProject(t, tc.files)
			}
			var cfg devConfig
			_, _, err := resolveDevRun("test", start, nil, nil, &cfg)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
	t.Run("a refusal no command fixes", func(t *testing.T) {
		requireApp(t)
		t.Setenv("TODO_API_TOKEN", todoToken)
		root := loopTodo(t, strings.Replace(loopConfig(webWatch, ""), "    - web=web\n", "", 1))
		_, _, err := startLoop(t, root, t.TempDir())
		require.ErrorContains(t, err, "images.web is a Site's image: name its build folder with --build web=<dir>")
	})
}

// scenario: dev-loop-absent — without watch and liveReload a build missing at start stops dev, and the Site serves the
// build's bytes unchanged.
func TestScenarioDevLoopAbsent(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	plain := strings.Replace(loopConfig("", ""), "  watch:\n", "", 1)

	missing := loopTodo(t, plain)
	_, _, err := startLoop(t, missing, t.TempDir())
	require.ErrorContains(t, err, "--build web="+filepath.Join(missing, "web")+": a Site's build is a folder")

	root := appCopy(t, "todo")
	require.NoError(t, os.WriteFile(filepath.Join(root, devConfigFile), []byte(plain), 0o600))
	r, _, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	r.serves(t)
	require.Nil(t, r.inst.commands)
	require.Nil(t, r.inst.live)
	index, err := os.ReadFile(filepath.Join(root, "web", "index.html"))
	require.NoError(t, err)
	require.Equal(t, string(index), siteIndex(t, r), "the Site's bytes are the build's")
}

// TestDevWatchEarlyChecks: a refusal that needs no build, such as a Secret key the shell does not hold, stops dev before
// it starts any command (ADR-0255 Decision 2).
func TestDevWatchEarlyChecks(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", "")
	require.NoError(t, os.Unsetenv("TODO_API_TOKEN"))
	root := loopTodo(t, loopConfig(pidWatch, ""))
	r, _, err := startLoop(t, root, t.TempDir())
	require.ErrorContains(t, err, "TODO_API_TOKEN")
	require.NotContains(t, r.out.String(), "watch web:", "no command started")
	require.NoFileExists(t, filepath.Join(root, "watch.pid"))
}

// TestDevRestartDelay: a failed command's delay doubles up to 30 s, and a run of 30 s or more brings it back to 1 s.
func TestDevRestartDelay(t *testing.T) {
	for _, tc := range []struct{ last, ran, wait, next time.Duration }{
		{time.Second, 0, time.Second, 2 * time.Second},
		{16 * time.Second, time.Second, 16 * time.Second, 30 * time.Second},
		{30 * time.Second, time.Second, 30 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second, time.Second, 2 * time.Second},
	} {
		wait, next := restartDelay(tc.last, tc.ran)
		require.Equal(t, [2]time.Duration{tc.wait, tc.next}, [2]time.Duration{wait, next}, "last %s, ran %s", tc.last, tc.ran)
	}
}

// TestDevSiteHoldsIndex: under watch, a Site folder that is empty or holds no index yet is a miss.
func TestDevSiteHoldsIndex(t *testing.T) {
	dir := t.TempDir()
	img := &devImage{part: partSite, site: dir, index: "index.html"}
	var miss buildMiss
	err := siteHoldsIndex("test", "web", img)
	require.ErrorAs(t, err, &miss)
	require.ErrorContains(t, err, dir+" is empty")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), nil, 0o600))
	err = siteHoldsIndex("test", "web", img)
	require.ErrorAs(t, err, &miss)
	require.ErrorContains(t, err, dir+" holds no index.html")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), nil, 0o600))
	require.NoError(t, siteHoldsIndex("test", "web", img))
}

// TestDevSiteCopy: the Site copy resolves a symlinked build folder, as packDir does, tags the root index only, and
// refuses a symlink inside the build, as packDir refuses it.
func TestDevSiteCopy(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	writeProject(t, dist, map[string]string{"index.html": "<html><head></head></html>", "app.js": "x", ".well-known/a": "y"})
	web := filepath.Join(root, "web")
	require.NoError(t, os.Symlink(dist, web))
	l := &devLiveReload{port: 4321}
	stage := t.TempDir()
	copied, err := l.siteCopy("test", stage, "web", web, "index.html")
	require.NoError(t, err)
	index, err := os.ReadFile(filepath.Join(copied, "index.html")) //nolint:gosec // the test's own copy
	require.NoError(t, err)
	require.Equal(t, "<html><head>"+l.tag()+"</head></html>", string(index))
	for _, f := range []string{"app.js", ".well-known/a"} {
		require.FileExists(t, filepath.Join(copied, f))
	}
	require.NoError(t, os.Symlink(filepath.Join(dist, "app.js"), filepath.Join(dist, "link.js")))
	_, err = l.siteCopy("test", stage, "web", web, "index.html")
	require.ErrorContains(t, err, "link.js is not a regular file or directory")
}

// TestDevWaitForBuilds: a miss is printed once and waited on; a refusal stops the wait only on a settled poll, and so
// does a miss once every command exited 0.
func TestDevWaitForBuilds(t *testing.T) {
	miss, refusal := buildMiss{error: errors.New("no build of images.web")}, errors.New("bad contract")
	type poll struct {
		stamp string
		err   error
	}
	for name, tc := range map[string]struct {
		polls    []poll
		finished bool
		want     error
	}{
		"found once settled":      {polls: []poll{{"a", miss}, {"a", miss}, {"b", nil}, {"b", nil}}},
		"an unsettled refusal":    {polls: []poll{{"a", refusal}, {"b", nil}, {"b", nil}}},
		"a settled refusal":       {polls: []poll{{"a", refusal}, {"a", refusal}}, want: refusal},
		"a miss, commands done":   {polls: []poll{{"a", miss}, {"a", miss}}, finished: true, want: miss},
		"a moving miss, not done": {polls: []poll{{"a", miss}, {"b", miss}, {"c", nil}, {"c", nil}}},
	} {
		t.Run(name, func(t *testing.T) {
			cmds := &devCommands{entries: []devWatchEntry{{Name: "web"}}, finished: map[string]bool{"web": tc.finished}}
			if !tc.finished {
				cmds.finished = map[string]bool{}
			}
			var out strings.Builder
			writef := func(format string, args ...any) error { _, err := fmt.Fprintf(&out, format, args...); return err }
			i := 0
			err := waitForBuilds(context.Background(), cmds, writef, func() (string, error) {
				p := tc.polls[min(i, len(tc.polls)-1)]
				i++
				return p.stamp, p.err
			})
			require.Equal(t, tc.want, err)
			require.LessOrEqual(t, strings.Count(out.String(), "waiting for"), 1, "a miss is printed once")
		})
	}
}

// TestDevLiveReloadTag: the tag goes before the first real </head>, not one inside a comment or a script, and at the
// end of an index without one.
func TestDevLiveReloadTag(t *testing.T) {
	const tag = "<script>T</script>"
	for name, tc := range map[string]struct{ doc, want string }{
		"head":       {"<html><head><title>x</title></head><body></body></html>", "<html><head><title>x</title>" + tag + "</head><body></body></html>"},
		"upper case": {"<HTML><HEAD></HEAD></HTML>", "<HTML><HEAD>" + tag + "</HEAD></HTML>"},
		"in comment": {"<head><!-- </head> --></head>", "<head><!-- </head> -->" + tag + "</head>"},
		"in script":  {"<head><script>'</head>'</script></head>", "<head><script>'</head>'</script>" + tag + "</head>"},
		"none":       {"<p>hi</p>", "<p>hi</p>" + tag},
	} {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, string(withTag([]byte(tc.doc), []byte(tag))))
		})
	}
}

// TestDevLiveReloadEvents: an event goes out when a current revision's Site images differ from those at the last
// event, or when the stream started, so a follow a newer revision cancelled loses none; a function change sends none.
func TestDevLiveReloadEvents(t *testing.T) {
	app := func(site, fn string) *v1.App {
		a := &v1.App{}
		a.Spec.Sites = []v1.AppSite{{Name: "web"}}
		a.Spec.Sites[0].Image = site
		a.Spec.Functions = []v1.AppFunction{{Name: "api"}}
		a.Spec.Functions[0].Image = fn
		return a
	}
	l := &devLiveReload{subs: map[chan struct{}]struct{}{}}
	ch := make(chan struct{}, 4)
	l.subs[ch] = struct{}{}
	sent := func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	l.started(app("site@1", "fn@1"))
	l.current(app("site@1", "fn@1"))
	require.False(t, sent(), "the started revision")
	l.current(app("site@1", "fn@2"))
	require.False(t, sent(), "a function change")
	// site@2's own revision was replaced before it was current: the next one, which carries it, sends the event
	l.current(app("site@2", "fn@3"))
	require.True(t, sent(), "a Site change, also through a cancelled follow")
	l.current(app("site@2", "fn@3"))
	require.False(t, sent(), "the same Site again")
}
