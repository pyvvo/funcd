//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// eventSource is EventSource name of default, a timer whose one event t fires every interval.
func eventSource(name v1.ObjectName, interval time.Duration) *v1.EventSource {
	es := &v1.EventSource{TypeMeta: v1.TypeMeta{APIVersion: v1.KindEventSource.GVK().APIVersion(), Kind: v1.KindEventSource},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	es.Spec.Timer = &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(interval)}}}
	return es
}

// sensor is Sensor name of default, whose action a calls Function fn on each event t of source.
func sensor(name, source, fn v1.ObjectName) *v1.Sensor {
	se := &v1.Sensor{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSensor.GVK().APIVersion(), Kind: v1.KindSensor},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	se.Spec.On = []v1.Dependency{{Name: "d", Source: source, Event: "t"}}
	se.Spec.Do = []v1.Action{{Name: "a", On: "d", Function: fn}}
	return se
}

// workflowRun is WorkflowRun name of default, a run of Workflow wf with input when it is not empty.
func workflowRun(name, wf v1.ObjectName, input string) *v1.WorkflowRun {
	run := &v1.WorkflowRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}}
	run.Spec.Workflow = wf
	if input != "" {
		run.Spec.Input = json.RawMessage(input)
	}
	return run
}

// itemPath is the path of obj, an object of a namespaced kind.
func itemPath(obj v1.Object) string {
	m := obj.GetObjectMeta()
	return "/apis/funcd.io/v1alpha1/namespaces/" + string(m.Namespace) + "/" + plural(obj.GroupVersionKind().Kind) + "/" +
		string(m.Name)
}

// readBody is the body that path answers the admin's token, nil when the request fails.
func readBody(c *http.Client, base, path string) []byte {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+answersAdmin)
	resp, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	return body
}

// deadLetters are the IDs of default's dead letters, by the Sensor that parked them.
func deadLetters(e *answerEnv) map[v1.ObjectName][]string {
	var list struct {
		Items []struct {
			ID     string        `json:"id"`
			Sensor v1.ObjectName `json:"sensor"`
		} `json:"items"`
	}
	out := map[v1.ObjectName][]string{}
	if json.Unmarshal(readBody(e.c, e.api, "/apis/funcd.io/v1alpha1/namespaces/default/deadletters"), &list) != nil {
		return out
	}
	for _, dl := range list.Items {
		out[dl.Sensor] = append(out[dl.Sensor], dl.ID)
	}
	return out
}

// The answers of the events feature's operations, one row per case of each: EventSource, Sensor and the dead letters.
func TestEventsAnswersMatrix(t *testing.T) {
	t.Parallel()
	hd, err := hold.Open(t.TempDir())
	require.NoError(t, err)
	e := newAnswerEnv(t, funcd.WithHold(hd), funcd.WithDeadLetterQueue("", 1, 0, 0), asCluster(t))
	placeOnOfflineNode(t, e)
	holding, release := make(chan struct{}, 1), make(chan struct{})
	e.workers.serve(t, "late", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	e.workers.serve(t, "sticky", func(_ http.ResponseWriter, r *http.Request) {
		select {
		case holding <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	callable(t, e.c, e.api, "late", "", nil)
	callable(t, e.c, e.api, "sticky", "", nil)
	site := function("site")
	site.Spec.Protocol = v1.ProtocolHTTP
	for _, obj := range []v1.Object{site, eventSource("es", time.Hour), sensor("se", "es", "ghost")} {
		create(t, e.c, e.api, obj)
	}
	// Each event of tick parks a dead letter of each of these Sensors: their Function ghost does not exist, and
	// eventing.deliveryAttempts is 1.
	parked := []v1.ObjectName{"doomed", "gone", "renamed", "garbled", "healed", "stuck", "web"}
	for _, name := range parked {
		create(t, e.c, e.api, sensor(name, "tick", "ghost"))
	}
	create(t, e.c, e.api, eventSource("tick", 100*time.Millisecond))
	var ids map[v1.ObjectName][]string
	require.Eventually(t, func() bool {
		ids = deadLetters(e)
		return len(ids["doomed"]) >= 3 && !slices.ContainsFunc(parked, func(s v1.ObjectName) bool { return len(ids[s]) == 0 })
	}, 20*time.Second, 50*time.Millisecond, "each Sensor of tick parks a dead letter")
	tick := eventSource("tick", 0)
	ask{what: "the delete of tick", method: http.MethodDelete, path: itemPath(tick), token: answersAdmin,
		status: http.StatusNoContent}.run(t, e.c, e.api)
	// A replay calls the action of the Sensor as it is now. Each changed Sensor listens to es, whose next event is an
	// hour away, so an event of tick never reaches the Function that a replay calls.
	renamed, garbled := sensor("renamed", "es", "ghost"), sensor("garbled", "es", "ghost")
	renamed.Spec.Do[0].Name = "b"
	garbled.Spec.Do[0].Input = json.RawMessage(`[1]`)
	for _, se := range []*v1.Sensor{sensor("healed", "es", "late"), sensor("stuck", "es", "sticky"),
		sensor("web", "es", "site"), renamed, garbled} {
		ask{what: "the replace of " + string(se.Name), method: http.MethodPut, path: itemPath(se), token: answersAdmin,
			body: jsonOf(t, se), status: http.StatusOK}.run(t, e.c, e.api)
	}
	ask{what: "the delete of gone", method: http.MethodDelete, path: itemPath(sensor("gone", "", "")),
		token: answersAdmin, status: http.StatusNoContent}.run(t, e.c, e.api)
	require.NoError(t, hd.HoldNamespace("held", hold.Marker{Reason: hold.ReasonData, Since: v1.NewTimestamp(time.Now())}))
	// pastReplay sends a's replay while another replay of the same dead letter, which sticky's worker keeps until the
	// row ends, holds it, and returns a's answer.
	pastReplay := func(t *testing.T, e *answerEnv, a ask) *http.Response {
		t.Helper()
		done := make(chan struct{})
		t.Cleanup(func() { close(release); <-done })
		go func() {
			defer close(done)
			if resp, err := (ask{method: http.MethodPost, path: a.path, token: answersDev}).do(e.c, e.api); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
		select {
		case <-holding:
		case <-time.After(10 * time.Second):
			t.Fatal("the first replay did not reach its worker")
		}
		resp, err := a.do(e.c, e.api)
		require.NoError(t, err)
		return resp
	}

	kindless := eventSource("es", time.Hour)
	kindless.Spec.Timer = nil
	undeclared := sensor("se", "es", "ghost")
	undeclared.Spec.Do[0].On = "nope"
	const dls = "/apis/funcd.io/v1alpha1/namespaces/default/deadletters"
	const absent = dls + "/01ABSENT"
	row := func(op, when string, a ask) answerRow { return answerRow{op: op, when: when, on: e, ask: a} }
	rows := slices.Concat(
		kindFixture{kind: "EventSource", kinds: "EventSources", path: "eventsources", name: "es",
			json: jsonOf(t, eventSource("es", time.Hour)), broken: jsonOf(t, kindless),
			detail: "exactly one source kind must be set"}.sharedRows(e),
		kindFixture{kind: "Sensor", kinds: "Sensors", path: "sensors", name: "se",
			json: jsonOf(t, sensor("se", "es", "ghost")), broken: jsonOf(t, undeclared),
			detail: `is bound to undeclared dependency "nope"`}.sharedRows(e),
		[]answerRow{
			row("listDeadLetters", "", ask{what: "the list", method: http.MethodGet, path: dls, token: answersDev,
				detail: `"sensor":"doomed"`}),
			row("listDeadLetters", "carries no bearer token", ask{what: "no credential", method: http.MethodGet,
				path: dls, detail: "missing bearer token"}),
			row("listDeadLetters", "do not allow it", ask{what: "another namespace's developer", method: http.MethodGet,
				path: dls, token: answersOther, detail: `dead-letter get in "default" denied`}),
			row("listDeadLetters", "in the path is not", ask{what: "a namespace that is no DNS-1123 label",
				method: http.MethodGet, path: "/apis/funcd.io/v1alpha1/namespaces/Not_A_Label/deadletters",
				token: answersDev, detail: "path.namespace"}),

			row("getDeadLetter", "", ask{what: "a dead letter", method: http.MethodGet, path: dls + "/" + ids["doomed"][0],
				token: answersDev, detail: `"id":"` + ids["doomed"][0] + `"`}),
			row("getDeadLetter", "carries no bearer token", ask{what: "no credential", method: http.MethodGet,
				path: absent, detail: "missing bearer token"}),
			row("getDeadLetter", "do not allow it", ask{what: "another namespace's developer", method: http.MethodGet,
				path: absent, token: answersOther, detail: `dead-letter get in "default" denied`}),
			row("getDeadLetter", "No dead letter of that ID", ask{what: "a missing dead letter", method: http.MethodGet,
				path: absent, token: answersDev, detail: `dead letter "default"/"01ABSENT" not found`}),
			row("getDeadLetter", "in the path is not", ask{what: "a namespace that is no DNS-1123 label",
				method: http.MethodGet, path: "/apis/funcd.io/v1alpha1/namespaces/Not_A_Label/deadletters/01ABSENT",
				token: answersDev, detail: "path.namespace"}),

			row("discardDeadLetter", "", ask{what: "a dead letter", method: http.MethodDelete,
				path: dls + "/" + ids["doomed"][1], token: answersDev}),
			row("discardDeadLetter", "carries no bearer token", ask{what: "no credential", method: http.MethodDelete,
				path: absent, detail: "missing bearer token"}),
			row("discardDeadLetter", "do not allow it", ask{what: "another namespace's developer",
				method: http.MethodDelete, path: absent, token: answersOther, detail: `dead-letter delete in "default" denied`}),
			row("discardDeadLetter", "in the path is not", ask{what: "a namespace that is no DNS-1123 label",
				method: http.MethodDelete, path: "/apis/funcd.io/v1alpha1/namespaces/Not_A_Label/deadletters/01ABSENT",
				token: answersDev, detail: "path.namespace"}),
			row("discardDeadLetter", "The query holds a parameter", ask{what: "an unknown query parameter",
				method: http.MethodDelete, path: absent + "?force=true", token: answersDev, detail: "unknown query parameter"}),

			row("replayDeadLetter", "", ask{what: "a dead letter whose action now succeeds", method: http.MethodPost,
				path: dls + "/" + ids["healed"][0] + "/replay", token: answersDev}),
			row("replayDeadLetter", "carries no bearer token", ask{what: "no credential", method: http.MethodPost,
				path: absent + "/replay", detail: "missing bearer token"}),
			row("replayDeadLetter", "do not allow it", ask{what: "another namespace's developer", method: http.MethodPost,
				path: absent + "/replay", token: answersOther, detail: `dead-letter delete in "default" denied`}),
			row("replayDeadLetter", "No dead letter of that ID", ask{what: "a missing dead letter",
				method: http.MethodPost, path: absent + "/replay", token: answersDev,
				detail: `dead letter "default"/"01ABSENT" not found`}),
			row("replayDeadLetter", "names no longer exists", ask{what: "a deleted Sensor", method: http.MethodPost,
				path: dls + "/" + ids["gone"][0] + "/replay", token: answersDev,
				detail: `sensor "default"/"gone" no longer exists`}),
			row("replayDeadLetter", "no longer has the dead letter's action", ask{what: "a removed action",
				method: http.MethodPost, path: dls + "/" + ids["renamed"][0] + "/replay", token: answersDev,
				detail: `action "a" is no longer defined on sensor "default"/"renamed"`}),
			{op: "replayDeadLetter", when: "Another replay", on: e, send: pastReplay, ask: ask{
				what: "a dead letter that another replay holds", method: http.MethodPost,
				path: dls + "/" + ids["stuck"][0] + "/replay", token: answersDev, detail: "is already being replayed"}},
			row("replayDeadLetter", "serves HTTP", ask{what: "an action whose Function serves HTTP",
				method: http.MethodPost, path: dls + "/" + ids["web"][0] + "/replay", token: answersDev,
				detail: "TargetServesHTTP"}),
			row("replayDeadLetter", "input in the Sensor", ask{what: "an action input that is no object",
				method: http.MethodPost, path: dls + "/" + ids["garbled"][0] + "/replay", token: answersDev,
				detail: "action input is not a JSON object"}),
			row("replayDeadLetter", "is held", ask{what: "a held namespace", method: http.MethodPost,
				path: "/apis/funcd.io/v1alpha1/namespaces/held/deadletters/01ABSENT/replay", token: answersAdmin,
				detail: "the platform or the namespace is held"}),
			row("replayDeadLetter", "delivery failed again", ask{what: "an action that fails again",
				method: http.MethodPost, path: dls + "/" + ids["doomed"][2] + "/replay", token: answersDev,
				detail: "delivery failed (entry re-parked)"}),
			row("replayDeadLetter", "in the path is not", ask{what: "a namespace that is no DNS-1123 label",
				method: http.MethodPost, path: "/apis/funcd.io/v1alpha1/namespaces/Not_A_Label/deadletters/01ABSENT/replay",
				token: answersDev, detail: "path.namespace"}),
			row("replayDeadLetter", "The query holds a parameter", ask{what: "an unknown query parameter",
				method: http.MethodPost, path: absent + "/replay?force=true", token: answersDev,
				detail: "unknown query parameter"}),
			awayRow("listDeadLetters", e, http.MethodGet, "deadletters"),
			awayRow("getDeadLetter", e, http.MethodGet, "deadletters/01ABSENT"),
			awayRow("discardDeadLetter", e, http.MethodDelete, "deadletters/01ABSENT"),
			awayRow("replayDeadLetter", e, http.MethodPost, "deadletters/01ABSENT/replay"),
		},
	)
	runMatrix(t, rows)
}

// typedContract is the contract of every step: an object input that requires the string a.
type typedContract struct{}

func (typedContract) Contract(context.Context, string) (v1.WorkflowContract, string, error) {
	return v1.WorkflowContract{Input: json.RawMessage(`{"type":"object","required":["a"],"properties":{"a":{"type":"string"}}}`),
		Output: json.RawMessage(`{"type":"object"}`)}, "", nil
}

// The answers of the Workflow feature's operations, one row per case of each: Workflow, WorkflowRun and the logs of a
// run.
func TestWorkflowAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t, funcd.WithWorkflowContractResolver(typedContract{}), asCluster(t))
	placeOnOfflineNode(t, e)
	typed := workflow("typed")
	typed.Spec.Steps = []v1.WorkflowStep{{Name: "s", Function: &v1.FunctionStep{Ref: "typedfn"}}}
	run := workflowRun("run", "wf", "")
	for _, obj := range []v1.Object{function("typedfn"), workflow("wf"), typed, run} {
		create(t, e.c, e.api, obj)
	}
	require.Eventually(t, func() bool {
		var wf v1.Workflow
		return json.Unmarshal(readBody(e.c, e.api, itemPath(typed)), &wf) == nil && wf.Status.Phase == v1.PhaseReady &&
			wf.Status.Contract != nil && len(wf.Status.Contract.Input) > 0
	}, 20*time.Second, 20*time.Millisecond, "Workflow typed caches its steps' contract")
	require.Eventually(t, func() bool {
		var got v1.WorkflowRun
		return json.Unmarshal(readBody(e.c, e.api, itemPath(run)), &got) == nil && got.Status.TraceID != ""
	}, 20*time.Second, 20*time.Millisecond, "the run starts")

	wf := kindFixture{kind: "Workflow", kinds: "Workflows", path: "workflows", name: "wf",
		json: jsonOf(t, workflow("wf")), detail: `step "s" depends on unknown step "nope"`}
	cyclic := workflow("wf")
	cyclic.Spec.Steps[0].DependsOn = []v1.ObjectName{"nope"}
	wf.broken = jsonOf(t, cyclic)
	replay := workflowRun("run", "wf", `{"a":1}`)
	replay.Spec.Replay = &v1.ReplaySeed{Run: "x", From: "s"}
	unbound := func(name v1.ObjectName) string {
		w := workflow(name)
		w.Spec.Steps = []v1.WorkflowStep{{Name: "s", Function: &v1.FunctionStep{Image: "registry.example.com/step:1",
			Bindings: v1.StepBindings{KV: []v1.FunctionKV{{Alias: "b", Store: "ghost", Table: "t"}}}}}}
		return jsonOf(t, w)
	}
	const wfs = "/apis/funcd.io/v1alpha1/namespaces/default/workflows"
	const runs = "/apis/funcd.io/v1alpha1/namespaces/default/workflowruns"
	const logs = runs + "/run/logs"
	big := `"` + strings.Repeat("x", 300<<10) + `"`
	input := func(name, wf v1.ObjectName, in string) string { return jsonOf(t, workflowRun(name, wf, in)) }
	row := func(op, when string, a ask) answerRow { return answerRow{op: op, when: when, on: e, ask: a} }
	rows := slices.Concat(
		wf.sharedRows(e),
		kindFixture{kind: "WorkflowRun", kinds: "WorkflowRuns", path: "workflowruns", name: "run",
			json: jsonOf(t, run), broken: jsonOf(t, replay),
			detail: "spec.input must be empty when spec.replay is set"}.sharedRows(e),
		[]answerRow{
			row("createWorkflow", "A binding of an image step", ask{what: "a binding to an undeclared store",
				method: http.MethodPost, path: wfs, token: answersDev, body: unbound("unbound"),
				detail: `KVStore "ghost" is not declared in spec.kv`}),
			row("replaceWorkflow", "A binding of an image step", ask{what: "a binding to an undeclared store",
				method: http.MethodPut, path: wfs + "/wf", token: answersDev, body: unbound("wf"),
				detail: `KVStore "ghost" is not declared in spec.kv`}),

			row("createWorkflowRun", "larger than workflow.payloadLimit", ask{what: "an input over the payload limit",
				method: http.MethodPost, path: runs, token: answersDev, body: input("huge", "wf", big),
				detail: "exceeds the payload limit 262144"}),
			row("createWorkflowRun", "does not match the input schema", ask{what: "an input off the contract",
				method: http.MethodPost, path: runs, token: answersDev, body: input("mismatch", "typed", `{}`),
				detail: `run input does not match workflow "typed" contract`}),
			row("replaceWorkflowRun", "larger than workflow.payloadLimit", ask{what: "an input over the payload limit",
				method: http.MethodPut, path: runs + "/run", token: answersDev, body: input("run", "wf", big),
				detail: "exceeds the payload limit 262144"}),
			row("replaceWorkflowRun", "does not match the input schema", ask{what: "an input off the contract",
				method: http.MethodPut, path: runs + "/run", token: answersDev, body: input("run", "typed", `{}`),
				detail: `run input does not match workflow "typed" contract`}),
			row("replaceWorkflowRun", "fixed once the run is created", ask{what: "another input",
				method: http.MethodPut, path: runs + "/run", token: answersDev, body: input("run", "wf", `{"x":1}`),
				detail: "a run's workflow, input and replay are fixed when it is created"}),

			row("getWorkflowRunLogs", "", ask{what: "the logs", method: http.MethodGet, path: logs, token: answersDev,
				detail: `"items"`}),
			row("getWorkflowRunLogs", "carries no bearer token", ask{what: "no credential", method: http.MethodGet,
				path: logs, detail: "missing bearer token"}),
			row("getWorkflowRunLogs", "do not allow it", ask{what: "another namespace's developer",
				method: http.MethodGet, path: logs, token: answersOther, detail: `read run logs in "default" denied`}),
			row("getWorkflowRunLogs", "No WorkflowRun of that name", ask{what: "a missing run", method: http.MethodGet,
				path: runs + "/nope/logs", token: answersDev, detail: `WorkflowRun "nope" not found`}),
			row("getWorkflowRunLogs", "The since parameter", ask{what: "a since of neither form", method: http.MethodGet,
				path: logs + "?since=yesterday", token: answersDev, detail: `since "yesterday" is not`}),
			row("getWorkflowRunLogs", "The severity parameter", ask{what: "an unknown severity", method: http.MethodGet,
				path: logs + "?severity=loud", token: answersDev, detail: `unknown severity "loud"`}),
			row("getWorkflowRunLogs", "names no step", ask{what: "a step the Workflow does not have",
				method: http.MethodGet, path: logs + "?step=nope", token: answersDev,
				detail: `step "nope" not found in workflow "wf"`}),
			row("getWorkflowRunLogs", "names a builtin or workflow step", ask{what: "a builtin step",
				method: http.MethodGet, path: logs + "?step=s", token: answersDev,
				detail: `step "s" is a builtin/sub-workflow step`}),
			row("getWorkflowRunLogs", "in the path is not", ask{what: "a name that is no DNS-1123 label",
				method: http.MethodGet, path: runs + "/Not_A_Label/logs", token: answersDev, detail: "path.name"}),
			row("getWorkflowRunLogs", "The limit parameter", ask{what: "a limit that is no integer",
				method: http.MethodGet, path: logs + "?limit=many", token: answersDev, detail: "query.limit"}),
			awayRow("getWorkflowRunLogs", e, http.MethodGet, "workflowruns/run/logs"),
		},
	)
	runMatrix(t, rows)
}
