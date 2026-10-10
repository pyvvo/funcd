package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	"github.com/pyvvo/funcd/internal/artifact"
)

// todoImages are the images of the to-do fixture, each <repo>:<version>.
func todoImages() map[string]string {
	return map[string]string{
		"api": "todo-api:1.0.0", "migrate": "todo-migrate:1.2.0", "planner": "todo-planner:1.0.0",
		"stats": "todo-stats:1.0.3", "web": "todo-web:1.0.0",
	}
}

// pushImage pushes an artifact whose content is body as oci-layout://<reg>/<repo>:<tag>, one layout per repo, and
// returns its digest. Lock checks no artifact type, so a site artifact stands in for any image.
func pushImage(t *testing.T, reg, repoTag, body string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), body)
	digest, err := artifact.PushSite(context.Background(), "oci-layout://"+reg+"/"+repoTag, dir)
	require.NoError(t, err)
	return digest
}

// layoutTodo copies the to-do fixture, without its app.lock unless locked, adds values/layout.yaml naming a new
// layout registry, pushes the fixture's images there and returns the copy, the registry and each image's digest.
func layoutTodo(t *testing.T, locked bool) (dir, reg string, digests map[string]string) {
	t.Helper()
	dir, reg = t.TempDir(), t.TempDir()
	require.NoError(t, os.CopyFS(dir, os.DirFS(todoFixture)))
	require.NoError(t, os.Remove(filepath.Join(dir, "app", "app.lock")))
	writeFile(t, filepath.Join(dir, "values", "layout.yaml"), "registry: oci-layout://"+reg+"\n")
	digests = map[string]string{}
	for name, repoTag := range todoImages() {
		digests[name] = pushImage(t, reg, repoTag, repoTag)
	}
	if locked {
		var out bytes.Buffer
		require.NoError(t, execCLI(&out, nil, "app", "lock", filepath.Join(dir, "app"), "-f", filepath.Join(dir, "values", "layout.yaml")))
	}
	return dir, reg, digests
}

// renderArgs renders the copy at src with values/prod.yaml and values/layout.yaml.
func renderArgs(dir, src string) []string {
	return []string{"app", "render", src, "--name", "todo", "-n", "team-a",
		"-f", filepath.Join(dir, "values", "prod.yaml"), "-f", filepath.Join(dir, "values", "layout.yaml")}
}

// renderOut runs funcdctl over the real registries and returns its stdout and stderr.
func renderOut(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err = execAppErr(&out, &errOut, nil, artifact.TagResolver{}, args...)
	return out.String(), errOut.String(), err
}

// ADR-0218 Decision 2: app lock resolves every image at the registry value, needs no other value, prints one line
// per image and writes app.lock; a failure writes nothing.
func TestCLIAppLock(t *testing.T) {
	t.Parallel()
	dir, _, digests := layoutTodo(t, false)
	appDir, layout := filepath.Join(dir, "app"), filepath.Join(dir, "values", "layout.yaml")
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "app", "lock", appDir, "-f", layout))
	var want strings.Builder
	for _, name := range []string{"api", "migrate", "planner", "stats", "web"} {
		_, version, _ := strings.Cut(todoImages()[name], ":")
		want.WriteString(name + " " + version + " " + digests[name] + "\n")
	}
	require.Equal(t, want.String(), out.String())

	lock, err := template.ReadLock(appDir)
	require.NoError(t, err)
	require.Len(t, lock, len(todoImages()))
	for name, l := range lock {
		require.Equal(t, template.LockedImage{Requested: todoImages()[name], Version: strings.SplitN(todoImages()[name], ":", 2)[1], Digest: digests[name]}, l)
	}
	first, err := os.ReadFile(filepath.Join(appDir, "app.lock")) //nolint:gosec // the test's own copy
	require.NoError(t, err)
	require.NoError(t, execCLI(&bytes.Buffer{}, nil, "app", "lock", appDir, "-f", layout))
	second, err := os.ReadFile(filepath.Join(appDir, "app.lock")) //nolint:gosec // the test's own copy
	require.NoError(t, err)
	require.Equal(t, string(first), string(second), "the same resolution writes the same bytes")

	editFile(t, filepath.Join(appDir, "app.yaml"), "web: todo-web:1.0.0", "web: todo-web:9.9.9")
	out.Reset()
	err = execCLI(&out, nil, "app", "lock", appDir, "-f", layout)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "images.web")
	require.Empty(t, out.String())
	unchanged, err := os.ReadFile(filepath.Join(appDir, "app.lock")) //nolint:gosec // the test's own copy
	require.NoError(t, err)
	require.Equal(t, string(first), string(unchanged), "a failure writes nothing")
}

// ADR-0218 Decision 4: a directory without app.lock renders what a locked copy renders, with a lock resolved now; the
// picks and the missing-lock note go to stderr, and no app.lock is written.
func TestCLIAppRenderWithoutLock(t *testing.T) {
	t.Parallel()
	dir, reg, digests := layoutTodo(t, false)
	locked := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.CopyFS(locked, os.DirFS(filepath.Join(dir, "app"))))
	require.NoError(t, execCLI(&bytes.Buffer{}, nil, "app", "lock", locked, "-f", filepath.Join(dir, "values", "layout.yaml")))

	want, wantErr, err := renderOut(t, renderArgs(dir, locked)...)
	require.NoError(t, err)
	require.Empty(t, wantErr, "a locked directory whose tags did not move warns of nothing")
	got, gotErr, err := renderOut(t, renderArgs(dir, filepath.Join(dir, "app"))...)
	require.NoError(t, err)
	require.Equal(t, want, got)
	require.Contains(t, got, "oci-layout://"+reg+"/todo-api:1.0.0@"+digests["api"])
	require.Contains(t, gotErr, "has no app.lock")
	for name := range todoImages() {
		_, version, _ := strings.Cut(todoImages()[name], ":")
		require.Contains(t, gotErr, "resolved "+name+" "+version+" "+digests[name]+"\n")
	}
	require.NoFileExists(t, filepath.Join(dir, "app", "app.lock"))
}

// ADR-0218 Decisions 5 and 7: a directory render warns on stderr of a tag that moved and still renders the locked
// digest; a render of the pushed template makes no check and prints the template it pulled.
func TestCLIAppRenderMovedTag(t *testing.T) {
	t.Parallel()
	dir, reg, digests := layoutTodo(t, true)
	appDir := filepath.Join(dir, "app")
	before, _, err := renderOut(t, renderArgs(dir, appDir)...)
	require.NoError(t, err)

	var pushed bytes.Buffer
	ref := "oci-layout://" + reg + "/todo-app:1.2.0"
	require.NoError(t, execCLI(&pushed, nil, "push", "--template", appDir, ref))
	moved := pushImage(t, reg, "todo-api:1.0.0", "moved")
	require.NotEqual(t, digests["api"], moved)

	out, errOut, err := renderOut(t, renderArgs(dir, appDir)...)
	require.NoError(t, err)
	require.Equal(t, before, out, "the warning changes nothing in the App")
	require.Contains(t, out, "todo-api:1.0.0@"+digests["api"])
	require.Equal(t, 1, strings.Count(errOut, "warning:"), errOut)
	for _, w := range []string{"images.api", digests["api"], moved, "funcdctl app lock"} {
		require.Contains(t, errOut, w)
	}

	out, errOut, err = renderOut(t, renderArgs(dir, ref)...)
	require.NoError(t, err)
	require.Equal(t, before, out, "the pushed template renders the same App")
	require.Equal(t, "template "+strings.TrimSpace(pushed.String())+"\n", errOut, "a ref source makes no moved-tag check")
}

// ADR-0218 Decision 7: deploy takes a pushed template ref; a ref to another artifact type, or to a template without
// app.lock, is refused and nothing is printed or applied.
func TestCLIAppTemplateRefSource(t *testing.T) {
	t.Parallel()
	dir, reg, digests := layoutTodo(t, true)
	ref := "oci-layout://" + reg + "/todo-app:1.2.0"
	require.NoError(t, execCLI(&bytes.Buffer{}, nil, "push", "--template", filepath.Join(dir, "app"), ref))
	c, _, _ := newTestServer(t)
	deploy := []string{"app", "deploy", ref, "--name", "todo", "-n", "team-a", "--no-wait",
		"-f", filepath.Join(dir, "values", "prod.yaml"), "-f", filepath.Join(dir, "values", "layout.yaml")}
	require.NoError(t, execAppErr(&bytes.Buffer{}, &bytes.Buffer{}, c, artifact.TagResolver{}, deploy...))
	app := waitApplied(t, c, "1.2.0")
	require.True(t, slices.ContainsFunc(app.Spec.Functions, func(f v1.AppFunction) bool {
		return f.Name == "todo-api" && f.Image == "oci-layout://"+reg+"/todo-api:1.0.0@"+digests["api"]
	}), "the deployed App pins the pushed lock's digest")

	unlocked := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.CopyFS(unlocked, os.DirFS(filepath.Join(dir, "app"))))
	require.NoError(t, os.Remove(filepath.Join(unlocked, "app.lock")))
	bare := "oci-layout://" + reg + "/bare-app:1.2.0"
	_, err := artifact.PushTemplate(context.Background(), bare, unlocked, "1.2.0")
	require.NoError(t, err)
	site := "oci-layout://" + reg + "/todo-web:1.0.0"
	for ref, want := range map[string]string{bare: "app.lock", site: artifact.AppTemplateArtifactType} {
		out, _, err := renderOut(t, renderArgs(dir, ref)...)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", ref, err)
		require.ErrorContains(t, err, want)
		require.Empty(t, out)
	}
}

// ADR-0218 Decision 6: push --template is exclusive with the function and site flags, needs a fresh lock and a tag
// that is the version, pushes once per version, and writes nothing when it refuses.
func TestCLIPushTemplate(t *testing.T) {
	t.Parallel()
	dir, reg, _ := layoutTodo(t, true)
	appDir := filepath.Join(dir, "app")
	ref := "oci-layout://" + reg + "/todo-app:1.2.0"
	for _, flag := range [][]string{{"--site"}, {"--schema", "s.json"}, {"--runtime", "nodejs22"}, {"--platform", "linux/amd64"}, {"--entry", "x"}} {
		err := execCLI(&bytes.Buffer{}, nil, append(append([]string{"push", "--template"}, flag...), appDir, ref)...)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", flag)
		require.ErrorContains(t, err, "--template is mutually exclusive")
	}
	require.NoDirExists(t, filepath.Join(reg, "todo-app"))

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "push", "--template", appDir, ref))
	require.True(t, strings.HasPrefix(out.String(), ref+"@sha256:"), out.String())
	pushed, err := artifact.ResolveTemplate(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, ref+"@"+pushed+"\n", out.String())
	out.Reset()
	require.NoError(t, execCLI(&out, nil, "push", "--template", appDir, ref))
	require.Equal(t, ref+"@"+pushed+"\n", out.String(), "the same template pushes again with no change")

	unlocked := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.CopyFS(unlocked, os.DirFS(appDir)))
	require.NoError(t, os.Remove(filepath.Join(unlocked, "app.lock")))
	stale := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.CopyFS(stale, os.DirFS(appDir)))
	editFile(t, filepath.Join(stale, "app.yaml"), "api: todo-api:1.0.0", "api: todo-api:1.0.1")
	changed := filepath.Join(t.TempDir(), "app")
	require.NoError(t, os.CopyFS(changed, os.DirFS(appDir)))
	writeFile(t, filepath.Join(changed, "resources", "extra.yaml"), "kv:\n  - name: extra\n")
	other := "oci-layout://" + reg + "/other-app:1.2.0"
	for name, tc := range map[string]struct {
		dir, ref string
		kind     fault.Kind
		want     []string
	}{
		"other tag":    {appDir, "oci-layout://" + reg + "/todo-app:1.3.0", fault.Invalid, []string{"1.3.0", "1.2.0"}},
		"no lock":      {unlocked, other, fault.Invalid, []string{"app.lock"}},
		"stale lock":   {stale, other, fault.Invalid, []string{"images.api", "stale"}},
		"other digest": {changed, ref, fault.Conflict, []string{"1.2.0", pushed}},
	} {
		out.Reset()
		err := execCLI(&out, nil, "push", "--template", tc.dir, tc.ref)
		require.Equal(t, tc.kind, fault.KindOf(err), "%s: %v", name, err)
		for _, w := range tc.want {
			require.ErrorContains(t, err, w, name)
		}
		require.Empty(t, out.String(), name)
	}
	require.NoDirExists(t, filepath.Join(reg, "other-app"), "a refused push writes nothing")
	tags, err := artifact.ListTags(context.Background(), "oci-layout://"+reg+"/todo-app")
	require.NoError(t, err)
	require.Equal(t, []string{"1.2.0"}, tags)
	at, err := artifact.ResolveTemplate(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, pushed, at, "the conflict left the version tag where it was")
}

// scenario: app-template-range
func TestScenarioAppTemplateRange(t *testing.T) {
	t.Parallel()
	dir, reg, _ := layoutTodo(t, false)
	appDir, layout := filepath.Join(dir, "app"), filepath.Join(dir, "values", "layout.yaml")
	stats := map[string]string{}
	for _, v := range []string{"1.0.0", "1.0.3", "2.0.0"} {
		stats[v] = pushImage(t, reg, "todo-stats:"+v, "stats "+v)
	}
	editFile(t, filepath.Join(appDir, "app.yaml"), "stats: todo-stats:1.0.3", "stats: todo-stats:^1.0.0")

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, nil, "app", "lock", appDir, "-f", layout))
	require.Contains(t, out.String(), "stats 1.0.3 "+stats["1.0.3"]+"\n")
	lock, err := template.ReadLock(appDir)
	require.NoError(t, err)
	require.Equal(t, template.LockedImage{Requested: "todo-stats:^1.0.0", Version: "1.0.3", Digest: stats["1.0.3"]}, lock["stats"])
	locked, err := os.ReadFile(filepath.Join(appDir, "app.lock")) //nolint:gosec // the test's own copy
	require.NoError(t, err)

	editFile(t, filepath.Join(appDir, "app.yaml"), "stats: todo-stats:^1.0.0", "stats: todo-stats:^3.0.0")
	err = execCLI(&bytes.Buffer{}, nil, "app", "lock", appDir, "-f", layout)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "images.stats")
	require.ErrorContains(t, err, "^3.0.0")
	unchanged, err := os.ReadFile(filepath.Join(appDir, "app.lock")) //nolint:gosec // the test's own copy
	require.NoError(t, err)
	require.Equal(t, string(locked), string(unchanged), "a failed lock leaves app.lock unchanged")

	editFile(t, filepath.Join(appDir, "app.yaml"), "stats: todo-stats:^3.0.0", "stats: todo-stats:^2.0.0")
	stdout, _, err := renderOut(t, renderArgs(dir, appDir)...)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "images.stats")
	require.ErrorContains(t, err, "stale")
	require.Empty(t, stdout, "a stale lock prints nothing")

	require.NoError(t, execCLI(&bytes.Buffer{}, nil, "app", "lock", appDir, "-f", layout))
	stdout, _, err = renderOut(t, renderArgs(dir, appDir)...)
	require.NoError(t, err)
	require.Contains(t, stdout, "image: oci-layout://"+reg+"/todo-stats:2.0.0@"+stats["2.0.0"]+"\n")
}
