//go:build e2e

package funcd_test

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// healWithin is how long the App takes at most to write a hand edit back (ADR-0212 Scenarios).
const healWithin = 5 * time.Second

// selfHeals records the attributes of each self-healed line the platform logs, the logger's own (component) included.
type selfHeals struct {
	mu    *sync.Mutex
	lines *[]map[string]string
	attrs []slog.Attr
}

func newSelfHeals() selfHeals { return selfHeals{mu: &sync.Mutex{}, lines: &[]map[string]string{}} }

func (s selfHeals) Enabled(context.Context, slog.Level) bool { return true }

func (s selfHeals) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "self-healed" {
		return nil
	}
	m := map[string]string{"level": r.Level.String()}
	for _, a := range s.attrs {
		m[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.String()
		return true
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.lines = append(*s.lines, m)
	return nil
}

func (s selfHeals) WithAttrs(as []slog.Attr) slog.Handler {
	s.attrs = append(slices.Clone(s.attrs), as...)
	return s
}

func (s selfHeals) WithGroup(string) slog.Handler { return s }

func (s selfHeals) seen() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(*s.lines)
}

// todoAPIHealed is the self-healed line of Function todo-api.
func todoAPIHealed() map[string]string {
	return map[string]string{
		"level": "INFO", "component": "app", "kind": "Function", "namespace": "default", "name": "todo-api", "app": "todo",
	}
}

// handEdit applies Function todo-api with image, as funcdctl apply of the Function does.
func (e *gcEnv) handEdit(t *testing.T, image string) {
	t.Helper()
	fn := e.object(t, v1.KindFunction, "todo-api").(*v1.Function)
	fn.Spec.Image = image
	e.apply(t, fn)
}

func (e *gcEnv) apiImage(t *testing.T) string {
	t.Helper()
	return e.object(t, v1.KindFunction, "todo-api").(*v1.Function).Spec.Image
}

// waitPaused waits until the App todo's Paused condition has status and reason, and returns it.
func (e *gcEnv) waitPaused(t *testing.T, status v1.ConditionStatus, reason string) v1.Condition {
	t.Helper()
	var c v1.Condition
	require.Eventually(t, func() bool {
		var ok bool
		c, ok = e.app(t, "todo").Status.Conditions.Get("Paused")
		return ok && c.Status == status && c.Reason == reason
	}, healWithin, 20*time.Millisecond, "Paused=%s %s", status, reason)
	return c
}

// historyRows runs funcdctl app history todo and returns its revision numbers.
func historyRows(t *testing.T, cli func(...string) (string, error)) []string {
	t.Helper()
	out, err := cli("app", "history", "todo")
	require.NoError(t, err, out)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Equal(t, []string{"REVISION", "VERSION", "PHASE", "STAMPED"}, strings.Fields(lines[0]))
	var revs []string
	for _, l := range lines[1:] {
		revs = append(revs, strings.Fields(l)[0])
	}
	return revs
}

func requireHealedAt(t *testing.T, h *v1.AppSelfHeal, after time.Time) {
	t.Helper()
	require.NotNil(t, h, "status.lastSelfHeal is set")
	require.Equal(t, v1.KindFunction, h.Kind)
	require.Equal(t, v1.ObjectName("todo-api"), h.Name)
	require.False(t, time.Time(h.At).Before(after.Truncate(time.Millisecond)), "lastSelfHeal.at %s is the write time", h.At)
	require.False(t, time.Time(h.At).After(time.Now()))
}

// scenario: app-drift-self-healed
func TestScenarioAppDriftSelfHealed(t *testing.T) {
	logs := newSelfHeals()
	e := startGC(t, funcd.WithLogger(slog.New(logs)))
	installTodo(t, e)
	declared := e.apiImage(t)

	edited := time.Now()
	e.handEdit(t, e.imageV2(t))
	require.Eventually(t, func() bool { return e.apiImage(t) == declared }, healWithin, 20*time.Millisecond,
		"the declared image is written back")
	require.Eventually(t, func() bool { return len(logs.seen()) == 1 }, healWithin, 20*time.Millisecond, "one self-healed line")
	require.Equal(t, []map[string]string{todoAPIHealed()}, logs.seen())
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	requireHealedAt(t, got.Status.LastSelfHeal, edited)
	first := got.Status.LastSelfHeal.At
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.LatestRevision, "no AppRevision is stamped")
	require.False(t, e.exists(t, v1.KindAppRevision, "todo-2"))

	deleted := time.Now()
	e.del(t, v1.KindFunction, "todo-api")
	require.Eventually(t, func() bool { return e.exists(t, v1.KindFunction, "todo-api") && e.apiImage(t) == declared },
		healWithin, 20*time.Millisecond, "todo-api is re-created")
	require.Eventually(t, func() bool { return len(logs.seen()) == 2 }, healWithin, 20*time.Millisecond, "a second self-healed line")
	require.Equal(t, []map[string]string{todoAPIHealed(), todoAPIHealed()}, logs.seen())
	require.Eventually(t, func() bool {
		h := e.app(t, "todo").Status.LastSelfHeal
		return h != nil && time.Time(h.At).After(time.Time(first))
	}, healWithin, 20*time.Millisecond, "lastSelfHeal records the re-create")
	requireHealedAt(t, e.app(t, "todo").Status.LastSelfHeal, deleted)
	require.False(t, e.exists(t, v1.KindAppRevision, "todo-2"))
}

// scenario: app-paused-keeps-hotfix
func TestScenarioAppPausedKeepsHotfix(t *testing.T) {
	logs := newSelfHeals()
	e := startGC(t, funcd.WithLogger(slog.New(logs)))
	installTodo(t, e)
	cli := funcdctl(t, e)
	declared := e.apiImage(t)

	out, err := cli("app", "pause", "todo")
	require.NoError(t, err, out)
	require.Equal(t, "paused todo\n", out)
	c := e.waitPaused(t, v1.ConditionTrue, "SpecPaused")
	require.Equal(t, "spec.paused is set: the App writes no part until it is resumed", c.Message)
	hotfix := e.imageV2(t)
	e.handEdit(t, hotfix)
	require.Never(t, func() bool { return e.apiImage(t) != hotfix }, 2*healWithin, 50*time.Millisecond, "the edit stays")
	got := e.app(t, "todo")
	require.Equal(t, v1.PhaseReady, got.Status.Phase, "the App keeps its phase")
	require.Nil(t, got.Status.LastSelfHeal)
	require.Empty(t, logs.seen())
	require.Equal(t, []string{"1"}, historyRows(t, cli))

	resumed := time.Now()
	out, err = cli("app", "resume", "todo")
	require.NoError(t, err, out)
	require.Equal(t, "resumed todo\n", out)
	require.Eventually(t, func() bool { return e.apiImage(t) == declared }, healWithin, 20*time.Millisecond,
		"the declared spec is back")
	e.waitPaused(t, v1.ConditionFalse, "Resumed")
	require.Eventually(t, func() bool { return e.app(t, "todo").Status.LastSelfHeal != nil }, healWithin, 20*time.Millisecond)
	requireHealedAt(t, e.app(t, "todo").Status.LastSelfHeal, resumed)
	require.Equal(t, []map[string]string{todoAPIHealed()}, logs.seen())
	require.Equal(t, []string{"1"}, historyRows(t, cli))
}

// scenario: app-rollout-is-not-self-heal
func TestScenarioAppRolloutIsNotSelfHeal(t *testing.T) {
	logs := newSelfHeals()
	e := startGC(t, funcd.WithLogger(slog.New(logs)))
	a := installTodo(t, e)

	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	e.waitCurrent(t, "todo-2")
	e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, a.Spec.Functions[0].Image, e.apiImage(t), "todo-api is written")

	a.ResourceGroup = "rg2"
	e.apply(t, a)
	require.Eventually(t, func() bool {
		for _, p := range todoV1Parts() {
			if e.object(t, p.kind, p.name).GetObjectMeta().ResourceGroup != "rg2" {
				return false
			}
		}
		return true
	}, appWithin, 20*time.Millisecond, "every part is rewritten into rg2")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Empty(t, logs.seen(), "no self-healed line")
	require.Nil(t, got.Status.LastSelfHeal)
	require.Equal(t, v1.ObjectName("todo-2"), got.Status.LatestRevision)
	require.False(t, e.exists(t, v1.KindAppRevision, "todo-3"))
}

// scenario: app-paused-defers-upgrade
func TestScenarioAppPausedDefersUpgrade(t *testing.T) {
	e := startGC(t)
	a := installTodo(t, e)
	cli := funcdctl(t, e)
	declared := e.apiImage(t)
	out, err := cli("app", "pause", "todo")
	require.NoError(t, err, out)
	e.waitPaused(t, v1.ConditionTrue, "SpecPaused")

	a.Spec.Paused = true
	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	require.Never(t, func() bool { return e.exists(t, v1.KindAppRevision, "todo-2") }, 2*time.Second, 50*time.Millisecond,
		"nothing is stamped")
	require.Equal(t, declared, e.apiImage(t), "todo-api keeps its image")
	e.waitPaused(t, v1.ConditionTrue, "SpecPaused")

	out, err = cli("app", "resume", "todo")
	require.NoError(t, err, out)
	e.waitCurrent(t, "todo-2")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.Equal(t, v1.PhaseReady, got.Status.Phase)
	require.Equal(t, "2.0.0", got.Status.Version)
	require.False(t, e.appRevision(t, "todo-2").Spec.Spec.Paused)
	code, body := e.routedBody(t, todoHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, todoV2Marker, "todo-2 rolled out")
}

// scenario: app-apply-without-paused-resumes
func TestScenarioAppApplyWithoutPausedResumes(t *testing.T) {
	e := startGC(t)
	a := installTodo(t, e)
	out, err := funcdctl(t, e)("app", "pause", "todo")
	require.NoError(t, err, out)
	e.waitPaused(t, v1.ConditionTrue, "SpecPaused")

	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageV2(t)
	e.apply(t, a)
	e.waitPaused(t, v1.ConditionFalse, "Resumed")
	e.waitCurrent(t, "todo-2")
	got := e.waitApp(t, "todo", v1.ConditionTrue, "", appWithin)
	require.False(t, got.Spec.Paused)
	require.Equal(t, "2.0.0", got.Status.Version)
	code, body := e.routedBody(t, todoHost, "/api")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, todoV2Marker, "todo-2 rolled out")
}

// scenario: app-paused-rollout-full-timeout
func TestScenarioAppPausedRolloutFullTimeout(t *testing.T) {
	e := startGC(t, failedPacing())
	a := installTodo(t, e)
	cli := funcdctl(t, e)

	a.Spec.Version = "2.0.0"
	a.Spec.Functions[0].Image = e.imageNeverStarts(t)
	e.apply(t, a)
	require.Eventually(t, func() bool { return e.exists(t, v1.KindAppRevision, "todo-2") }, appWithin, 20*time.Millisecond,
		"todo-2 is stamped")
	stamped := e.appRevision(t, "todo-2").Status.StartedAt
	require.NotNil(t, stamped)
	time.Sleep(time.Until(time.Time(*stamped).Add(5 * time.Second)))
	out, err := cli("app", "pause", "todo")
	require.NoError(t, err, out)
	e.waitPaused(t, v1.ConditionTrue, "SpecPaused")

	require.Never(t, func() bool { return e.appRevision(t, "todo-2").Status.Phase != v1.PhaseDeploying },
		30*time.Second, 100*time.Millisecond, "todo-2 stays Deploying while paused")
	out, err = cli("app", "resume", "todo")
	require.NoError(t, err, out)
	resumed := time.Time(e.waitPaused(t, v1.ConditionFalse, "Resumed").LastTransitionTime)

	r2 := e.waitRevisionPhase(t, "todo-2", v1.PhaseFailed, 2*appWithin)
	after := time.Since(resumed)
	require.GreaterOrEqual(t, after, 20*time.Second, "todo-2 fails no sooner than app.upgradeTimeout after the resume")
	require.Less(t, after, 23*time.Second, "todo-2 fails at app.upgradeTimeout after the resume")
	require.Equal(t, *stamped, *r2.Status.StartedAt, "the stamp time is kept")
	got := e.waitApp(t, "todo", v1.ConditionFalse, "ChildNotReady", appWithin)
	require.Equal(t, v1.PhaseFailed, got.Status.Phase)
	require.Equal(t, v1.ObjectName("todo-1"), got.Status.CurrentRevision)
}
