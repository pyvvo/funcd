package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// ADR-0337: an App template carries its add-on.

const addonAdminToken = "addon-admin-secret"

// rauthyTemplateAddon is the template's addon.yaml: rauthy's Addon, of the kind IdentityProvider.
const rauthyTemplateAddon = `apiVersion: funcd.io/v1alpha1
kind: Addon
metadata:
  name: rauthy
spec:
  runtimes:
    - name: identityprovider
      image: ghcr.io/pyvvo/funcd-rauthy@sha256:7686fef5f0a7e5f5f5c1d7c7cf0f3a9f3d1c2b4a5e6f708192a3b4c5d6e7f809
      workloads:
        - Engine
      attributes:
        kind: IdentityProvider
        port: 8080
        basePath: /auth/v1
        readiness:
          path: /auth/v1/ping
        config:
          type: object
          additionalProperties: false
          properties:
            issuer:
              type: string
          required:
            - issuer
        env:
          PUB_URL: ${{ config.issuer }}
`

// addonCLIEnv is a control plane with the Addon admissions, and an admin's and a developer's client.
type addonCLIEnv struct {
	admin, dev *sdk.Client
	st         store.Store
	url        string
	stamped    bool // a stand-in for the Addon reconciler stamps each Addon's revision
}

func newAddonCLIEnv(t *testing.T) *addonCLIEnv {
	t.Helper()
	return newAddonCLIEnvWith(t, true)
}

// newAddonCLIEnvWith is newAddonCLIEnv whose Addons get their revision named current only with stamp.
func newAddonCLIEnvWith(t *testing.T, stamp bool) *addonCLIEnv {
	t.Helper()
	st := store.New(memory.New())
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: rbac.New(),
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken:        {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			addonAdminToken: {Subject: "ops", Role: auth.RoleAdmin},
		}),
		Admissions: controlplane.NewAddonAdmissions(controlplane.AddonRules{Store: storeList{st}, AnyRegistry: true,
			Reserved: controlplane.ReservedPlurals()}),
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client := func(token string) *sdk.Client {
		c, err := sdk.New(srv.URL, sdk.WithToken(token))
		require.NoError(t, err)
		return c
	}
	e := &addonCLIEnv{admin: client(addonAdminToken), dev: client(devToken), st: st, url: srv.URL}
	if stamp {
		e.stampRevisions(t, 0)
	}
	return e
}

// stampRevisions stands in for the Addon reconciler: delay after each write, it names the Addon's generation its
// current revision.
func (e *addonCLIEnv) stampRevisions(t *testing.T, delay time.Duration) {
	t.Helper()
	e.stamped = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w, err := e.st.Watch(ctx, v1.KindAddon.GVK(), store.WatchOptions{})
	require.NoError(t, err)
	go func() {
		defer w.Stop()
		for ev := range w.ResultChan() {
			a, ok := ev.Object.(*v1.Addon)
			if !ok || ev.Type == store.Deleted || a.Status.CurrentRevision == v1.AddonRevisionName(a.Name, a.Generation) {
				continue
			}
			time.Sleep(delay)
			a.Status.CurrentRevision = v1.AddonRevisionName(a.Name, a.Generation)
			_, _ = e.st.Update(ctx, a)
		}
	}()
}

// addonTemplate writes the rauthy template, whose App holds the IdentityProvider <app>-login, with addon as its
// addon.yaml.
func addonTemplate(t *testing.T, addon string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.yaml"), "name: rauthy\nversion: 1.0.0\n")
	writeFile(t, filepath.Join(dir, "resources", "login.yaml"), "addons:\n  - name: ${{ app.name + \"-login\" }}\n"+
		"    kind: IdentityProvider\n    config:\n      issuer: https://login.example.com/auth/v1\n")
	writeFile(t, filepath.Join(dir, template.AddonFile), addon)
	require.NoError(t, template.WriteLock(dir, map[string]template.LockedImage{}))
	return dir
}

// deployAs runs funcdctl app deploy with c and returns what it printed and its error.
func deployAs(c *sdk.Client, args ...string) (string, error) {
	var out bytes.Buffer
	err := execAppErr(&out, io.Discard, c, hashImages{}, append([]string{"app", "deploy"}, args...)...)
	return out.String(), err
}

func (e *addonCLIEnv) addon(t *testing.T, name v1.ObjectName) (*v1.Addon, bool) {
	t.Helper()
	obj, err := e.st.Get(context.Background(), v1.KindAddon.GVK(), "", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(t, err)
	return obj.(*v1.Addon), true
}

func (e *addonCLIEnv) app(t *testing.T, name v1.ObjectName) (*v1.App, bool) {
	t.Helper()
	obj, err := e.st.Get(context.Background(), v1.KindApp.GVK(), "team-a", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(t, err)
	return obj.(*v1.App), true
}

// register stores the template's Addon under name, as an admin would have, and returns once its revision is current
// when the env stamps revisions: a stamp still due could meet the next deploy's update (#1531).
func (e *addonCLIEnv) register(t *testing.T, name v1.ObjectName, body string) *v1.Addon {
	t.Helper()
	obj, err := sdk.DecodeManifest([]byte(body))
	require.NoError(t, err)
	a := obj.(*v1.Addon)
	a.Name = name
	out, err := e.admin.Apply(context.Background(), a)
	require.NoError(t, err)
	written := out.(*v1.Addon)
	if e.stamped {
		require.Eventually(t, func() bool {
			cur, err := e.st.Get(context.Background(), v1.KindAddon.GVK(), "", name)
			return err == nil && cur.(*v1.Addon).Status.CurrentRevision == v1.AddonRevisionName(name, written.Generation)
		}, cliWithin, 10*time.Millisecond, "Addon %s's revision is current", name)
	}
	return written
}

// scenario: addon-template-needs-kind — a template whose addon.yaml adds no kind is refused: deploy exits 1 naming the
// file, and applies neither the Addon nor the App (ADR-0344 Decision 10).
func TestScenarioAddonTemplateNeedsKind(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnv(t)
	runtimesOnly := strings.Replace(rauthyTemplateAddon, "workloads:\n        - Engine", "workloads:\n        - Function", 1)
	runtimesOnly = runtimesOnly[:strings.Index(runtimesOnly, "      attributes:")]
	_, err := deployAs(e.admin, addonTemplate(t, runtimesOnly), "-n", "team-a", "--no-wait")
	require.Error(t, err)
	require.Equal(t, 1, exitCode(err))
	require.ErrorContains(t, err, template.AddonFile)
	require.ErrorContains(t, err, "adds no kind")
	_, ok := e.addon(t, "rauthy")
	require.False(t, ok, "no Addon is applied")
	_, ok = e.app(t, "rauthy")
	require.False(t, ok, "no App is applied")
}

// scenario: addon-template-admin-install — with no Addon of the kind, an admin's deploy creates the template's Addon,
// then applies the App, whose part is the IdentityProvider rauthy-login.
func TestScenarioAddonTemplateAdminInstall(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnv(t)
	out, err := deployAs(e.admin, addonTemplate(t, rauthyTemplateAddon), "-n", "team-a", "--no-wait")
	require.NoError(t, err)
	require.Equal(t, "created Addon/rauthy\n", out)
	a, ok := e.addon(t, "rauthy")
	require.True(t, ok)
	require.Equal(t, "IdentityProvider", a.Spec.Kind())
	app, ok := e.app(t, "rauthy")
	require.True(t, ok, "the App is applied after the Addon")
	require.Equal(t, v1.ObjectName("rauthy-login"), app.Spec.Addons[0].Name)
}

// ADR-0337 Decision 2: after it writes the Addon, deploy applies the App only once the Addon names the revision of
// the generation it wrote current, so the App's revision records it; --no-wait waits too.
func TestAddonDeployWaitsForTheRevision(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnvWith(t, false)
	run := startCLI(t, e.admin, "app", "deploy", addonTemplate(t, rauthyTemplateAddon), "-n", "team-a", "--no-wait")
	run.waitOut(t, "created Addon/rauthy\n")
	require.True(t, poll.Never(func() bool {
		_, ok := e.app(t, "rauthy")
		return ok
	}, 300*time.Millisecond, 20*time.Millisecond), "no App before the Addon's revision is current")
	a, _ := e.addon(t, "rauthy")
	a.Status.CurrentRevision = v1.AddonRevisionName(a.Name, a.Generation)
	_, err := e.st.Update(context.Background(), a)
	require.NoError(t, err)
	require.NoError(t, run.wait(t))
	_, ok := e.app(t, "rauthy")
	require.True(t, ok, "the App follows the revision")
}

// scenario: addon-template-admin-update — an Addon of the template's kind under another name: unchanged when the specs
// agree, else updated under its own name; deploy never creates a second Addon of the kind.
func TestScenarioAddonTemplateAdminUpdate(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnv(t)
	before := e.register(t, "login-provider", rauthyTemplateAddon)
	out, err := deployAs(e.admin, addonTemplate(t, rauthyTemplateAddon), "-n", "team-a", "--no-wait")
	require.NoError(t, err)
	require.Equal(t, "unchanged Addon/login-provider\n", out)
	same, _ := e.addon(t, "login-provider")
	require.Equal(t, before.Generation, same.Generation, "an unchanged Addon is not written")

	newer := strings.Replace(rauthyTemplateAddon, "path: /auth/v1/ping", "path: /auth/v1/health", 1)
	out, err = deployAs(e.admin, addonTemplate(t, newer), "-n", "team-a", "--no-wait")
	require.NoError(t, err)
	require.Equal(t, "updated Addon/login-provider\n", out)
	updated, _ := e.addon(t, "login-provider")
	require.Equal(t, "/auth/v1/health", updated.Spec.Runtimes[0].Attributes.Readiness.Path)
	_, made := e.addon(t, "rauthy")
	require.False(t, made, "deploy finds the Addon by its kind, under any name")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The stand-in reconciler stamps a registered Addon a moment after the write, as the real one does. A stamp still due
// once register returns can land between a deploy's list of the Addons and its update, which then answers 409: the
// transport writes it there whenever it is still due.
func TestIssue1531_DeployAfterRegisterMeetsNoStamp(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnvWith(t, false)
	e.stampRevisions(t, 200*time.Millisecond)
	e.register(t, "login-provider", rauthyTemplateAddon)
	late := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if a, ok := e.addon(t, "login-provider"); ok && r.Method == http.MethodPut && a.Status.CurrentRevision == "" {
			a.Status.CurrentRevision = v1.AddonRevisionName(a.Name, a.Generation)
			_, err := e.st.Update(context.Background(), a)
			require.NoError(t, err)
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	admin, err := sdk.New(e.url, sdk.WithToken(addonAdminToken), sdk.WithHTTPClient(&http.Client{Transport: late}))
	require.NoError(t, err)
	newer := strings.Replace(rauthyTemplateAddon, "path: /auth/v1/ping", "path: /auth/v1/health", 1)
	out, err := deployAs(admin, addonTemplate(t, newer), "-n", "team-a", "--no-wait")
	require.NoError(t, err)
	require.Equal(t, "updated Addon/login-provider\n", out)
}

// scenario: addon-template-developer-deploy — with the kind served, a developer's deploy applies the App alone.
func TestScenarioAddonTemplateDeveloperDeploy(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnv(t)
	before := e.register(t, "rauthy", rauthyTemplateAddon)
	out, err := deployAs(e.dev, addonTemplate(t, strings.Replace(rauthyTemplateAddon, "8080", "9090", 1)), "-n", "team-a", "--no-wait")
	require.NoError(t, err)
	require.Empty(t, out, "a developer writes no Addon")
	after, _ := e.addon(t, "rauthy")
	require.Equal(t, before.Generation, after.Generation, "the Addon is unchanged")
	require.Equal(t, before.Spec.Runtimes[0].Attributes.Port, after.Spec.Runtimes[0].Attributes.Port)
	_, ok := e.app(t, "rauthy")
	require.True(t, ok)
}

// scenario: addon-template-developer-unregistered — with no Addon serving identityproviders, a developer's deploy exits 1
// before it applies anything, naming the Addon and the admin's step, a dry run too.
func TestScenarioAddonTemplateDeveloperUnregistered(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnv(t)
	dir := addonTemplate(t, rauthyTemplateAddon)
	for _, extra := range [][]string{{"--no-wait"}, {"--dry-run"}} {
		_, err := deployAs(e.dev, append([]string{dir, "-n", "team-a"}, extra...)...)
		require.ErrorContains(t, err, "Addon rauthy is not registered: an admin deploys this template first", extra[0])
		_, ok := e.app(t, "rauthy")
		require.False(t, ok, "%s: nothing is applied", extra[0])
	}
}

// scenario: addon-template-dry-run — an admin's dry run with no Addon of the kind admits the Addon and stores nothing,
// says why the App's own dry run waits, and exits 0.
func TestScenarioAddonTemplateDryRun(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnv(t)
	out, err := deployAs(e.admin, addonTemplate(t, rauthyTemplateAddon), "-n", "team-a", "--dry-run")
	require.NoError(t, err)
	require.Equal(t, "would apply Addon/rauthy\nthe App's dry run needs Addon/rauthy registered\n", out)
	_, ok := e.addon(t, "rauthy")
	require.False(t, ok, "a dry run stores no Addon")
	_, ok = e.app(t, "rauthy")
	require.False(t, ok)

	_, err = deployAs(e.admin, addonTemplate(t, strings.Replace(rauthyTemplateAddon, "port: 8080", "port: 0", 1)),
		"-n", "team-a", "--dry-run")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a refused dry run exits 1: %v", err)
}

// scenario: addon-yaml-as-written — addon.yaml's ${{ values.publicHost }} reaches the server as written, and the
// Addon's admission refuses it, so no App is applied.
func TestScenarioAddonYAMLAsWritten(t *testing.T) {
	t.Parallel()
	e := newAddonCLIEnv(t)
	body := strings.Replace(rauthyTemplateAddon, "${{ config.issuer }}", "${{ values.publicHost }}", 1)
	_, err := deployAs(e.admin, addonTemplate(t, body), "-n", "team-a", "--no-wait")
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "attributes.env")
	_, ok := e.addon(t, "rauthy")
	require.False(t, ok)
	_, ok = e.app(t, "rauthy")
	require.False(t, ok, "a refused Addon applies no App")
}

// seedLoginPart stores web-auth's part web-auth-login, as the App reconciler writes it, and the Function that funcd
// creates for it from its kind's image (ADR-0358 Decision 2).
func seedLoginPart(t *testing.T, st store.Store, app *v1.App) {
	t.Helper()
	o := &v1.AddonObject{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddonObject.GVK().APIVersion(), Kind: v1.KindAddonObject}}
	o.Name, o.Namespace, o.ResourceGroup = "web-auth-login", "team-a", app.ResourceGroup
	o.Spec.Kind, o.Spec.Config = "IdentityProvider", json.RawMessage(`{"issuer":"https://login.example.com/auth/v1"}`)
	o.OwnerReferences = []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: "team-a", Name: app.Name},
		UID: app.UID, Controller: true, BlockOwnerDeletion: true}}
	part, err := st.Create(context.Background(), o)
	require.NoError(t, err)
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = "web-auth-login-rauthypasswordpolicy", "team-a", app.ResourceGroup
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "ghcr.io/pyvvo/rauthy-settings:1"
	fn.OwnerReferences = ownedstore.Refs(part, v1.DeletionDelete)
	_, err = st.Create(context.Background(), fn)
	require.NoError(t, err)
}

// scenario: addon-app-delete — app delete walks the App's add-on part at its kind's plural and prints it once it is
// gone, and the Function that the part controls; with the Addon removed first, the kind's 404 reads as no object and
// delete ends with exit 0.
func TestScenarioAddonAppDelete(t *testing.T) {
	t.Parallel()
	for _, addonFirst := range []bool{false, true} {
		e := newAddonCLIEnv(t)
		e.register(t, "rauthy", rauthyTemplateAddon)
		_, err := deployAs(e.dev, addonTemplate(t, rauthyTemplateAddon), "-n", "team-a", "--name", "web-auth", "--no-wait")
		require.NoError(t, err)
		app, _ := e.app(t, "web-auth")
		seedLoginPart(t, e.st, app)
		setStatus := func(s *v1.AppStatus) {
			s.Children = []v1.AppChild{{Kind: "IdentityProvider", Name: "web-auth-login", State: v1.AppChildReady}}
		}
		obj, err := e.st.Get(context.Background(), v1.KindApp.GVK(), "team-a", "web-auth")
		require.NoError(t, err)
		stored := obj.(*v1.App)
		setStatus(&stored.Status)
		_, err = e.st.Update(context.Background(), stored)
		require.NoError(t, err)
		if addonFirst {
			require.NoError(t, e.admin.Delete(context.Background(), v1.KindAddon, "", "rauthy"))
		}

		run := startCLI(t, e.dev, "app", "delete", "web-auth", "-n", "team-a")
		require.Eventually(t, func() bool {
			_, ok := e.app(t, "web-auth")
			return !ok
		}, cliWithin, 10*time.Millisecond, "the App is deleted")
		storeDelete(t, e.st, v1.KindAddonObject, "web-auth-login")
		if !addonFirst {
			require.Eventually(t, func() bool { return strings.Contains(run.out.String(), "deleted IdentityProvider/web-auth-login\n") },
				cliWithin, 10*time.Millisecond)
			select {
			case <-run.done:
				require.Fail(t, "app delete ended before the part's Function went")
			default:
			}
		}
		storeDelete(t, e.st, v1.KindFunction, "web-auth-login-rauthypasswordpolicy")
		require.NoError(t, run.wait(t), "addon first: %v", addonFirst)
		require.Contains(t, run.out.String(), "deleted IdentityProvider/web-auth-login\n", "addon first: %v", addonFirst)
		if !addonFirst {
			require.Contains(t, run.out.String(), "deleted Function/web-auth-login-rauthypasswordpolicy\n")
		}
	}
}

// ADR-0337 Decision 1: a template with addon.yaml renders the App alone.
func TestCLIAppRenderLeavesTheAddonOut(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	require.NoError(t, execAppErr(&out, io.Discard, nil, hashImages{}, "app", "render", addonTemplate(t, rauthyTemplateAddon), "-o", "json"))
	var app v1.App
	require.NoError(t, json.Unmarshal(out.Bytes(), &app), out.String())
	require.Equal(t, v1.KindApp, app.Kind)
	require.NotContains(t, out.String(), "funcd-rauthy", "the Addon is not printed")
}
