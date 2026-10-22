//go:build dev

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The tests of ADR-0255 and ADR-0256 run serially: each one forks watch commands.

// loopConfig is the scenarios' file, in ADR-0256's layout, where each command owns its build; extra is appended under
// dev.
func loopConfig(watch, extra string) string {
	return "dev:\n  dir: app\n  build:\n    - functions/ts\n    - functions/py\n    - web=web/dist\n" +
		"    - api=functions/ts/api-1.0.0.mjs\n    - migrate=functions/ts/migrate-1.0.0.mjs\n  watch:\n" + watch + extra
}

// webWatch is the scenario's watch entry.
const webWatch = "    - name: web\n      dir: web\n      run: sh build.sh --watch\n"

// loopBuild is the scenario's web/build.sh, a stand-in for vite build --watch: after half a second it prints that a build
// started, copies site/ into dist and, 0.3 s later, prints a line on each stream, then again on each change under
// --watch; while FAIL exists it exits 3, and while BROKEN exists it empties dist, as Vite does before a build that
// fails, prints an error and runs on.
const loopBuild = `fail() { echo "build failed" >&2; exit 3; }
copy() {
	[ -e FAIL ] && fail
	echo "build started"
	if [ -e BROKEN ]; then rm -rf dist/*; echo "build error: broken" >&2; return; fi
	mkdir -p dist && cp -R site/. dist/ && sleep 0.3 && echo "copied to dist" && echo "copy done" >&2
}
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

// loopTodo is a copy of the todo App with its Site moved to web/site, web/build.sh, and config as its
// funcdctl.config.yaml.
func loopTodo(t *testing.T, config string) string {
	t.Helper()
	root := appCopy(t, "todo")
	web := filepath.Join(root, "web")
	require.NoError(t, os.Rename(web, filepath.Join(root, "web-site")))
	require.NoError(t, os.MkdirAll(web, 0o755))
	require.NoError(t, os.Rename(filepath.Join(root, "web-site"), filepath.Join(web, "site")))
	require.NoError(t, os.WriteFile(filepath.Join(web, "build.sh"), []byte(loopBuild), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, devConfigFile), []byte(config), 0o600))
	return root
}

// startLoop runs `funcdctl dev` from root as a developer would; cache is the user cache dir dev uses without --persist.
func startLoop(t *testing.T, root, cache string) (*appRun, devConfig, error) {
	t.Helper()
	r, cfg, started := startLoopAsync(t, root, cache)
	return r, cfg, started()
}

// startLoopAsync is startLoop that returns while dev starts: started waits for it, and r.inst is set once it did. Each
// of opts changes the run's config first.
func startLoopAsync(t *testing.T, root, cache string, opts ...func(*devConfig)) (*appRun, devConfig, func() error) {
	t.Helper()
	var cfg devConfig
	dir, _, err := resolveDevRun("test", root, nil, nil, &cfg)
	require.NoError(t, err)
	cfg.cacheDir = cache
	for _, o := range opts {
		o(&cfg)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &appRun{out: &lockedBuffer{}}
	done := make(chan error, 1)
	go func() {
		inst, err := (&cli{out: r.out}).startDev(ctx, dir, cfg)
		r.inst = inst
		done <- err
	}()
	var once sync.Once
	var startErr error
	started := func() error {
		once.Do(func() { startErr = <-done })
		return startErr
	}
	stopped := false
	r.stop = func() {
		if !stopped {
			stopped = true
			cancel()
			_ = started()
			if r.inst != nil {
				_ = r.inst.stop()
			}
		}
	}
	t.Cleanup(r.stop)
	return r, cfg, started
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
	require.Contains(t, out, "waiting for images.web: "+filepath.Join(root, "web", "dist")+" is absent\n")
	require.Less(t, strings.Index(out, "watch web: "), strings.Index(out, "waiting for "), "the log file is printed first")
	index, err := os.ReadFile(filepath.Join(root, "web", "site", "index.html"))
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
	require.Contains(t, string(log), "copied to dist")
	require.Contains(t, string(log), "copy done")
	var banner strings.Builder
	require.NoError(t, (&cli{out: &banner}).printBanner(r.inst))
	require.Contains(t, banner.String(), "WATCH")
	require.Contains(t, banner.String(), logFile)

	mark := len(r.out.String())
	appendTo(t, filepath.Join(root, "web", "site", "app.js"), "\n// saved\n")
	r.eventually(t, func() bool {
		s := r.out.String()[mark:]
		return strings.Contains(s, "pushed images.web ") && strings.Contains(s, "reloaded in ")
	}, "a save prints dev's lines")
	for _, line := range []string{"build started", "copied to dist", "copy done"} {
		require.NotContains(t, r.out.String(), line, "a command's output stays in its log file")
	}
}

// scenario: dev-watch-restart — a command that exits non-zero is printed with its last lines and started again after
// 1 s, the delay doubling; a command that exits 0 is printed as finished and not started again.
func TestScenarioDevWatchRestart(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	once := "    - name: once\n      dir: web\n      run: sh build.sh\n"
	root := loopTodo(t, loopConfig(pidWatch+once, ""))
	r, _, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	r.serves(t)
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "once finished") }, "the one-shot command finishes")

	first := watchPID(t, filepath.Join(root, "watch.pid"))
	fail := filepath.Join(root, "web", "FAIL")
	require.NoError(t, os.WriteFile(fail, nil, 0o600))
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "web exited 3; restarting in 1s") }, "web fails")
	require.Contains(t, r.out.String(), "web | build failed", "its last lines")
	require.False(t, groupAlive(first), "the sleep the failed run started went with it")
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "web exited 3; restarting in 2s") }, "the delay doubles")
	require.NoError(t, os.Remove(fail))
	mark := len(r.out.String())
	appendTo(t, filepath.Join(root, "web", "site", "app.js"), "\n// fixed\n")
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
const pidWatch = "    - name: web\n      dir: web\n      run: echo $$ > ../watch.pid; sleep 300 & exec sh build.sh --watch\n"

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

	appendTo(t, filepath.Join(root, "web", "site", "app.js"), "\n// saved\n")
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
		root := loopTodo(t, strings.Replace(loopConfig(webWatch, ""), "    - web=web/dist\n", "", 1))
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
	require.ErrorContains(t, err, "--build web="+filepath.Join(missing, "web", "dist")+": a Site's build is a folder")

	root := appCopy(t, "todo")
	require.NoError(t, os.WriteFile(filepath.Join(root, devConfigFile), []byte(strings.Replace(plain, "web=web/dist", "web=web", 1)), 0o600))
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

// apiWatch rebuilds the api bundle: on each change of api-src.mjs, apiBuild copies it over api-1.0.0.mjs.
const apiWatch = "    - name: api\n      dir: functions/ts\n      run: sh api-build.sh\n"

const apiBuild = `stamp() { cksum < api-src.mjs; }
last=$(stamp)
while :; do
	sleep 0.2
	now=$(stamp)
	if [ "$now" != "$last" ]; then echo "api build started"; cp api-src.mjs api-1.0.0.mjs && echo "api built"; last=$now; fi
done
`

// buildJudged waits until the burst of a build whose last line came before the call was judged (ADR-0256 Decision 3).
func buildJudged() {
	time.Sleep(devWatchBuiltQuiet + 2*devReloadPoll)
}

// TestScenarioDevWatchBursts runs ADR-0256's scenarios that need dev running on one run, each a subtest named after
// its scenario; each leaves the builds as it found them.
func TestScenarioDevWatchBursts(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := loopTodo(t, loopConfig(webWatch+apiWatch, ""))
	ts := filepath.Join(root, "functions", "ts")
	bundle, err := os.ReadFile(filepath.Join(ts, "api-1.0.0.mjs")) //nolint:gosec // the test's own copy
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(ts, "api-src.mjs"), bundle, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(ts, "api-build.sh"), []byte(apiBuild), 0o600))
	r, _, err := startLoop(t, root, t.TempDir())
	require.NoError(t, err)
	r.serves(t)
	app := filepath.Join(root, "web", "site", "app.js")
	broken := filepath.Join(root, "web", "BROKEN")
	save := func(what string) int {
		mark := len(r.out.String())
		appendTo(t, app, "\n// "+what+"\n")
		return mark
	}
	pushed := func(mark int) {
		r.eventually(t, func() bool { return strings.Contains(r.out.String()[mark:], "pushed images.web ") }, "the Site builds")
	}
	shows := func(mark int, line string) {
		r.eventually(t, func() bool { return strings.Contains(r.out.String()[mark:], line) }, line)
	}
	fix := func() {
		require.NoError(t, os.Remove(broken))
		pushed(save("fixed"))
		buildJudged()
	}

	// scenario: dev-watch-quiet-success — a save that rebuilds the Site shows no line of its command.
	t.Run("dev-watch-quiet-success", func(t *testing.T) {
		mark := save("saved")
		pushed(mark)
		buildJudged()
		require.NotContains(t, r.out.String(), "web |", "neither the start's build nor the save's shows")
	})

	// scenario: dev-watch-silent-failure — a save that fails shows its error and no push; once fixed, a save pushes and
	// shows no line of its command.
	t.Run("dev-watch-silent-failure", func(t *testing.T) {
		require.NoError(t, os.WriteFile(broken, nil, 0o600))
		mark := save("broken")
		shows(mark, "web | build error: broken\n")
		require.NotContains(t, r.out.String()[mark:], "pushed images.web")
		require.NoError(t, os.Remove(broken))
		mark = save("fixed")
		pushed(mark)
		buildJudged()
		require.NotContains(t, r.out.String()[mark:], "web |")
	})

	// scenario: dev-watch-quick-failure — a save that fails more than 1 s plus a poll, but less than 3 s, after the last
	// line of a save that rebuilt the Site shows its error.
	t.Run("dev-watch-quick-failure", func(t *testing.T) {
		pushed(save("good"))
		// the build's burst ends devWatchBuiltQuiet after its last line, and the failing save comes inside devWatchQuiet
		time.Sleep(devWatchBuiltQuiet + 2*devReloadPoll)
		require.NoError(t, os.WriteFile(broken, nil, 0o600))
		shows(save("broken"), "web | build error: broken\n")
		fix()
	})

	// scenario: dev-watch-own-build — when two saves in one window make api rebuild and web fail, web's error shows,
	// though a build was written, and no api line. The App reloads once web builds again.
	t.Run("dev-watch-own-build", func(t *testing.T) {
		require.NoError(t, os.WriteFile(broken, nil, 0o600))
		mark := save("broken")
		replaceIn(t, filepath.Join(ts, "api-src.mjs"), `var version = "1.0.0";`, `var version = "1.0.0-saved";`)
		shows(mark, "web | build error: broken\n")
		r.eventually(t, func() bool {
			b, err := os.ReadFile(filepath.Join(ts, "api-1.0.0.mjs")) //nolint:gosec // the test's own copy
			return err == nil && strings.Contains(string(b), `"1.0.0-saved"`)
		}, "api rebuilds")
		buildJudged()
		require.NotContains(t, r.out.String()[mark:], "api |")
		fix()
		r.eventually(t, func() bool { return r.listing(t).Version == "1.0.0-saved" }, "api's build serves")
	})
}

// scenario: dev-watch-start-failure — a first build that fails shows its error, with no build yet or an earlier good
// one, which the failing build empties; dev starts once the build is fixed.
func TestScenarioDevWatchStartFailure(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	for name, earlier := range map[string]bool{"no build yet": false, "an earlier good build": true} {
		t.Run(name, func(t *testing.T) {
			root := loopTodo(t, loopConfig(webWatch, ""))
			web := filepath.Join(root, "web")
			if earlier {
				require.NoError(t, copyBuildTree(filepath.Join(web, "site"), filepath.Join(web, "dist")))
			}
			require.NoError(t, os.WriteFile(filepath.Join(web, "BROKEN"), nil, 0o600))
			r, _, started := startLoopAsync(t, root, t.TempDir())
			r.eventually(t, func() bool { return strings.Contains(r.out.String(), "web | build error: broken\n") }, "the error shows")
			require.NoError(t, os.Remove(filepath.Join(web, "BROKEN")))
			appendTo(t, filepath.Join(web, "site", "app.js"), "\n// fixed\n")
			require.NoError(t, started())
			r.serves(t)
		})
	}
}

// Issue #1171: the watch command's first build emptied a Site's folder after dev's start found the folder's earlier
// build and before dev pushed it, as Vite empties dist before a build, and the start failed on the push, so the
// build's own error never showed (a slow host, as -race on Linux, hit it every time). The start now waits for the
// build again.
func TestIssue1171_StartWaitsWhenASitesBuildVanishesBeforeItsPush(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := loopTodo(t, loopConfig(webWatch, ""))
	web := filepath.Join(root, "web")
	dist := filepath.Join(web, "dist")
	require.NoError(t, copyBuildTree(filepath.Join(web, "site"), dist))
	require.NoError(t, os.WriteFile(filepath.Join(web, "BROKEN"), nil, 0o600))
	var once sync.Once
	r, _, started := startLoopAsync(t, root, t.TempDir(), func(cfg *devConfig) {
		cfg.beforeSitePush = func(string) {
			once.Do(func() {
				for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
					if entries, err := os.ReadDir(dist); err == nil && len(entries) == 0 {
						return
					}
				}
			})
		}
	})
	r.eventually(t, func() bool { return strings.Contains(r.out.String(), "web | build error: broken\n") }, "the error shows")
	require.Contains(t, r.out.String(), "waiting for images.web: ", "the start waits for the build")
	require.NoError(t, os.Remove(filepath.Join(web, "BROKEN")))
	appendTo(t, filepath.Join(web, "site", "app.js"), "\n// fixed\n")
	require.NoError(t, started())
	r.serves(t)
}

// TestDevWatchExits: an exit 0 judges its command's open burst before the finished line, and a non-zero exit drops it,
// so its lines print once, with the exit, and never again as a burst (ADR-0256 Decision 5).
func TestDevWatchExits(t *testing.T) {
	dir := t.TempDir()
	out := &lockedBuffer{}
	writef := func(format string, args ...any) error { _, err := fmt.Fprintf(out, format, args...); return err }
	entries := []devWatchEntry{
		{Name: "once", Run: "echo hello; exit 0", Dir: dir},
		{Name: "fail", Run: "[ -e ran ] && exec sleep 300; touch ran; echo oops; exit 3", Dir: dir},
	}
	c, err := startDevCommands(context.Background(), "test", filepath.Join(dir, "process"), entries, nil, writef)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.stop() })
	require.Eventually(t, func() bool {
		return strings.Contains(out.String(), "once finished") && strings.Contains(out.String(), "fail exited 3")
	}, 30*time.Second, 50*time.Millisecond, "both commands exit")
	require.Contains(t, out.String(), "once | hello\nonce finished\n", "the burst before the finished line")
	time.Sleep(devWatchQuiet + 2*devReloadPoll)
	require.Equal(t, 1, strings.Count(out.String(), "fail | oops"), "the failed run's lines print with its exit only")
}

// TestDevBurst: a burst ends once the log file stayed still for its quiet window, or for its shorter one once an owned
// build changed; it prints its last lines only when no owned build changed since the poll before its first line, and
// always for a command that owns none; an exit 0 judges the open burst, another drops it.
func TestDevBurst(t *testing.T) {
	const ms = time.Millisecond
	type op struct {
		at      time.Duration // when the poll runs; an op with exit set runs no poll
		write   string        // what the command wrote before it
		built   bool          // whether a file of an owned build was rewritten before it
		emptied bool          // whether the owned build's files were removed before it
		exit    string        // "ok" or "fail": the command exits instead
		want    string
	}
	for name, tc := range map[string]struct {
		owns bool
		ops  []op
	}{
		"a failed build prints": {owns: true, ops: []op{
			{at: 300 * ms, write: "build started\nbuild error: broken\n"},
			{at: 600 * ms},
			{at: 1400 * ms, want: "web | build started\nweb | build error: broken\n"},
		}},
		"a build ends its burst sooner and a failure after it prints": {owns: true, ops: []op{
			{at: 300 * ms, write: "build started\n"},
			{at: 600 * ms, write: "copied\n", built: true},
			{at: 900 * ms},
			{at: 1100 * ms},
			{at: 1400 * ms, write: "build error: broken\n"},
			{at: 1700 * ms},
			{at: 2500 * ms, want: "web | build error: broken\n"},
		}},
		"an emptied build that fails prints": {owns: true, ops: []op{
			{at: 300 * ms, write: "build started\n", emptied: true},
			{at: 600 * ms, write: "build error: broken\n"},
			{at: 1200 * ms},
			{at: 1700 * ms, want: "web | build started\nweb | build error: broken\n"},
		}},
		"an emptied build written again is a build": {owns: true, ops: []op{
			{at: 300 * ms, write: "build started\n", emptied: true},
			{at: 900 * ms},
			{at: 1100 * ms, write: "built in 800ms\n", built: true},
			{at: 1400 * ms},
			{at: 1600 * ms},
			{at: 2700 * ms},
		}},
		"a pause after the write shorter than the built window stays in the burst": {owns: true, ops: []op{
			{at: 300 * ms, write: "build started\n"},
			{at: 600 * ms, write: "computing gzip size...\n", built: true},
			{at: 900 * ms},
			{at: 950 * ms, write: "dist/index.html 0.5 kB\nbuilt in 650ms\n"},
			{at: 1250 * ms},
			{at: 1400 * ms},
			{at: 2100 * ms},
		}},
		"a build before the poll that read the first line counts": {owns: true, ops: []op{
			{at: 300 * ms, write: "copied\n", built: true},
			{at: 600 * ms},
			{at: 1400 * ms},
		}},
		"no owned build prints every burst": {ops: []op{
			{at: 300 * ms, write: "Found 0 errors\n"},
			{at: 1400 * ms, want: "web | Found 0 errors\n"},
		}},
		"an exit 0 judges the open burst": {owns: true, ops: []op{
			{at: 300 * ms, write: "build error: broken\n"},
			{write: "done\n", exit: "ok", want: "web | build error: broken\nweb | done\n"},
		}},
		"an exit 0 after a build prints nothing": {owns: true, ops: []op{
			{write: "copied\n", built: true, exit: "ok"},
		}},
		"a non-zero exit drops the burst": {owns: true, ops: []op{
			{at: 300 * ms, write: "build failed\n"},
			{write: "exiting\n", exit: "fail"},
			{at: 2000 * ms},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			b, w, files := testBurst(t, tc.owns)
			start := time.Now()
			b.started()
			for i, o := range tc.ops {
				_, err := w.WriteString(o.write)
				require.NoError(t, err)
				if o.emptied {
					clear(files)
				}
				if o.built {
					files["dist/index.html"] += "+"
				}
				got := ""
				if o.exit == "" {
					got = b.poll(start.Add(o.at))
				} else {
					got = b.exited(o.exit == "ok")
				}
				require.Equal(t, o.want, got, "op %d", i)
			}
		})
	}
	t.Run("a long burst prints its last lines", func(t *testing.T) {
		b, w, _ := testBurst(t, true)
		start := time.Now()
		b.started()
		var want strings.Builder
		for i := range 10000 {
			_, err := fmt.Fprintf(w, "line %05d\n", i)
			require.NoError(t, err)
			if i >= 10000-devWatchLastLines {
				fmt.Fprintf(&want, "web | line %05d\n", i)
			}
		}
		require.Empty(t, b.poll(start.Add(300*time.Millisecond)))
		require.Len(t, b.buf, devWatchTail, "the burst keeps its last bytes")
		require.Equal(t, want.String(), b.poll(start.Add(1400*time.Millisecond)))
	})
}

// testBurst is a burst on a fresh log file, with windows of 1 s and 400 ms, w the command's end of the file, and the
// owned build's files, by path, which the burst reads when it owns one.
func testBurst(t *testing.T, owns bool) (*devBurst, *os.File, map[string]string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.log")
	w, err := os.Create(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	r, err := os.Open(path) //nolint:gosec // the test's own file
	require.NoError(t, err)
	files := map[string]string{"dist/index.html": "1", "dist/app.js": "1"}
	b := &devBurst{name: "web", log: r, quiet: time.Second, builtQuiet: 400 * time.Millisecond}
	if owns {
		b.files = func() map[string]string { return maps.Clone(files) }
	}
	t.Cleanup(func() {
		b.close()
		_ = w.Close()
	})
	return b, w, files
}

// TestDevWatchOwnership: a command owns the build paths inside its dir: a key=path's path, present or not, and the two
// places a --build folder holds a JavaScript build of an image no key=path names; a Site and a Python build have no
// folder path, and a dir that only shares a build's prefix owns nothing.
func TestDevWatchOwnership(t *testing.T) {
	root := t.TempDir()
	at := func(p string) string { return filepath.Join(root, p) }
	used := map[string]*devImage{
		"web":    {part: partSite},
		"api":    {part: partFunction, runtime: "nodejs22"},
		"worker": {part: partFunction, runtime: "nodejs22"},
		"report": {part: partFunction, runtime: "python3.12"},
	}
	builds := watchBuilds([]string{at("functions/ts")}, map[string]string{"web": at("web/dist"), "api": at("functions/ts/api-1.0.0.mjs")}, used)
	ts := []string{at("functions/ts/api-1.0.0.mjs"), at("functions/ts/dist/worker.mjs"), at("functions/ts/worker.mjs")}
	require.Equal(t, append(slices.Clone(ts), at("web/dist")), builds)
	require.Equal(t, []string{at("web/dist")}, ownedPaths(at("web"), builds))
	require.Equal(t, ts, ownedPaths(at("functions/ts"), builds))
	require.Equal(t, builds, ownedPaths(root, builds), "the file's directory holds every build")
	require.Empty(t, ownedPaths(at("we"), builds), "a dir that only shares a prefix")
	require.Empty(t, ownedPaths(at("functions/ts/src"), builds), "a dir beside the builds")
}

// TestDevLiveReloadWithoutWatch: live reload starts before anything created dev's process dir, as when the file names no
// watch command.
func TestDevLiveReloadWithoutWatch(t *testing.T) {
	l, err := startLiveReload("test", filepath.Join(t.TempDir(), "process"))
	require.NoError(t, err)
	t.Cleanup(l.stop)
	require.NotZero(t, l.port)
}
