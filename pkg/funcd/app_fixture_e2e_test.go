//go:build e2e

package funcd_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// appWithin is how long the fixture App todo takes at most to become Ready (ADR-0199 scenario app-install).
const appWithin = 30 * time.Second

// todoHost is the host the fixture's Route todo-api answers on.
const todoHost = "todo.example.com"

// todoApp is the fixture App todo of ADR-0199's Scenarios. todo-api and the step due run e's echo image; todo-cache
// has a table only because a Function binds a store through one of its tables.
func todoApp(t *testing.T, e *gcEnv) *v1.App {
	t.Helper()
	img := e.image(t)
	return &v1.App{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: "todo", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.AppSpec{
			KV: []v1.AppKVStore{
				{Name: "todo-store", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "todos", Owner: "todo-api"}}}},
				{Name: "todo-cache", Deletion: v1.DeletionDelete, KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "entries"}}}},
			},
			Buckets: []v1.AppBucket{
				{Name: "todo-files", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "attachments", Owner: "todo-api"}}}},
				{Name: "todo-tmp", Deletion: v1.DeletionDelete},
			},
			Functions: []v1.AppFunction{{Name: "todo-api", FunctionSpec: v1.FunctionSpec{
				Runtime: "nodejs22",
				Handler: "handle",
				Image:   img,
				KV: []v1.FunctionKV{
					{Alias: "store", Store: "todo-store", Table: "todos"},
					{Alias: "cache", Store: "todo-cache", Table: "entries"},
				},
				Blob:    []v1.FunctionBlob{{Alias: "files", Bucket: "todo-files", Prefix: "attachments"}},
				Scaling: v1.Scaling{MinReplicas: 1},
			}}},
			Workflows: []v1.AppWorkflow{{Name: "todo-plan", WorkflowSpec: v1.WorkflowSpec{
				Steps: []v1.WorkflowStep{{Name: "due", Function: &v1.FunctionStep{Image: img}}},
			}}},
			Routes: []v1.AppRoute{{Name: "todo-api", RouteSpec: v1.RouteSpec{
				Host:  todoHost,
				Rules: []v1.RouteRule{{Path: "/api", Backend: v1.RouteBackend{Function: "todo-api"}}},
			}}},
		},
	}
}

// todoPart is one part of the fixture App todo.
type todoPart struct {
	kind v1.Kind
	name string
}

// todoParts are the fixture's seven parts in section order.
func todoParts() []todoPart {
	return []todoPart{
		{v1.KindKVStore, "todo-store"}, {v1.KindKVStore, "todo-cache"},
		{v1.KindBucket, "todo-files"}, {v1.KindBucket, "todo-tmp"},
		{v1.KindFunction, "todo-api"}, {v1.KindWorkflow, "todo-plan"}, {v1.KindRoute, "todo-api"},
	}
}

func (p todoPart) store() bool { return p.kind == v1.KindKVStore || p.kind == v1.KindBucket }

// retained reports whether p is one of the fixture's stores with the default deletion, retain: it carries only the
// App's marker.
func (p todoPart) retained() bool { return p.store() && p.name != "todo-cache" && p.name != "todo-tmp" }

func (e *gcEnv) object(t *testing.T, kind v1.Kind, name string) v1.Object {
	t.Helper()
	obj, err := e.c.Get(e.ctx, kind, "default", v1.ObjectName(name))
	require.NoError(t, err, "get %s/%s", kind, name)
	return obj
}

func (e *gcEnv) app(t *testing.T, name string) *v1.App {
	t.Helper()
	return e.object(t, v1.KindApp, name).(*v1.App)
}

// waitApp waits until the App's Ready condition has status and, when reason is set, reason at the App's generation,
// and returns the App.
func (e *gcEnv) waitApp(t *testing.T, name string, status v1.ConditionStatus, reason string, within time.Duration) *v1.App {
	t.Helper()
	var a *v1.App
	require.Eventually(t, func() bool {
		a = e.app(t, name)
		c, ok := a.Status.Conditions.Get("Ready")
		return ok && c.ObservedGeneration == a.Generation && c.Status == status && (reason == "" || c.Reason == reason)
	}, within, 50*time.Millisecond, "App/%s is Ready=%s %s", name, status, reason)
	return a
}

func readyCondition(a *v1.App) v1.Condition {
	c, _ := a.Status.Conditions.Get("Ready")
	return c
}

// appRefs reports whether obj carries a's marker (a non-controller reference) and a's controller reference, both
// naming a's UID (ADR-0199 Decision 7).
func appRefs(obj v1.Object, a *v1.App) (marked, controlled bool) {
	for _, r := range obj.GetObjectMeta().OwnerReferences {
		if r.Kind != v1.KindApp || r.Name != a.Name || r.UID != a.UID {
			continue
		}
		if r.Controller {
			controlled = true
		} else {
			marked = true
		}
	}
	return marked, controlled
}

// routed calls the data plane on host and path, as a client of a Route does, and returns the status code.
func (e *gcEnv) routed(t *testing.T, host, path string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, e.dp+path, strings.NewReader(`{"data":{}}`))
	require.NoError(t, err)
	req.Host = host
	return e.do(t, req)
}

// versions reads the resourceVersion of each part.
func (e *gcEnv) versions(t *testing.T, parts []todoPart) map[todoPart]string {
	t.Helper()
	out := make(map[todoPart]string, len(parts))
	for _, p := range parts {
		out[p] = e.object(t, p.kind, p.name).GetObjectMeta().ResourceVersion
	}
	return out
}
