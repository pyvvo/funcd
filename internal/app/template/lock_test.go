package template

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// todoApp is ADR-0217's to-do template, which cmd/funcdctl's scenarios read: its schema requires host and gives
// registry the default registry.example.
const todoApp = "../../../cmd/funcdctl/testdata/app-todo/app"

// fakeRegistry is an ImageResolver over fixed tags and digests, keyed by <registry>/<repo> and by its ref.
type fakeRegistry struct {
	tags    map[string][]string
	digests map[string]string
	listed  []string
}

func (f *fakeRegistry) Tags(_ context.Context, repo string) ([]string, error) {
	f.listed = append(f.listed, repo)
	tags, ok := f.tags[repo]
	if !ok {
		return nil, fault.NotFoundf("fake.Tags", "no repository %s", repo)
	}
	return tags, nil
}

func (f *fakeRegistry) Digest(_ context.Context, ref string) (string, error) {
	d, ok := f.digests[ref]
	if !ok {
		return "", fault.NotFoundf("fake.Digest", "no manifest %s", ref)
	}
	return d, nil
}

// ADR-0218 Decision 1: an entry is <repo>:<spec>, the spec a strict version or a range.
func TestParseImage(t *testing.T) {
	t.Parallel()
	for entry, want := range map[string]struct {
		repo, exact string
		accepts     []string
		refuses     []string
	}{
		"todo-api:1.0.0":                 {repo: "todo-api", exact: "1.0.0"},
		"todo-api:1.0.0-rc.1":            {repo: "todo-api", exact: "1.0.0-rc.1"},
		"team/todo-api:2.1.0":            {repo: "team/todo-api", exact: "2.1.0"},
		"todo-stats:^1.0.0":              {repo: "todo-stats", accepts: []string{"1.0.0", "1.9.9"}, refuses: []string{"2.0.0", "0.9.0"}},
		"todo-stats:~1.2.0":              {repo: "todo-stats", accepts: []string{"1.2.9"}, refuses: []string{"1.3.0"}},
		"todo-stats:>=1.0.0 <2.0.0":      {repo: "todo-stats", accepts: []string{"1.5.0"}, refuses: []string{"2.0.0"}},
		"todo-stats:^1.0.0 || ^3.0.0":    {repo: "todo-stats", accepts: []string{"1.1.0", "3.1.0"}, refuses: []string{"2.0.0"}},
		"todo-stats:1.0":                 {repo: "todo-stats", accepts: []string{"1.0.7"}, refuses: []string{"1.1.0"}},
		"todo-stats:>=1.0.0-rc.0 <2.0.0": {repo: "todo-stats", accepts: []string{"1.0.0-rc.1"}, refuses: []string{"2.0.0"}},
	} {
		img, err := ParseImage(entry)
		require.NoError(t, err, entry)
		require.Equal(t, want.repo, img.Repo, entry)
		require.Equal(t, want.exact, img.Exact, entry)
		if want.exact != "" {
			require.Nil(t, img.Range, entry)
			continue
		}
		require.NotNil(t, img.Range, entry)
		for _, v := range want.accepts {
			require.NotEmpty(t, highest([]string{v}, img.Range), "%s accepts %s", entry, v)
		}
		for _, v := range want.refuses {
			require.Empty(t, highest([]string{v}, img.Range), "%s refuses %s", entry, v)
		}
	}

	for _, entry := range []string{
		"todo-api", "todo-api:", ":1.0.0", "Todo-Api:1.0.0", "localhost:5000/todo-api:1.0.0",
		"todo-api:latest", "todo-api:v1.0.0", "todo-api:^v1.0.0", "todo-api:>=1.0.0 <v2.0.0", "todo-api:1.0.0+b1",
		"todo-api:^1.0.0+b1", "todo-api@sha256:" + strings.Repeat("a", 64), "todo-api:1.0.0@sha256:" + strings.Repeat("a", 64),
	} {
		_, err := ParseImage(entry)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%q: %v", entry, err)
		require.ErrorContains(t, err, entry)
	}
}

// ADR-0218 Decision 4: registry is evaluated over the values; with lock only the values it reads are validated.
func TestEvalRegistry(t *testing.T) {
	t.Parallel()
	tpl, err := Load(todoApp)
	require.NoError(t, err)

	reg, err := EvalRegistry(tpl, nil, true)
	require.NoError(t, err, "app lock needs no -f although the schema requires host")
	require.Equal(t, "registry.example", reg, "the schema default applies")
	_, err = EvalRegistry(tpl, nil, false)
	require.ErrorContains(t, err, "host", "render validates every value")

	_, err = EvalRegistry(tpl, values(t, "registry: 5\n"), true)
	require.ErrorContains(t, err, "/registry", "lock validates the value registry reads")
	reg, err = EvalRegistry(tpl, values(t, "minReplicas: nope\nregistry: other.example\n"), true)
	require.NoError(t, err, "lock validates no value registry does not read")
	require.Equal(t, "other.example", reg)

	for _, ok := range []string{"registry.example:5000", "registry.example:5000/team", "oci-layout:///tmp/apps", "oci-layout://a:b/apps"} {
		reg, err := EvalRegistry(tpl, values(t, "host: h\nregistry: "+ok+"\n"), false)
		require.NoError(t, err, ok)
		require.Equal(t, ok, reg)
	}
	for _, bad := range []string{
		"registry.example/team:1", "registry.example/team@sha256:" + strings.Repeat("a", 64), "file:///tmp/apps", "oci://registry.example",
		"oci-layout:///tmp/apps:1", "registry.example/",
	} {
		_, err := EvalRegistry(tpl, values(t, "host: h\nregistry: "+bad+"\n"), false)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s: %v", bad, err)
		require.ErrorContains(t, err, "registry")
		require.ErrorContains(t, err, bad)
	}
	for _, root := range []string{"app.name", "images.api"} {
		other := *tpl
		other.Registry = "${{ " + root + " }}"
		_, err := EvalRegistry(&other, nil, true)
		require.ErrorContains(t, err, "registry reads "+strings.Split(root, ".")[0])
	}
}

// lockTemplate loads a template whose images are api: todo-api:1.0.0 and stats: the given entry.
func lockTemplate(t *testing.T, stats string) *Template {
	t.Helper()
	app := "name: todo\nversion: 1.2.0\nregistry: reg.example\nimages:\n  api: todo-api:1.0.0\n  stats: " + stats + "\n"
	tpl, err := Load(writeTemplate(t, app, nil))
	require.NoError(t, err)
	return tpl
}

// ADR-0218 Decision 2: Lock resolves an exact entry by its tag and a range to the highest strict tag it accepts.
func TestLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := &fakeRegistry{
		tags: map[string][]string{"reg.example/todo-stats": {"1.0.0", "1.0.3", "1.1.0-rc.1", "2.0.0", "latest", "v1.9.0", "1.9"}},
		digests: map[string]string{
			"reg.example/todo-api:1.0.0":        fakeDigest("api-1.0.0"),
			"reg.example/todo-stats:1.0.3":      fakeDigest("stats-1.0.3"),
			"reg.example/todo-stats:1.1.0-rc.1": fakeDigest("stats-1.1.0-rc.1"),
			"reg.example/todo-stats:2.0.0":      fakeDigest("stats-2.0.0"),
		},
	}
	lock, err := Lock(ctx, lockTemplate(t, "todo-stats:^1.0.0"), "reg.example", reg)
	require.NoError(t, err)
	require.Equal(t, map[string]LockedImage{
		"api":   {Requested: "todo-api:1.0.0", Version: "1.0.0", Digest: fakeDigest("api-1.0.0")},
		"stats": {Requested: "todo-stats:^1.0.0", Version: "1.0.3", Digest: fakeDigest("stats-1.0.3")},
	}, lock, "latest, v1.9.0, 1.9 and the pre-release 1.1.0-rc.1 are skipped")
	require.Equal(t, []string{"reg.example/todo-stats"}, reg.listed, "an exact entry is resolved without a tag list")

	lock, err = Lock(ctx, lockTemplate(t, "todo-stats:>=1.1.0-rc.0 <2.0.0"), "reg.example", reg)
	require.NoError(t, err)
	require.Equal(t, "1.1.0-rc.1", lock["stats"].Version, "a pre-release is kept when the range names one")

	lock, err = Lock(ctx, lockTemplate(t, "todo-stats:^3.0.0"), "reg.example", reg)
	require.Nil(t, lock)
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	for _, w := range []string{"images.stats", "^3.0.0", "reg.example/todo-stats"} {
		require.ErrorContains(t, err, w)
	}

	lock, err = Lock(ctx, lockTemplate(t, "todo-stats:^1.0.0"), "other.example", reg)
	require.Nil(t, lock, "nothing is returned on a failure")
	require.ErrorContains(t, err, "images.api")
}

// ADR-0218 Decision 3: a lock is stale when its names or a requested differ from images; a registry change is not.
func TestCheckLock(t *testing.T) {
	t.Parallel()
	fresh := func() map[string]LockedImage {
		return map[string]LockedImage{
			"api":   {Requested: "todo-api:1.0.0", Version: "1.0.0", Digest: fakeDigest("api")},
			"stats": {Requested: "todo-stats:^1.0.0", Version: "1.0.3", Digest: fakeDigest("stats")},
		}
	}
	tpl := lockTemplate(t, "todo-stats:^1.0.0")
	require.ErrorContains(t, CheckLock(tpl), "app.lock", "a missing lock is an error")
	tpl.Lock = fresh()
	require.NoError(t, CheckLock(tpl))

	for name, tc := range map[string]struct {
		edit func(map[string]LockedImage)
		want []string
	}{
		"changed requested": {func(l map[string]LockedImage) { l["stats"] = LockedImage{Requested: "todo-stats:^2.0.0"} },
			[]string{"images.stats", `"todo-stats:^1.0.0"`, `"todo-stats:^2.0.0"`, "funcdctl app lock"}},
		"missing name": {func(l map[string]LockedImage) { delete(l, "api") }, []string{"images.api", "nothing", "funcdctl app lock"}},
		"extra name":   {func(l map[string]LockedImage) { l["web"] = LockedImage{Requested: "todo-web:1.0.0"} }, []string{"images.web", `"todo-web:1.0.0"`}},
	} {
		tpl.Lock = fresh()
		tc.edit(tpl.Lock)
		err := CheckLock(tpl)
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		for _, w := range tc.want {
			require.ErrorContains(t, err, w, name)
		}
	}

	tpl.Lock = fresh()
	for _, registry := range []string{"reg.example", "other.example:5000"} {
		other := *tpl
		other.Registry = registry
		a, err := Render(&other, RenderInput{})
		require.NoError(t, err, "a registry change does not make the lock stale")
		require.Empty(t, a.Spec.Functions)
	}
}

// ADR-0218 Decision 3: app.lock is decoded strictly; an absent lock is nil.
func TestReadLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lock, err := ReadLock(dir)
	require.NoError(t, err)
	require.Nil(t, lock, "no app.lock")

	digest := fakeDigest("api")
	for body, want := range map[string]string{
		"images:\n  api:\n    requested: todo-api:1.0.0\n    version: 1.0.0\n    digest: " + digest + "\n    registry: x\n": "registry",
		"images: {}\npins: {}\n": "pins",
		"images:\n  api:\n    requested: todo-api:1.0.0\n    version: 1.0.0\n    digest: sha256:4f1c\n":                "images.api",
		"images:\n  api:\n    requested: a\n    version: 1.0.0\n    digest: sha512:" + strings.Repeat("a", 128) + "\n": "sha512",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "app.lock"), []byte(body), 0o600))
		_, err := ReadLock(dir)
		require.Equal(t, fault.Invalid, fault.KindOf(err), body)
		require.ErrorContains(t, err, "app.lock", body)
		require.ErrorContains(t, err, want, body)
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.lock"), nil, 0o600))
	lock, err = ReadLock(dir)
	require.NoError(t, err)
	require.Equal(t, map[string]LockedImage{}, lock, "an empty lock locks no image")
}

// ADR-0218 Decision 3: the same resolution writes the same bytes, with sorted keys, and reads back.
func TestWriteLock(t *testing.T) {
	t.Parallel()
	lock := map[string]LockedImage{
		"web":   {Requested: "todo-web:1.0.0", Version: "1.0.0", Digest: fakeDigest("web")},
		"api":   {Requested: "todo-api:1.0.0", Version: "1.0.0", Digest: fakeDigest("api")},
		"stats": {Requested: "todo-stats:^1.0.0", Version: "1.0.3", Digest: fakeDigest("stats")},
	}
	dir := writeTemplate(t, "name: todo\nversion: 1.2.0\nregistry: reg.example\nimages:\n  web: todo-web:1.0.0\n", nil)
	require.NoError(t, WriteLock(dir, lock))
	first, err := os.ReadFile(filepath.Join(dir, "app.lock")) //nolint:gosec // the test's own template
	require.NoError(t, err)
	require.NoError(t, WriteLock(dir, map[string]LockedImage{"stats": lock["stats"], "api": lock["api"], "web": lock["web"]}))
	second, err := os.ReadFile(filepath.Join(dir, "app.lock")) //nolint:gosec // the test's own template
	require.NoError(t, err)
	require.Equal(t, string(first), string(second))
	require.Less(t, strings.Index(string(first), "  api:"), strings.Index(string(first), "  stats:"))
	require.Less(t, strings.Index(string(first), "digest:"), strings.Index(string(first), "requested:"))

	read, err := ReadLock(dir)
	require.NoError(t, err)
	require.Equal(t, lock, read)
	_, err = Load(dir)
	require.NoError(t, err, "no temporary file is left in the template")
}

// ADR-0218 Decision 4: an image renders as <registry>/<repo>:<version>@<digest>.
func TestImageRef(t *testing.T) {
	t.Parallel()
	l := LockedImage{Requested: "todo-api:1.0.0", Version: "1.0.0", Digest: fakeDigest("api")}
	for registry, want := range map[string]string{
		"registry.example":           "registry.example/todo-api:1.0.0@",
		"registry.example:5000/team": "registry.example:5000/team/todo-api:1.0.0@",
		"oci-layout:///tmp/apps":     "oci-layout:///tmp/apps/todo-api:1.0.0@",
	} {
		ref, err := ImageRef(registry, "todo-api", l)
		require.NoError(t, err)
		require.Equal(t, want+l.Digest, ref)
	}
	_, err := ImageRef("registry.example", "todo-api", LockedImage{Version: "1.0.0", Digest: "sha256:4f1c"})
	require.Equal(t, fault.Invalid, fault.KindOf(err))
}

// ADR-0218 Decisions 3 and 4: every rendered image carries the lock's digest; no lock or a stale one is refused
// naming the image, and nothing is returned.
func TestRenderPinsImages(t *testing.T) {
	t.Parallel()
	frag := map[string]string{"resources/a.yaml": apiFragment}
	tpl, err := Load(writeTemplate(t, baseApp, frag))
	require.NoError(t, err)
	_, err = Render(tpl, RenderInput{})
	require.ErrorContains(t, err, "app.lock", "no lock")

	digest := fakeDigest("pinned")
	tpl.Lock = map[string]LockedImage{"api": {Requested: "shop-api:1.0.0", Version: "1.0.0", Digest: digest}}
	a, err := Render(tpl, RenderInput{})
	require.NoError(t, err)
	require.Equal(t, "reg.example/shop-api:1.0.0@"+digest, a.Spec.Functions[0].Image)
	require.Empty(t, a.Spec.Functions[0].ImageDigest, "the digest is in the image, never imageDigest")

	tpl.Lock["api"] = LockedImage{Requested: "shop-api:0.9.0", Version: "0.9.0", Digest: digest}
	a, err = Render(tpl, RenderInput{})
	require.Nil(t, a)
	require.ErrorContains(t, err, "images.api")
	require.ErrorContains(t, err, "stale")
}

// ADR-0218 Decision 5: the moved-tag check warns for each tag at another digest and for each failed check.
func TestMoved(t *testing.T) {
	t.Parallel()
	tpl := lockTemplate(t, "todo-stats:^1.0.0")
	tpl.Lock = map[string]LockedImage{
		"api":   {Requested: "todo-api:1.0.0", Version: "1.0.0", Digest: fakeDigest("api")},
		"stats": {Requested: "todo-stats:^1.0.0", Version: "1.0.3", Digest: fakeDigest("stats")},
	}
	reg := &fakeRegistry{digests: map[string]string{
		"reg.example/todo-api:1.0.0":   fakeDigest("api"),
		"reg.example/todo-stats:1.0.3": fakeDigest("moved"),
	}}
	require.Equal(t, []string{"warning: images.stats: reg.example/todo-stats:1.0.3 now resolves to " + fakeDigest("moved") +
		", not the locked " + fakeDigest("stats") + "; funcdctl app lock would take the new one"},
		Moved(context.Background(), tpl, "reg.example", reg))
	warnings := Moved(context.Background(), tpl, "gone.example", reg)
	require.Len(t, warnings, 2)
	require.Contains(t, warnings[0], "images.api: the moved-tag check of gone.example/todo-api:1.0.0 failed")
}

// ADR-0218 Decision 6: a push needs a fresh lock and a version without build metadata.
func TestCheckPush(t *testing.T) {
	t.Parallel()
	app := "name: todo\nversion: 1.2.0\nregistry: reg.example\nimages:\n  api: todo-api:1.0.0\n"
	lock := "images:\n  api:\n    requested: todo-api:1.0.0\n    version: 1.0.0\n    digest: " + fakeDigest("api") + "\n"
	tpl, err := CheckPush(writeTemplate(t, app, map[string]string{"app.lock": lock}))
	require.NoError(t, err)
	require.Equal(t, "1.2.0", tpl.Version)

	for name, tc := range map[string]struct {
		app, lock string
		want      []string
	}{
		"no lock":        {app, "", []string{"app.lock"}},
		"stale lock":     {strings.Replace(app, "todo-api:1.0.0", "todo-api:1.1.0", 1), lock, []string{"images.api", "stale"}},
		"build metadata": {strings.Replace(app, "1.2.0", "1.2.0+b1", 1), lock, []string{"1.2.0+b1", "version"}},
	} {
		files := map[string]string{}
		if tc.lock != "" {
			files["app.lock"] = tc.lock
		}
		_, err := CheckPush(writeTemplate(t, tc.app, files))
		require.Equal(t, fault.Invalid, fault.KindOf(err), name)
		for _, w := range tc.want {
			require.ErrorContains(t, err, w, name)
		}
	}
}

// ADR-0217 Decision 5's walk refuses a YAML alias, a merge key and a !!binary key in a fragment, as each would hide
// a field from routing; ADR-0218 records these refusals.
func TestRenderRefusesAliasMergeAndBinaryKeys(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		frag string
		want []string
	}{
		"alias value": {fnWith("&h h", "*h", ""), []string{"resources/a.yaml:5: functions[0].image", "alias"}},
		"alias key":   {fnWith("&k image", "${{ images.api }}", "    *k : x\n"), []string{"resources/a.yaml:6: functions[0]", "alias"}},
		"merge key":   {"functions:\n  - <<:\n      runtime: nodejs22\n    name: f\n", []string{"resources/a.yaml:2: functions[0]", "merge key"}},
		"binary key": {strings.Replace(fnWith("h", "registry.example/todo-api:1.0.0", ""), "image:", "!!binary aW1hZ2U=:", 1),
			[]string{"resources/a.yaml:5: functions[0]", "!!binary"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			refused(t, baseApp, map[string]string{"resources/a.yaml": tc.frag}, nil, tc.want...)
		})
	}
}
