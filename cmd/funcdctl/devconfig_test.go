//go:build dev

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// startDevFrom resolves start's funcdctl.config.yaml with no flags given, as `funcdctl dev` in start does, and boots
// dev with dev's output written to out.
func startDevFrom(t *testing.T, start string, out io.Writer, cfg devConfig) (*devInstance, error) {
	t.Helper()
	if cfg.cacheDir == "" {
		cfg.cacheDir = t.TempDir()
	}
	dir, _, err := resolveDevRun("test", start, nil, nil, &cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: out}).startDev(ctx, dir, cfg)
	t.Cleanup(func() {
		cancel()
		if inst != nil {
			_ = inst.stop()
		}
	})
	return inst, err
}

// secretFunction is Function api declaring Secret api-keys with key key and returning its value read through ctx.secret.
func secretFunction(key string) map[string]string {
	return nodeFunction("api", "  secrets:\n    - name: api-keys\n      keys:\n        - "+key+"\n  bindings:\n    secret:\n      - api-keys\n",
		"export async function handle(ctx) { return { key: await ctx.secret.get('"+key+"') }; }\n")
}

func TestDevConfigDecodeIsStrict(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		file, wantErr string
	}{
		"empty":            {file: ""},
		"empty dev":        {file: "dev:\n"},
		"dev only":         {file: "dev:\n  persist: true\n  gport: 3005\n  env:\n    K: v\n"},
		"nested platform":  {file: "dev:\n  platform:\n    runtime:\n      handOutSettle: 100ms\n    eventing:\n      blobPollInterval: 2s\n    funclog:\n      segmentMaxAge: 3s\n"},
		"other root key":   {file: "dev: {}\nserver: x\n", wantErr: "funcdctl.config.yaml: unknown key server"},
		"unknown dev key":  {file: "dev:\n  port: 1\n", wantErr: "funcdctl.config.yaml: unknown key dev.port"},
		"other platform":   {file: "dev:\n  platform:\n    runtime:\n      bootTimeout: 1s\n", wantErr: "platform takes runtime.handOutSettle, eventing.blobPollInterval and funclog.segmentMaxAge, not runtime.bootTimeout"},
		"platform section": {file: "dev:\n  platform:\n    auth:\n      mode: open\n", wantErr: "not auth"},
		"bad duration":     {file: "dev:\n  platform:\n    funclog:\n      segmentMaxAge: soon\n", wantErr: `dev.platform.funclog.segmentMaxAge "soon" is not a positive duration`},
		"wrong type":       {file: "dev:\n  gport: many\n", wantErr: "funcdctl.config.yaml: dev:"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeDevFile("test", []byte(tc.file))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestDevConfigEnvFileFormat(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		file    string
		want    map[string]string
		wantErr string
	}{
		"pairs, comments and blanks": {file: "# a comment\n\nA=1\n  # indented comment\nB=two words\r\n", want: map[string]string{"A": "1", "B": "two words"}},
		"one pair of quotes":         {file: "A=\"quoted\"\nB=\"\"x\"\"\nC=\"\n", want: map[string]string{"A": "quoted", "B": "\"x\"", "C": "\""}},
		"empty value":                {file: "A=\n", want: map[string]string{"A": ""}},
		"value with =":               {file: "A=b=c\n", want: map[string]string{"A": "b=c"}},
		"no =":                       {file: "A=1\nB\n", wantErr: ":2: not KEY=value"},
		"export prefix":              {file: "export A=1\n", wantErr: ":1: not KEY=value"},
		"space before key":           {file: " A=1\n", wantErr: ":1: not KEY=value"},
		"bad key":                    {file: "1A=1\n", wantErr: ":1: not KEY=value"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".env")
			require.NoError(t, os.WriteFile(path, []byte(tc.file), 0o600))
			got, err := readEnvFile("test", path)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, path+tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	got, err := readEnvFile("test", filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)
	require.Empty(t, got, "a missing envFile reads as empty")
}

func TestDevConfigPrecedence(t *testing.T) {
	t.Parallel()
	const file = "dev:\n  dir: folder\n  persist: true\n  persistTo: state\n  gport: 3005\n  s3port: 3006\n  cport: 3007\n"
	t.Run("the persist default is beside the file", func(t *testing.T) {
		start := devProject(t, map[string]string{devConfigFile: "dev:\n  persist: true\n"})
		var cfg devConfig
		_, _, err := resolveDevRun("test", start, nil, nil, &cfg)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(start, devPersistDir), cfg.persistTo)
	})
	for name, tc := range map[string]struct {
		args                []string
		wantDir             string
		wantPersist         bool
		wantPersistTo       string
		wantG, wantS, wantC int
	}{
		"the file":             {wantDir: "folder", wantPersist: true, wantPersistTo: "state", wantG: 3005, wantS: 3006, wantC: 3007},
		"a flag replaces":      {args: []string{"--gport", "4005", "--persist=false"}, wantDir: "folder", wantPersistTo: "state", wantG: 4005, wantS: 3006, wantC: 3007},
		"a zero flag replaces": {args: []string{"--s3port", "0"}, wantDir: "folder", wantPersist: true, wantPersistTo: "state", wantG: 3005, wantC: 3007},
		"persist-to flag":      {args: []string{"--persist-to", "elsewhere"}, wantDir: "folder", wantPersist: true, wantPersistTo: "!elsewhere", wantG: 3005, wantS: 3006, wantC: 3007},
		"the dir argument":     {args: []string{"other"}, wantDir: "!other", wantPersist: true, wantPersistTo: "state", wantG: 3005, wantS: 3006, wantC: 3007},
	} {
		t.Run(name, func(t *testing.T) {
			start := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(start, devConfigFile), []byte(file), 0o600))
			cmd := (&cli{out: io.Discard}).devCmd()
			require.NoError(t, cmd.Flags().Parse(tc.args))
			var cfg devConfig
			cfg.persistTo, _ = cmd.Flags().GetString("persist-to")
			cfg.persist, _ = cmd.Flags().GetBool("persist")
			cfg.gport, _ = cmd.Flags().GetInt("gport")
			cfg.s3port, _ = cmd.Flags().GetInt("s3port")
			cfg.cport, _ = cmd.Flags().GetInt("cport")
			dir, _, err := resolveDevRun("test", start, cmd.Flags().Args(), cmd.Flags(), &cfg)
			require.NoError(t, err)
			rel := func(want string) string {
				if strings.HasPrefix(want, "!") {
					return strings.TrimPrefix(want, "!")
				}
				return filepath.Join(start, want)
			}
			require.Equal(t, rel(tc.wantDir), dir)
			require.Equal(t, tc.wantPersist, cfg.persist)
			require.Equal(t, rel(tc.wantPersistTo), cfg.persistTo)
			require.Equal(t, []int{tc.wantG, tc.wantS, tc.wantC}, []int{cfg.gport, cfg.s3port, cfg.cport})
		})
	}
}

// scenario: dev-config-absent — with no file, dev runs from its flags as ADR-0247 says, with Decision 4's defaults.
func TestScenarioDevConfigAbsent(t *testing.T) {
	t.Parallel()
	start := t.TempDir()
	cfg := devConfig{gport: 7}
	dir, st, err := resolveDevRun("test", start, nil, nil, &cfg)
	require.NoError(t, err)
	require.Nil(t, st)
	require.Equal(t, start, dir)
	require.Equal(t, 7, cfg.gport, "the flags stand")
	require.Equal(t, devPace{handOutSettle: 250 * time.Millisecond, blobPollInterval: time.Second, segmentMaxAge: time.Second}, cfg.pace)

	requireNode(t)
	writeProject(t, start, nodeFunction("api", "", "export function handle() { return { ok: true }; }\n"))
	inst, err := startDevFrom(t, start, io.Discard, devConfig{})
	require.NoError(t, err)
	eventuallyServes(t, inst.gatewayURL+"/function/api", `{"data":{}}`, 200, `"ok":true`)
}

// scenario: dev-config-strict — dev stops, naming the key, on a key the file may not hold, a malformed envFile line,
// and an App-only key when dev.dir is not a template (the decode itself is TestDevConfigDecodeIsStrict's).
func TestScenarioDevConfigStrict(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		files   map[string]string
		args    []string
		wantErr string
	}{
		"unknown key":      {files: map[string]string{devConfigFile: "dev:\n  ports: 1\n"}, wantErr: "unknown key dev.ports"},
		"platform key":     {files: map[string]string{devConfigFile: "dev:\n  platform:\n    eventing:\n      cronTick: 1s\n"}, wantErr: "not eventing.cronTick"},
		"malformed env":    {files: map[string]string{devConfigFile: "dev:\n  envFile: .env\n", ".env": "A=1\nnot a pair\n"}, wantErr: ".env:2: not KEY=value"},
		"values on folder": {files: map[string]string{devConfigFile: "dev:\n  values:\n    - v.yaml\n"}, wantErr: "dev.values needs dev.dir to be an App template"},
		"build on folder":  {files: map[string]string{devConfigFile: "dev:\n  build:\n    - functions/ts\n"}, wantErr: "dev.build needs dev.dir to be an App template"},
		"hooks on folder":  {files: map[string]string{devConfigFile: "dev:\n  hooks: true\n"}, wantErr: "dev.hooks needs dev.dir to be an App template"},
		"apply on folder":  {files: map[string]string{devConfigFile: "dev:\n  apply:\n    - grant.yaml\n"}, wantErr: "dev.apply needs dev.dir to be an App template"},
	} {
		t.Run(name, func(t *testing.T) {
			start := devProject(t, tc.files)
			var cfg devConfig
			_, _, err := resolveDevRun("test", start, tc.args, nil, &cfg)
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
	t.Run("a dir argument ignores App-only keys", func(t *testing.T) {
		start := devProject(t, map[string]string{devConfigFile: "dev:\n  hooks: true\n"})
		var cfg devConfig
		_, _, err := resolveDevRun("test", start, []string{start}, nil, &cfg)
		require.NoError(t, err)
	})
}

// scenario: dev-config-secret-sources — a declared Secret no document defines takes each key from the shell, else
// envFile, else env; with none, dev stops before the platform starts, naming the key and the three places.
func TestScenarioDevConfigSecretSources(t *testing.T) {
	const key = "DEVCFG_SECRET_SOURCES_KEY"
	start := devProject(t, merge(secretFunction(key), map[string]string{
		devConfigFile: "dev:\n  env:\n    " + key + ": from-env\n  envFile: .env\n",
		".env":        key + "=from-envfile\n",
	}))
	value := func(t *testing.T) (string, error) {
		t.Helper()
		var cfg devConfig
		dir, _, err := resolveDevRun("test", start, nil, nil, &cfg)
		require.NoError(t, err)
		folder, err := loadDevFolder("test", dir)
		require.NoError(t, err)
		objs, err := folder.declaredSecrets("test", cfg.secrets)
		if err != nil {
			return "", err
		}
		require.Len(t, objs, 1)
		return string(objs[0].(*v1.Secret).Spec.Data[key]), nil
	}

	t.Setenv(key, "from-shell")
	got, err := value(t)
	require.NoError(t, err)
	require.Equal(t, "from-shell", got, "the shell wins")

	require.NoError(t, os.Unsetenv(key))
	got, err = value(t)
	require.NoError(t, err)
	require.Equal(t, "from-envfile", got, "then the envFile")

	require.NoError(t, os.WriteFile(filepath.Join(start, ".env"), nil, 0o600))
	got, err = value(t)
	require.NoError(t, err)
	require.Equal(t, "from-env", got, "then dev.env")

	require.NoError(t, os.WriteFile(filepath.Join(start, devConfigFile), []byte("dev:\n  envFile: .env\n"), 0o600))
	_, err = value(t)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.EqualError(t, err, "test: Function api of "+filepath.Join(start, "api.yaml")+" declares Secret api-keys with key "+key+
		"; set it in the shell, in .env or under dev.env in funcdctl.config.yaml")

	requireNode(t)
	_, err = startDevFrom(t, start, io.Discard, devConfig{})
	require.ErrorContains(t, err, "declares Secret api-keys with key "+key, "dev stops before the platform starts")

	require.NoError(t, os.WriteFile(filepath.Join(start, ".env"), []byte(key+"=\"served\"\n"), 0o600))
	inst, err := startDevFrom(t, start, io.Discard, devConfig{})
	require.NoError(t, err)
	eventuallyServes(t, inst.gatewayURL+"/function/api", `{"data":{}}`, 200, `"key":"served"`)
}

// scenario: dev-config-reload — a change to env or to the envFile's contents reloads; a change that does not decode is
// printed and dev runs on; another key prints that a restart applies it.
func TestScenarioDevConfigReload(t *testing.T) {
	requireNode(t)
	const key = "DEVCFG_RELOAD_KEY"
	require.NoError(t, os.Unsetenv(key))
	start := devProject(t, merge(secretFunction(key), map[string]string{
		devConfigFile: "dev:\n  env:\n    " + key + ": one\n  envFile: .env\n",
	}))
	var logs lockedBuffer
	inst, err := startDevFrom(t, start, &logs, devConfig{})
	require.NoError(t, err)
	url := inst.gatewayURL + "/function/api"
	eventuallyServes(t, url, `{"data":{}}`, 200, `"key":"one"`)

	writeProject(t, start, map[string]string{devConfigFile: "dev:\n  env:\n    " + key + ": two\n  envFile: .env\n"})
	eventuallyServes(t, url, `{"data":{}}`, 200, `"key":"two"`)

	writeProject(t, start, map[string]string{".env": key + "=three\n"})
	eventuallyServes(t, url, `{"data":{}}`, 200, `"key":"three"`)

	writeProject(t, start, map[string]string{devConfigFile: "dev:\n  gport: 1\n  env:\n    " + key + ": two\n  envFile: .env\n"})
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), "funcdctl.config.yaml: dev.gport changed; restart funcdctl dev to apply it")
	}, 10*time.Second, 50*time.Millisecond, "%s", logs.String())

	writeProject(t, start, map[string]string{devConfigFile: "dev:\n  nope: true\n"})
	require.Eventually(t, func() bool { return strings.Contains(logs.String(), "unknown key dev.nope") }, 10*time.Second, 50*time.Millisecond)
	eventuallyServes(t, url, `{"data":{}}`, 200, `"key":"three"`)
}

// scenario: dev-config-cadences (folder mode) — with no platform section, a blob EventSource of the folder polls every
// second; with dev.platform.eventing.blobPollInterval 5s, an upload waits for that poll. Each case first waits for one
// object, so a poll has just run, then times the next.
func TestScenarioDevConfigCadences(t *testing.T) {
	requireRuntime(t)
	for _, tc := range []struct {
		name     string
		platform string
		seenIn   bool
	}{
		{name: "default", seenIn: true},
		{name: "blobPollInterval", platform: "  platform:\n    eventing:\n      blobPollInterval: 5s\n", seenIn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := devProject(t, merge(
				nodeFunction("seen", "  kv:\n    - name: seen-kv\n      tables:\n        - name: seen\n"+
					"  bindings:\n    kv:\n      - alias: seen\n        store: seen-kv\n        table: seen\n",
					"export async function handle(ctx, e) { await ctx.kv.put('seen', e.data.key.replaceAll('/', '_'), '1'); return {}; }\n"),
				map[string]string{
					devConfigFile: "dev:\n  persist: true\n" + tc.platform,
					"inbox.yaml":  "apiVersion: funcd.io/v1alpha1\nkind: Bucket\nmetadata:\n  name: inbox\nspec:\n  prefixes:\n    - name: in\n",
					"uploads.yaml": "apiVersion: funcd.io/v1alpha1\nkind: EventSource\nmetadata:\n  name: uploads\nspec:\n  blob:\n" +
						"    bucket: inbox\n    events:\n      - name: added\n        prefix: in/\n",
					"record.yaml": "apiVersion: funcd.io/v1alpha1\nkind: Sensor\nmetadata:\n  name: record\nspec:\n  on:\n" +
						"    - name: added\n      source: uploads\n      event: added\n  do:\n    - name: note\n      on: added\n      function: seen\n",
				}))
			inst, err := startDevFrom(t, start, io.Discard, devConfig{persistTo: t.TempDir()})
			require.NoError(t, err)
			s3 := devS3Client(t, inst)
			ctx := context.Background()
			upload := func(name string) {
				t.Helper()
				bucket, key := "inbox", "in/"+name
				require.Eventually(t, func() bool {
					_, perr := s3.PutObject(ctx, &awss3.PutObjectInput{Bucket: &bucket, Key: &key, Body: bytes.NewReader([]byte(name))})
					return perr == nil
				}, 30*time.Second, 100*time.Millisecond, "the upload of %s", name)
			}
			seen := func(name string) bool {
				_, found, gerr := inst.kv.Get(ctx, "default/seen-kv/seen/in_"+name)
				return gerr == nil && found
			}
			upload("first")
			require.Eventually(t, func() bool { return seen("first") }, 60*time.Second, 50*time.Millisecond, "the first upload reaches its Sensor")
			upload("second")
			got := false
			deadline := time.Now().Add(3 * time.Second)
			for !got && time.Now().Before(deadline) {
				got = seen("second")
				time.Sleep(50 * time.Millisecond)
			}
			require.Equal(t, tc.seenIn, got, "the second upload reaches its Sensor within 3s")
		})
	}
}

// scenario: dev-config-cadences (logs and saves) — with no platform section, `funcdctl logs` shows a line within about
// a second, and a change saved right after another is not held by production's 2s hand-out settle: the second save
// serves at least a second sooner than under dev.platform.runtime.handOutSettle 2s.
func TestScenarioDevConfigCadencesLogsAndSaves(t *testing.T) {
	requireNode(t)
	build := func(v int) string {
		return "export function handle(ctx) { ctx.log('cadence-line-" + strconv.Itoa(v) + "'); return { v: " + strconv.Itoa(v) + " }; }\n"
	}
	secondSave := func(t *testing.T, platform string) time.Duration {
		t.Helper()
		start := devProject(t, merge(nodeFunction("api", "", build(1)), map[string]string{devConfigFile: "dev:\n" + platform}))
		inst, err := startDevFrom(t, start, io.Discard, devConfig{})
		require.NoError(t, err)
		url := inst.gatewayURL + "/function/api"
		eventuallyServes(t, url, `{"data":{}}`, 200, `"v":1`)
		writeProject(t, start, map[string]string{"api.mjs": build(2)})
		eventuallyServes(t, url, `{"data":{}}`, 200, `"v":2`)
		saved := time.Now()
		writeProject(t, start, map[string]string{"api.mjs": build(3)})
		eventuallyServes(t, url, `{"data":{}}`, 200, `"v":3`)
		return time.Since(saved)
	}

	t.Run("logs", func(t *testing.T) {
		start := devProject(t, nodeFunction("api", "", build(7)))
		inst, err := startDevFrom(t, start, io.Discard, devConfig{})
		require.NoError(t, err)
		eventuallyServes(t, inst.gatewayURL+"/function/api", `{"data":{}}`, 200, `"v":7`)
		called := time.Now()
		require.Eventually(t, func() bool {
			lines, lerr := inst.client.Logs(context.Background(), devNamespace, "api", sdk.LogsOptions{})
			return lerr == nil && slices.ContainsFunc(lines, func(l logread.Line) bool { return strings.Contains(l.Body, "cadence-line-7") })
		}, 30*time.Second, 100*time.Millisecond)
		require.Less(t, time.Since(called), 3*time.Second, "the line shows within about a second, not after 10s")
	})
	t.Run("saves", func(t *testing.T) {
		fast := secondSave(t, "")
		slow := secondSave(t, "  platform:\n    runtime:\n      handOutSettle: 2s\n")
		t.Logf("second save served after %s by default, %s under a 2s settle", fast, slow)
		require.Less(t, fast+time.Second, slow, "the default settle does not hold a back-to-back save for 2s")
	})
}

// todoConfigFile is ADR-0254's scenario file for the todo App's copy.
const todoConfigFile = `dev:
  dir: app
  values:
    - values/greeting.yaml
  build:
    - functions/ts
    - functions/py
    - web=web
    - api=functions/ts/api-1.0.0.mjs
    - migrate=functions/ts/migrate-1.0.0.mjs
  hooks: true
  persist: true
  env:
    TODO_API_TOKEN: dev-token
  envFile: .env
`

// startTodoFrom writes ADR-0254's scenario file into a copy of the todo App and runs `funcdctl dev args...` there,
// as a developer would from the copy's root.
func startTodoFrom(t *testing.T, args ...string) (*appRun, string) {
	t.Helper()
	root := appCopy(t, "todo")
	require.NoError(t, os.WriteFile(filepath.Join(root, devConfigFile), []byte(todoConfigFile), 0o600))
	cmd := (&cli{out: io.Discard}).devCmd()
	require.NoError(t, cmd.Flags().Parse(args))
	var cfg devConfig
	cfg.persistTo, _ = cmd.Flags().GetString("persist-to")
	cfg.persist, _ = cmd.Flags().GetBool("persist")
	cfg.hooks, _ = cmd.Flags().GetBool("hooks")
	cfg.values, _ = cmd.Flags().GetStringArray("values")
	cfg.builds, _ = cmd.Flags().GetStringArray("build")
	cfg.apply, _ = cmd.Flags().GetStringArray("apply")
	for i, v := range cfg.values {
		cfg.values[i] = filepath.Join(root, v)
	}
	dir, _, err := resolveDevRun("test", root, cmd.Flags().Args(), cmd.Flags(), &cfg)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "app"), dir)
	cfg.cacheDir = t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	out := &lockedBuffer{}
	inst, err := (&cli{out: out}).startDev(ctx, dir, cfg)
	t.Cleanup(func() {
		cancel()
		if inst != nil {
			_ = inst.stop()
		}
	})
	require.NoError(t, err)
	return &appRun{inst: inst, out: out, stop: cancel}, root
}

// scenario: dev-config-runs — `funcdctl dev` with no argument runs as dev-todo -f values/greeting.yaml --hooks
// --persist does, and the missing .env reads as empty.
func TestScenarioDevConfigRuns(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", "")
	require.NoError(t, os.Unsetenv("TODO_API_TOKEN"))
	r, root := startTodoFrom(t)
	r.serves(t)
	r.eventually(t, func() bool {
		b := r.listing(t)
		return b.Greeting == "good morning from the todo App" && b.Migration.event() == "install" && b.Release.event() == "install"
	}, "the values file and the hooks apply")
	status, _ := r.call(t, todoRequest{Op: "add", Title: "milk", Token: "dev-token"})
	require.Equal(t, http.StatusOK, status, "the App's Secret holds dev.env's TODO_API_TOKEN")
	require.DirExists(t, filepath.Join(root, devPersistDir, "metastore"), "--persist under the file's directory")
}

// scenario: dev-config-flag-wins — `funcdctl dev --hooks=false -f values/backup.yaml` calls no hook, the greeting is
// the template's default and the App has its backup schedule: each flag replaced its key.
func TestScenarioDevConfigFlagWins(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	r, _ := startTodoFrom(t, "--hooks=false", "-f", "values/backup.yaml")
	r.serves(t)
	r.eventually(t, func() bool {
		obj, err := r.inst.client.Get(context.Background(), v1.KindApp, devNamespace, "todo")
		return err == nil && len(obj.(*v1.App).Spec.BackupSchedules) == 1
	}, "the App has its backup schedule")
	b := r.listing(t)
	require.Equal(t, "hello from the todo App", b.Greeting, "the template's default, not values/greeting.yaml's")
	require.Nil(t, b.Migration, "no preApply hook ran")
	require.Nil(t, b.Release, "no postApply hook ran")
}

// scenario: dev-config-reload (App mode) — a change to the file's values or env redeploys the App.
func TestScenarioDevConfigReloadApp(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", "")
	require.NoError(t, os.Unsetenv("TODO_API_TOKEN"))
	r, root := startTodoFrom(t, "--hooks=false")
	r.serves(t)
	r.eventually(t, func() bool { return r.listing(t).Greeting == "good morning from the todo App" }, "the values file applies")

	changed := strings.Replace(todoConfigFile, "    - values/greeting.yaml\n", "", 1)
	changed = strings.Replace(changed, "TODO_API_TOKEN: dev-token", "TODO_API_TOKEN: next-token", 1)
	require.NoError(t, os.WriteFile(filepath.Join(root, devConfigFile), []byte(changed), 0o600))
	r.eventually(t, func() bool { return r.listing(t).Greeting == "hello from the todo App" }, "dropping the values file redeploys")
	r.eventually(t, func() bool {
		status, _ := r.call(t, todoRequest{Op: "add", Title: "milk", Token: "next-token"})
		return status == http.StatusOK
	}, "the changed env reaches the App's Secret")
}

// scenario: dev-config-cadences (App mode) — with no platform section, a todo's upload reaches the App's intake
// Function, through its blob EventSource and Sensor, within a few seconds, not after production's 15s poll.
func TestScenarioDevConfigCadencesApp(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	r, _ := startTodoFrom(t, "--hooks=false")
	r.serves(t)
	status, _ := r.call(t, todoRequest{Op: "add", Title: "milk", Token: todoToken})
	require.Equal(t, http.StatusOK, status)
	post := func(body string) string {
		resp, err := http.Post(r.inst.gatewayURL+"/api", "application/json", strings.NewReader(body))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode, "%s", b)
		return string(b)
	}
	post(`{"op":"attach","id":"milk","name":"list.txt","content":"eggs","token":"` + todoToken + `"}`)
	attached := time.Now()
	r.eventually(t, func() bool { return strings.Contains(post(`{"op":"get","id":"milk"}`), "list.txt") }, "the upload reaches intake")
	require.Less(t, time.Since(attached), 5*time.Second, "the 1s blob poll, not production's 15s")
}
