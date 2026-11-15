//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/volume"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// appObject is App name of default, whose spec edit sets when it is not nil.
func appObject(name v1.ObjectName, edit func(*v1.AppSpec)) *v1.App {
	a := &v1.App{TypeMeta: v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	if edit != nil {
		edit(&a.Spec)
	}
	return a
}

// appFunction is a Function entry of an App whose artifact is image.
func appFunction(name v1.ObjectName, image string) v1.AppFunction {
	return v1.AppFunction{Name: name, FunctionSpec: v1.FunctionSpec{Runtime: "nodejs22", Handler: "handle", Image: image}}
}

// appHooked is App name, whose one part, Function hook, is its pre-hook.
func appHooked(name, hook v1.ObjectName, image string) *v1.App {
	return appObject(name, func(s *v1.AppSpec) {
		s.Functions = []v1.AppFunction{appFunction(hook, image)}
		s.Hooks = &v1.AppHooks{PreApply: []v1.AppHook{{Function: hook}}}
	})
}

// appRead decodes the object at path, under default's objects, into out, read with the admin's token.
func appRead(c *http.Client, base, path string, out v1.Object) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		base+"/apis/funcd.io/v1alpha1/namespaces/default/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+answersAdmin)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// The answers of the App feature's operations, one row per case of each: App, AppRevision and the retry of a hook.
func TestAppAnswersMatrix(t *testing.T) {
	t.Parallel()
	hd, err := hold.Open(t.TempDir())
	require.NoError(t, err)
	e := newAnswerEnv(t, funcd.WithHold(hd))
	require.NoError(t, hd.HoldNamespace("held", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	image := "file://" + filepath.Join(artifact(t, ""), "handler.mjs")
	e.workers.serve(t, "migrate", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such column", http.StatusInternalServerError)
	})
	stalls, release := make(chan struct{}, 1), make(chan struct{})
	e.workers.serve(t, "stall", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case stalls <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	for _, a := range []*v1.App{
		appObject("plain", nil), appObject("edited", nil),
		appObject("lib", func(s *v1.AppSpec) { s.Version = "1.0.0" }),
		appObject("user", func(s *v1.AppSpec) { s.Requires = []v1.AppRequirement{{App: "lib", Version: "^1.0.0"}} }),
		appObject("keeper", func(s *v1.AppSpec) {
			s.KV = []v1.KVStoreEntry{{Name: "kept", Deletion: v1.DeletionDelete,
				KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t"}}}}}
		}),
		appObject("idle", func(s *v1.AppSpec) { s.Paused = true }),
		appHooked("hooked", "migrate", image),
	} {
		create(t, e.c, e.api, a)
	}
	const objs = "/apis/funcd.io/v1alpha1/namespaces/default/"
	readable := func(path string) func() bool {
		return func() bool { return statusOf(e.c, http.MethodGet, e.api+objs+path, answersAdmin) == http.StatusOK }
	}
	require.Eventually(t, readable("apprevisions/plain-1"), 20*time.Second, 20*time.Millisecond, "App plain stamps plain-1")
	require.Eventually(t, readable("kvstores/kept"), 20*time.Second, 20*time.Millisecond, "App keeper makes its store")
	require.Eventually(t, func() bool {
		var a v1.App
		return appRead(e.c, e.api, "apps/user", &a) == nil && a.Status.CurrentRevision != ""
	}, 20*time.Second, 20*time.Millisecond, "App user, which requires lib, starts")
	require.Eventually(t, func() bool {
		var r v1.AppRevision
		return appRead(e.c, e.api, "apprevisions/hooked-1", &r) == nil && r.Status.Phase == v1.PhaseFailed
	}, 20*time.Second, 20*time.Millisecond, "the pre-hook of App hooked fails its revision")

	root, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	vol := newAnswerEnv(t, funcd.WithFunctionVolumes(volume.Options{Root: root, CapacityBytes: 64 << 20}, true))
	create(t, vol.c, vol.api, appObject("spare", nil))

	const apps = objs + "apps"
	appJSON := func(name v1.ObjectName, edit func(*v1.AppSpec)) string { return jsonOf(t, appObject(name, edit)) }
	leaky := func(s *v1.AppSpec) {
		f := appFunction("leaky-api", "file:///nonexistent/leaky.mjs")
		f.Bindings.Secret = []v1.ObjectName{"ghost"}
		s.Functions = []v1.AppFunction{f}
	}
	linked := func(s *v1.AppSpec) {
		f := appFunction("linked-api", "file:///nonexistent/linked.mjs")
		f.Bindings.Links = []v1.FunctionLink{{Alias: "l", Target: "ghost"}}
		s.Functions = []v1.AppFunction{f}
	}
	hoarding := func(s *v1.AppSpec) {
		f := appFunction("hoard", "file:///nonexistent/hoard.mjs")
		f.Volumes = []v1.VolumeEntry{{Name: "state", Type: v1.VolumeLocal, SizeBytes: 128 << 20}}
		s.Functions = []v1.AppFunction{f}
	}
	requiring := func(app v1.ObjectName) func(*v1.AppSpec) {
		return func(s *v1.AppSpec) { s.Requires = []v1.AppRequirement{{App: app}} }
	}
	plain := kindFixture{kind: "App", kinds: "Apps", path: "apps", name: "plain", json: appJSON("plain", nil),
		broken: appJSON("plain", func(s *v1.AppSpec) { s.KV = []v1.KVStoreEntry{{Name: "t"}, {Name: "t"}} }),
		detail: `spec.kv[1] repeats the name "t" of spec.kv[0]`}

	rows := slices.Concat(
		plain.sharedRows(e),
		kindFixture{kind: "AppRevision", kinds: "AppRevisions", path: "apprevisions", name: "plain-1"}.sharedRows(e),
		[]answerRow{
			{op: "createApp", when: "binds a Secret that spec.secrets does not declare", on: e, ask: ask{
				what: "an undeclared Secret", method: http.MethodPost, path: apps, token: answersDev,
				body:   appJSON("leaky", leaky),
				detail: `names Secret "ghost", which spec.secrets does not declare`}},
			{op: "createApp", when: "would close a cycle among the namespace's Apps", on: e, ask: ask{
				what: "an App that requires itself", method: http.MethodPost, path: apps, token: answersDev,
				body: appJSON("selfish", requiring("selfish")), detail: "would create a requirement cycle"}},
			{op: "createApp", when: "A part breaks a rule", on: e, ask: ask{what: "a link to a missing Function",
				method: http.MethodPost, path: apps, token: answersDev, body: appJSON("linker", linked),
				detail: "spec.functions[0] (Function/linked-api)"}},
			{op: "createApp", when: "A part conflicts with", on: vol, ask: ask{what: "a volume over the capacity",
				method: http.MethodPost, path: apps, token: answersDev, body: appJSON("hoarder", hoarding),
				detail: "spec.functions[0] (Function/hoard)"}},

			{op: "replaceApp", when: "binds a Secret that spec.secrets does not declare", on: e, ask: ask{
				what: "an undeclared Secret", method: http.MethodPut, path: apps + "/edited", token: answersDev,
				body:   appJSON("edited", leaky),
				detail: `names Secret "ghost", which spec.secrets does not declare`}},
			{op: "replaceApp", when: "would close a cycle among the namespace's Apps", on: e, ask: ask{
				what: "an App that requires itself", method: http.MethodPut, path: apps + "/edited", token: answersDev,
				body: appJSON("edited", requiring("edited")), detail: "would create a requirement cycle"}},
			{op: "replaceApp", when: "A part breaks a rule", on: e, ask: ask{what: "a link to a missing Function",
				method: http.MethodPut, path: apps + "/edited", token: answersDev, body: appJSON("edited", linked),
				detail: "spec.functions[0] (Function/linked-api)"}},
			{op: "replaceApp", when: "A part conflicts with", on: vol, ask: ask{what: "a volume over the capacity",
				method: http.MethodPut, path: apps + "/spare", token: answersDev, body: appJSON("spare", hoarding),
				detail: "spec.functions[0] (Function/hoard)"}},
			{op: "replaceApp", when: "A ref entry names an object that the App controls", on: e, ask: ask{
				what: "a ref to a store that the App controls", method: http.MethodPut, path: apps + "/keeper",
				token: answersDev, body: appJSON("keeper", func(s *v1.AppSpec) { s.KV = []v1.KVStoreEntry{{Ref: "kept"}} }),
				detail: "is controlled by App/keeper"}},
			{op: "replaceApp", when: "outside the range", on: e, ask: ask{what: "a version that a started App refuses",
				method: http.MethodPut, path: apps + "/lib", token: answersDev,
				body:   appJSON("lib", func(s *v1.AppSpec) { s.Version = "2.0.0" }),
				detail: `is outside the range its dependents require: user (^1.0.0)`}},

			{op: "deleteApp", when: "Another App's spec.requires names the App", on: e, ask: ask{
				what: "an App that another requires", method: http.MethodDelete, path: apps + "/lib", token: answersDev,
				detail: `app "lib" is required by user`}},

			{op: "retryApp", on: e, ask: ask{what: "a failed pre-hook", method: http.MethodPost,
				path: apps + "/hooked/retry", token: answersDev}},
			{op: "retryApp", when: "carries no bearer token", on: e, ask: ask{what: "no credential",
				method: http.MethodPost, path: apps + "/hooked/retry", detail: "missing bearer token"}},
			{op: "retryApp", when: "do not allow it", on: e, ask: ask{what: "another namespace's developer",
				method: http.MethodPost, path: apps + "/hooked/retry", token: answersOther,
				detail: `update App in "default" denied`}},
			{op: "retryApp", when: "of that name exists in the namespace", on: e, ask: ask{what: "a missing App",
				method: http.MethodPost, path: apps + "/nope/retry", token: answersDev, detail: "not found"}},
			{op: "retryApp", when: "in the path is not", on: e, ask: ask{what: "a name that is no DNS-1123 label",
				method: http.MethodPost, path: apps + "/Not_A_Label/retry", token: answersDev, detail: "path.name"}},
			{op: "retryApp", when: "The query holds a parameter", on: e, ask: ask{what: "an unknown query parameter",
				method: http.MethodPost, path: apps + "/plain/retry?force=true", token: answersDev,
				detail: "unknown query parameter"}},
			{op: "retryApp", when: "has no hook to call again", on: e, ask: ask{what: "an App with no failed hook",
				method: http.MethodPost, path: apps + "/plain/retry", token: answersDev,
				detail: "app plain has no failed hook"}},
			{op: "retryApp", when: "A hook call of the App is running", on: e,
				setup: func(t *testing.T, e *answerEnv) {
					t.Cleanup(func() { close(release) })
					create(t, e.c, e.api, appHooked("busy", "stall", image))
					select {
					case <-stalls:
					case <-time.After(20 * time.Second):
						t.Fatal("the pre-hook call of App busy did not reach its worker")
					}
				},
				ask: ask{what: "a running hook call", method: http.MethodPost, path: apps + "/busy/retry",
					token: answersDev, detail: "a hook call of app busy is running"}},
			{op: "retryApp", when: "The App is paused", on: e, ask: ask{what: "a paused App", method: http.MethodPost,
				path: apps + "/idle/retry", token: answersDev, detail: "app idle is paused"}},
			{op: "retryApp", when: "is held", on: e, ask: ask{what: "a held namespace", method: http.MethodPost,
				path: strings.Replace(apps, "/default/", "/held/", 1) + "/any/retry", token: answersAdmin,
				detail: "funcd is held: app any calls no hook"}},
		},
	)
	runMatrix(t, rows)
}

// siteObject is Site name of default, whose bundle is an image that funcd cannot resolve.
func siteObject(name v1.ObjectName) *v1.Site {
	s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	s.Spec = v1.SiteSpec{Image: "oci-layout:///nonexistent/site:v1", Buckets: []v1.BucketEntry{{Name: "site-files"}},
		Bindings: v1.SiteBindings{Blob: []v1.BlobRef{{Bucket: "site-files", Prefix: "web"}}}}
	return s
}

// The answers of the Site feature's operations, one row per case of each.
func TestSiteAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t)
	for _, s := range []*v1.Site{siteObject("plain"), siteObject("fixed")} {
		create(t, e.c, e.api, s)
	}
	broken, moved := siteObject("plain"), siteObject("fixed")
	broken.Spec.Index = "/index.html"
	moved.Spec.Bindings.Blob[0].Prefix = "elsewhere"
	rows := slices.Concat(
		kindFixture{kind: "Site", kinds: "Sites", path: "sites", name: "plain", json: jsonOf(t, siteObject("plain")),
			broken: jsonOf(t, broken), detail: `spec.index "/index.html" must be a relative path`}.sharedRows(e),
		[]answerRow{{op: "replaceSite", when: "spec.bindings.blob[0].prefix", on: e, ask: ask{what: "another prefix",
			method: http.MethodPut, path: "/apis/funcd.io/v1alpha1/namespaces/default/sites/fixed", token: answersDev,
			body: jsonOf(t, moved), detail: "spec.bindings.blob[0].prefix is immutable"}}},
	)
	runMatrix(t, rows)
}
