package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/pkg/sdk"
)

func todoSpec(version string) v1.AppSpec {
	return v1.AppSpec{Version: version, Functions: []v1.AppFunction{{Name: "todo-api", FunctionSpec: v1.FunctionSpec{
		Runtime: "nodejs22", Handler: "index.handler", Image: "oci-layout://todo-api:" + version,
	}}}}
}

// applyTodo applies the App todo in team-a through the API and returns it as stored.
func applyTodo(t *testing.T, c *sdk.Client, spec v1.AppSpec) *v1.App {
	t.Helper()
	app := &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "todo", Namespace: "team-a", ResourceGroup: "rg1"},
		Spec:       spec,
	}
	obj, err := c.Apply(context.Background(), app)
	require.NoError(t, err)
	return obj.(*v1.App)
}

// seedAppRevision writes an AppRevision as the App reconciler does: straight to the store, the API being read-only.
func seedAppRevision(t *testing.T, st store.Store, app v1.ObjectName, uid v1.UID, n int64, spec v1.AppSpec, phase v1.Phase) *v1.AppRevision {
	t.Helper()
	ref := v1.ObjectRef{Kind: v1.KindApp, Namespace: "team-a", Name: app}
	r := &v1.AppRevision{
		TypeMeta: v1.TypeMeta{APIVersion: v1.KindAppRevision.GVK().APIVersion(), Kind: v1.KindAppRevision},
		ObjectMeta: v1.ObjectMeta{Name: v1.AppRevisionName(app, n), Namespace: "team-a", ResourceGroup: "rg1",
			OwnerReferences: []v1.OwnerReference{{ObjectRef: ref, UID: uid, Controller: true, BlockOwnerDeletion: true}}},
		Spec: v1.AppRevisionSpec{App: ref, Number: n, Spec: spec},
	}
	r.Status.Phase = phase
	created, err := st.Create(context.Background(), r)
	require.NoError(t, err)
	return created.(*v1.AppRevision)
}

func resourceVersion(t *testing.T, c *sdk.Client, kind v1.Kind, name v1.ObjectName) string {
	t.Helper()
	obj, err := c.Get(context.Background(), kind, "team-a", name)
	require.NoError(t, err)
	return obj.GetObjectMeta().ResourceVersion
}

// ADR-0200 Decision 9 and ADR-0212 Decision 9: the app group holds history, rollback, pause and resume.
func TestCLIAppGroupVerbs(t *testing.T) {
	t.Parallel()
	app, _, err := newRootCmdWith(&bytes.Buffer{}, nil).Find([]string{"app"})
	require.NoError(t, err)
	var verbs []string
	for _, sub := range app.Commands() {
		verbs = append(verbs, sub.Name())
	}
	require.Equal(t, []string{"history", "pause", "resume", "rollback"}, verbs)
	require.Equal(t, "Manage Apps (history|rollback|pause|resume)", app.Short)
}

// ADR-0200 Decision 9: app history lists the App's revisions by number with REVISION, VERSION, PHASE and STAMPED;
// while the App exists, a namesake revision of another UID is left out; -o json returns the same revisions in order.
func TestScenarioCLIAppHistory(t *testing.T) {
	t.Parallel()
	c, _, st := newTestServer(t)
	app := applyTodo(t, c, todoSpec("3.0.0"))

	ten := seedAppRevision(t, st, "todo", app.UID, 10, todoSpec("3.0.0"), v1.PhaseReady)
	one := seedAppRevision(t, st, "todo", app.UID, 1, todoSpec("1.0.0"), v1.PhaseReady)
	two := seedAppRevision(t, st, "todo", app.UID, 2, todoSpec("2.0.0"), v1.PhaseFailed)
	seedAppRevision(t, st, "todo", "uid-of-a-deleted-todo", 4, todoSpec("0.9.0"), v1.PhaseReady)
	seedAppRevision(t, st, "todo-x", "uid-of-todo-x", 1, todoSpec("9.0.0"), v1.PhaseReady)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "history", "todo", "-n", "team-a"))
	var rows [][]string
	for line := range strings.Lines(out.String()) {
		rows = append(rows, strings.Fields(line))
	}
	require.Equal(t, [][]string{
		{"REVISION", "VERSION", "PHASE", "STAMPED"},
		{"1", "1.0.0", "Ready", one.CreationTime.String()},
		{"2", "2.0.0", "Failed", two.CreationTime.String()},
		{"10", "3.0.0", "Ready", ten.CreationTime.String()},
	}, rows, out.String())

	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "history", "todo", "-n", "team-a", "-o", "json"))
	var listed []v1.AppRevision
	require.NoError(t, json.Unmarshal(out.Bytes(), &listed), out.String())
	var names []v1.ObjectName
	for _, r := range listed {
		names = append(names, r.Name)
	}
	require.Equal(t, []v1.ObjectName{"todo-1", "todo-2", "todo-10"}, names)
	require.Equal(t, v1.PhaseFailed, listed[1].Status.Phase)

	err := execCLI(&bytes.Buffer{}, c, "app", "history", "todo", "-n", "team-a", "-o", "yaml")
	require.Equal(t, fault.Invalid, fault.KindOf(err))

	// Without the App, every revision named for it is listed, whatever its controller UID.
	seedAppRevision(t, st, "gone", "uid-a", 2, todoSpec("2.0.0"), v1.PhaseReady)
	seedAppRevision(t, st, "gone", "uid-b", 1, todoSpec("1.0.0"), v1.PhaseFailed)
	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "history", "gone", "-n", "team-a"))
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 3, out.String())
	require.Equal(t, []string{"1", "1.0.0", "Failed"}, strings.Fields(lines[1])[:3])
	require.Equal(t, []string{"2", "2.0.0", "Ready"}, strings.Fields(lines[2])[:3])
}

// ADR-0200 Decision 9 and scenario app-history-kept: app rollback applies the App with an earlier revision's spec and
// writes nothing for a bad number, an absent revision, a revision of another App UID or an equal spec.
func TestScenarioCLIAppRollback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c, _, st := newTestServer(t)
	app := applyTodo(t, c, todoSpec("2.0.0"))
	seedAppRevision(t, st, "todo", app.UID, 1, todoSpec("1.0.0"), v1.PhaseReady)
	seedAppRevision(t, st, "todo", app.UID, 2, todoSpec("2.0.0"), v1.PhaseReady)
	seedAppRevision(t, st, "todo", "uid-of-a-deleted-todo", 3, todoSpec("0.9.0"), v1.PhaseReady)
	revRVs := map[v1.ObjectName]string{}
	for _, name := range []v1.ObjectName{"todo-1", "todo-2", "todo-3"} {
		revRVs[name] = resourceVersion(t, c, v1.KindAppRevision, name)
	}
	appRV := resourceVersion(t, c, v1.KindApp, "todo")

	for _, n := range []string{"0", "-1", "two", "1.5", "+"} {
		err := execCLI(&bytes.Buffer{}, c, "app", "rollback", "-n", "team-a", "--", "todo", n)
		require.Equal(t, fault.Invalid, fault.KindOf(err), n)
		require.ErrorContains(t, err, "is not a positive integer", n)
	}

	err := execCLI(&bytes.Buffer{}, c, "app", "rollback", "todo", "7", "-n", "team-a")
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.ErrorContains(t, err, "AppRevision todo-7 does not exist")
	require.ErrorContains(t, err, "app.revisionHistory")

	err = execCLI(&bytes.Buffer{}, c, "app", "rollback", "todo", "3", "-n", "team-a")
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.ErrorContains(t, err, "AppRevision todo-3 is not a revision of App todo")

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "rollback", "todo", "2", "-n", "team-a"))
	require.Equal(t, "no change: App todo already has the spec of todo-2\n", out.String())
	require.Equal(t, appRV, resourceVersion(t, c, v1.KindApp, "todo"), "nothing is written so far")

	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "rollback", "todo", "1", "-n", "team-a"))
	require.Equal(t, "applied App todo with the spec of todo-1\n", out.String())
	obj, err := c.Get(ctx, v1.KindApp, "team-a", "todo")
	require.NoError(t, err)
	rolled := obj.(*v1.App)
	require.Equal(t, todoSpec("1.0.0"), rolled.Spec)
	require.Equal(t, app.UID, rolled.UID)
	require.Equal(t, app.Generation+1, rolled.Generation)
	for name, rv := range revRVs {
		require.Equal(t, rv, resourceVersion(t, c, v1.KindAppRevision, name), "rollback writes only the App, not %s", name)
	}

	seedAppRevision(t, st, "gone", "uid-a", 1, todoSpec("1.0.0"), v1.PhaseReady)
	err = execCLI(&bytes.Buffer{}, c, "app", "rollback", "gone", "1", "-n", "team-a")
	require.Equal(t, fault.NotFound, fault.KindOf(err), "a revision without its App is not applied")
}

func getApp(t *testing.T, c *sdk.Client) *v1.App {
	t.Helper()
	obj, err := c.Get(context.Background(), v1.KindApp, "team-a", "todo")
	require.NoError(t, err)
	return obj.(*v1.App)
}

// ADR-0212 Decision 9: app pause and resume set spec.paused alone and print the verb; -n names the namespace, which
// defaults to default.
func TestCLIAppPauseResume(t *testing.T) {
	t.Parallel()
	c, _, _ := newTestServer(t)
	app := applyTodo(t, c, todoSpec("1.0.0"))

	require.Error(t, execCLI(&bytes.Buffer{}, c, "app", "pause", "todo"), "without -n the App is looked up in default")
	require.Equal(t, app.ResourceVersion, resourceVersion(t, c, v1.KindApp, "todo"))

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "pause", "todo", "-n", "team-a"))
	require.Equal(t, "paused todo\n", out.String())
	got := getApp(t, c)
	want := todoSpec("1.0.0")
	want.Paused = true
	require.Equal(t, want, got.Spec)
	require.Equal(t, app.UID, got.UID)
	require.Equal(t, app.Generation+1, got.Generation)

	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "resume", "todo", "--namespace", "team-a"))
	require.Equal(t, "resumed todo\n", out.String())
	require.Equal(t, todoSpec("1.0.0"), getApp(t, c).Spec)
}

// putConflicts answers the next armed PUTs of one path with a Conflict, as the API does when the App changed between
// the read and the write, and counts the PUTs of that path.
type putConflicts struct {
	mu   sync.Mutex
	path string
	left int
	puts int
}

func (p *putConflicts) arm(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.left, p.puts = n, 0
}

func (p *putConflicts) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.puts
}

func (p *putConflicts) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut && r.URL.Path == p.path {
		p.mu.Lock()
		p.puts++
		fail := p.left > 0
		if fail {
			p.left--
		}
		p.mu.Unlock()
		if fail {
			rec := httptest.NewRecorder()
			fault.WriteProblem(rec, fault.Conflictf("store.Update", "App %q resourceVersion mismatch", "todo"))
			return rec.Result(), nil
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// ADR-0212 Decision 9 on ADR-0210 Decision 4: a PUT that answers Conflict re-reads the App and retries, at most five
// attempts in all.
func TestCLIAppPauseRetriesAConflict(t *testing.T) {
	t.Parallel()
	_, url, _ := newTestServer(t)
	conflicts := &putConflicts{path: "/apis/funcd.io/v1alpha1/namespaces/team-a/apps/todo"}
	c, err := sdk.New(url, sdk.WithToken(devToken), sdk.WithHTTPClient(&http.Client{Transport: conflicts}))
	require.NoError(t, err)
	applyTodo(t, c, todoSpec("1.0.0"))

	conflicts.arm(2)
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "pause", "todo", "-n", "team-a"))
	require.Equal(t, "paused todo\n", out.String())
	require.Equal(t, 3, conflicts.count(), "two Conflicts, then the write")
	require.True(t, getApp(t, c).Spec.Paused)

	conflicts.arm(appApplyAttempts)
	err = execCLI(&bytes.Buffer{}, c, "app", "resume", "todo", "-n", "team-a")
	require.Equal(t, fault.Conflict, fault.KindOf(err))
	require.Equal(t, 5, conflicts.count())
	require.True(t, getApp(t, c).Spec.Paused, "nothing is written")
}

// ADR-0212 Decisions 3 and 9: rollback copies the revision's spec and keeps the App's paused, and sameAppSpec ignores
// paused, so a paused App with the revision's spec has no change.
func TestCLIAppRollbackKeepsPaused(t *testing.T) {
	t.Parallel()
	c, _, st := newTestServer(t)
	paused := todoSpec("2.0.0")
	paused.Paused = true
	app := applyTodo(t, c, paused)
	seedAppRevision(t, st, "todo", app.UID, 1, todoSpec("1.0.0"), v1.PhaseReady)
	seedAppRevision(t, st, "todo", app.UID, 2, todoSpec("2.0.0"), v1.PhaseReady)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "app", "rollback", "todo", "2", "-n", "team-a"))
	require.Equal(t, "no change: App todo already has the spec of todo-2\n", out.String())
	require.Equal(t, app.ResourceVersion, resourceVersion(t, c, v1.KindApp, "todo"))

	out.Reset()
	require.NoError(t, execCLI(&out, c, "app", "rollback", "todo", "1", "-n", "team-a"))
	require.Equal(t, "applied App todo with the spec of todo-1\n", out.String())
	want := todoSpec("1.0.0")
	want.Paused = true
	require.Equal(t, want, getApp(t, c).Spec)

	same, err := sameAppSpec(want, todoSpec("1.0.0"))
	require.NoError(t, err)
	require.True(t, same)
	same, err = sameAppSpec(want, todoSpec("2.0.0"))
	require.NoError(t, err)
	require.False(t, same)
}
