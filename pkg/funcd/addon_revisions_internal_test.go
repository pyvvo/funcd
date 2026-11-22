package funcd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/poll"
)

// ADR-0339: an Addon's versions are revisions, and each object pins the one its engine runs.

const addonRevisionsPath = "/apis/funcd.io/v1alpha1/addonrevisions"

// newerImage is rauthy's image at another digest.
const newerImage = "ghcr.io/pyvvo/funcd-rauthy@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// waitCurrent waits until the Addon rauthy names revision current, and returns it.
func (e *addonEnv) waitCurrent(t *testing.T, revision v1.ObjectName) *v1.Addon {
	t.Helper()
	var a *v1.Addon
	require.Eventually(t, func() bool {
		obj, err := e.p.cfg.store.Get(e.ctx, v1.KindAddon.GVK(), "", "rauthy")
		if err != nil {
			return false
		}
		a = obj.(*v1.Addon)
		return a.Status.CurrentRevision == revision
	}, 5*time.Second, 10*time.Millisecond, "rauthy's current revision is %s", revision)
	return a
}

func (e *addonEnv) revision(t *testing.T, name v1.ObjectName) (*v1.AddonRevision, bool) {
	t.Helper()
	obj, err := e.p.cfg.store.Get(e.ctx, v1.KindAddonRevision.GVK(), "", name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, false
	}
	require.NoError(t, err)
	return obj.(*v1.AddonRevision), true
}

// updateImage applies rauthy with image.
func (e *addonEnv) updateImage(t *testing.T, image string) {
	t.Helper()
	a := rauthyAddon("rauthy")
	a.Spec.Image = image
	e.apply(t, e.admin, a)
}

// pin waits until ns/name pins revision and its engine runs image.
func (e *addonEnv) pin(t *testing.T, ns v1.NamespaceName, name, revision v1.ObjectName, image string) {
	t.Helper()
	require.Eventually(t, func() bool {
		o, ok := e.object(t, ns, name)
		spec, running, _ := e.rt.state(ref(ns, name))
		return ok && o.Status.AddonRevision == revision && running && spec.Image == image
	}, 5*time.Second, 10*time.Millisecond, "%s/%s pins %s and runs %s", ns, name, revision, image)
}

// scenario: addon-revision-stamped — a change of rauthy's image stamps rauthy-2 with the new spec and makes it current,
// and rauthy-1, which login pins, keeps the old one.
func TestScenarioAddonRevisionStamped(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.waitCurrent(t, "rauthy-1")
	e.apply(t, e.dev, identityProvider("default"))
	e.pin(t, "default", "login", "rauthy-1", rauthyAddon("rauthy").Spec.Image)
	e.updateImage(t, newerImage)
	e.waitCurrent(t, "rauthy-2")
	second, ok := e.revision(t, "rauthy-2")
	require.True(t, ok)
	require.Equal(t, newerImage, second.Spec.Spec.Image)
	require.Equal(t, int64(2), second.Spec.Number)
	first, ok := e.revision(t, "rauthy-1")
	require.True(t, ok)
	require.Equal(t, rauthyAddon("rauthy").Spec.Image, first.Spec.Spec.Image, "rauthy-1 is unchanged")
}

// scenario: addon-revision-no-move — login pinned to rauthy-1 keeps rauthy-1's image when the Addon moves to rauthy-2,
// and its engine is not replaced.
//
// scenario: addon-revision-edit-moves — then an edit of login's spec pins rauthy-2, whose image its engine runs.
func TestScenarioAddonRevisionNoMove(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.apply(t, e.dev, identityProvider("default"))
	e.waitReason(t, "default", "login", v1.PhaseReady, "")
	e.pin(t, "default", "login", "rauthy-1", rauthyAddon("rauthy").Spec.Image)
	_, _, starts := e.rt.state(ref("default", "login"))

	e.updateImage(t, newerImage)
	e.waitCurrent(t, "rauthy-2")
	require.True(t, poll.Never(func() bool {
		o, _ := e.object(t, "default", "login")
		spec, _, n := e.rt.state(ref("default", "login"))
		return n != starts || spec.Image != rauthyAddon("rauthy").Spec.Image || o.Status.AddonRevision != "rauthy-1"
	}, 500*time.Millisecond, 20*time.Millisecond), "a newer revision moves no engine")
	_, kept := e.revision(t, "rauthy-1")
	require.True(t, kept, "login pins rauthy-1, so it stays")

	edited := identityProvider("default")
	edited.Spec.Config = json.RawMessage(`{"issuer":"https://login.example.com/auth/v2","port":8080}`)
	e.apply(t, e.dev, edited)
	e.pin(t, "default", "login", "rauthy-2", newerImage)
	require.Eventually(t, func() bool {
		_, ok := e.revision(t, "rauthy-1")
		return !ok
	}, 5*time.Second, 20*time.Millisecond, "nothing pins rauthy-1 any more: it is removed")
}

// scenario: addon-revision-new-object — an object created after the Addon moved pins the current revision.
func TestScenarioAddonRevisionNewObject(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.waitCurrent(t, "rauthy-1")
	e.updateImage(t, newerImage)
	e.waitCurrent(t, "rauthy-2")
	e.apply(t, e.dev, identityProvider("team"))
	e.pin(t, "team", "login", "rauthy-2", newerImage)
}

// scenario: addon-revision-read-only — the AddonRevision routes answer POST, PUT and DELETE with 405 and Allow: GET; an
// admin lists and reads them, and a developer reads none.
func TestScenarioAddonRevisionReadOnly(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.waitCurrent(t, "rauthy-1")
	rev, _ := e.revision(t, "rauthy-1")
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, addonRevisionsPath}, {http.MethodPut, addonRevisionsPath + "/rauthy-1"},
		{http.MethodDelete, addonRevisionsPath + "/rauthy-1"},
	} {
		req, err := http.NewRequestWithContext(e.ctx, c.method, "http://"+e.p.Addr()+c.path, strings.NewReader(revisionJSON(t, rev)))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+addonAdminToken)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = res.Body.Close()
		require.Equal(t, http.StatusMethodNotAllowed, res.StatusCode, "%s %s", c.method, c.path)
		require.Equal(t, "GET", res.Header.Get("Allow"), "%s %s", c.method, c.path)
	}
	items, err := e.admin.List(e.ctx, v1.KindAddonRevision, "")
	require.NoError(t, err)
	require.Len(t, items, 1)
	got, err := e.admin.Get(e.ctx, v1.KindAddonRevision, "", "rauthy-1")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("rauthy"), got.(*v1.AddonRevision).Spec.Addon)
	status, _ := e.send(t, http.MethodGet, addonRevisionsPath, addonDevToken, nil)
	require.Equal(t, http.StatusForbidden, status, "a developer reads no AddonRevision")
}

func revisionJSON(t *testing.T, rev *v1.AddonRevision) string {
	t.Helper()
	b, err := json.Marshal(rev)
	require.NoError(t, err)
	return string(b)
}

// ADR-0339 Decisions 3 and 4 on the platform: an App's part pins its App revision's record once that revision is
// applied; the Addon's change moves it only through the App's next revision, and the App is Ready once it runs it.
func TestAddonRevisionAppPartMoves(t *testing.T) {
	t.Parallel()
	e := startAddons(t, []string{"containerd"})
	e.apply(t, e.admin, rauthyAddon("rauthy"))
	e.waitCurrent(t, "rauthy-1")
	e.apply(t, e.dev, loginApp("default", "web-auth"))
	e.waitApp(t, "default", "web-auth")
	e.pin(t, "default", "web-auth-login", "rauthy-1", rauthyAddon("rauthy").Spec.Image)

	e.updateImage(t, newerImage)
	e.waitCurrent(t, "rauthy-2")
	require.True(t, poll.Never(func() bool {
		o, _ := e.object(t, "default", "web-auth-login")
		return o.Status.AddonRevision != "rauthy-1"
	}, 300*time.Millisecond, 20*time.Millisecond), "the Addon's change alone moves no part")

	next := loginApp("default", "web-auth")
	next.Spec.Version = "1.1.0"
	e.apply(t, e.dev, next)
	e.pin(t, "default", "web-auth-login", "rauthy-2", newerImage)
	app := e.waitApp(t, "default", "web-auth")
	require.Equal(t, v1.ObjectName("web-auth-2"), app.Status.CurrentRevision)
	o, _ := e.object(t, "default", "web-auth-login")
	require.Equal(t, v1.ObjectName("web-auth-2"), o.Status.AppRevision)
	obj, err := e.p.cfg.store.Get(e.ctx, v1.KindAppRevision.GVK(), "default", "web-auth-2")
	require.NoError(t, err)
	require.Equal(t, []v1.AppAddonRevision{{Kind: "IdentityProvider", Revision: "rauthy-2"}},
		obj.(*v1.AppRevision).Spec.AddonRevisions, "web-auth-2 records rauthy-2")
}
