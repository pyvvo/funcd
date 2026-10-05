package controlplane_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// racePairs is how many concurrent write pairs each race scenario runs (ADR-0147: 40 pairs released at once).
const racePairs = 40

type raceReader struct{ s store.Store }

func (r raceReader) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	res, err := r.s.List(ctx, gvk, store.ListOptions{Namespace: ns})
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}

// newRaceServer is the API over the memory store with the real link and quota admissions (quota 3); the dev
// token reaches one namespace per pair plus the sequential control's.
func newRaceServer(t *testing.T) (http.Handler, store.Store) {
	t.Helper()
	st := store.New(memory.New())
	var nss []v1.NamespaceName
	for i := range racePairs {
		nss = append(nss, raceNS(i))
	}
	nss = append(nss, "control")
	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: nss},
	})
	r := raceReader{st}
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       st,
		Authorizer:  rbac.New(),
		Credentials: creds,
		Admissions: []admission.Admission{
			admission.NewLinkValidityAdmission(r),
			admission.NewLinkDeletionProtectionAdmission(r),
			admission.NewKVStoreQuotaAdmission(r, 3),
			admission.NewBucketQuotaAdmission(r, 3),
		},
	})
	require.NoError(t, err)
	return h, st
}

func raceNS(i int) v1.NamespaceName { return v1.NamespaceName(fmt.Sprintf("race-%d", i)) }

func kindPath(ns v1.NamespaceName, plural string) string {
	return "/apis/funcd.io/v1alpha1/namespaces/" + string(ns) + "/" + plural
}

func linkedFunctionBody(t *testing.T, ns v1.NamespaceName, name, target string) []byte {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "app.handler", "oci://example/app:v1"
	if target != "" {
		fn.Spec.Links = []v1.FunctionLink{{Alias: "peer", Target: v1.ObjectName(target)}}
	}
	b, err := json.Marshal(fn)
	require.NoError(t, err)
	return b
}

func quotaBody(t *testing.T, kind v1.Kind, ns v1.NamespaceName, name string) []byte {
	t.Helper()
	obj, _ := v1.NewObject(kind)
	m := obj.GetObjectMeta()
	m.Name, m.Namespace, m.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	b, err := json.Marshal(obj)
	require.NoError(t, err)
	return b
}

type call struct {
	method, path string
	body         []byte
}

// race runs each pair's two calls at once, every pair released together on a closed channel.
func race(t *testing.T, srv http.Handler, pairs [][2]call) [][2]*httptest.ResponseRecorder {
	t.Helper()
	out := make([][2]*httptest.ResponseRecorder, len(pairs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, p := range pairs {
		for j := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				out[i][j] = do(t, srv, p[j].method, p[j].path, devToken, p[j].body)
			}()
		}
	}
	close(start)
	wg.Wait()
	return out
}

func mustDo(t *testing.T, srv http.Handler, c call) {
	t.Helper()
	rec := do(t, srv, c.method, c.path, devToken, c.body)
	require.Less(t, rec.Code, 300, "%s %s: %s", c.method, c.path, rec.Body.String())
}

func problemOf(t *testing.T, rec *httptest.ResponseRecorder) (typ, detail string) {
	t.Helper()
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p), rec.Body.String())
	return p.Type, p.Detail
}

func linksOf(t *testing.T, st store.Store, ns v1.NamespaceName) map[v1.ObjectName][]v1.ObjectName {
	t.Helper()
	res, err := st.List(context.Background(), v1.KindFunction.GVK(), store.ListOptions{Namespace: ns})
	require.NoError(t, err)
	out := map[v1.ObjectName][]v1.ObjectName{}
	for _, o := range res.Items {
		f := o.(*v1.Function)
		out[f.Name] = nil
		for _, l := range f.Spec.Links {
			out[f.Name] = append(out[f.Name], l.Target)
		}
	}
	return out
}

// scenario: link-cycle-race-rejected — PUT a → b and PUT b → a at once: exactly one succeeds, the other fails
// Invalid "would create a dependency cycle", as the sequential control does; no pair stores a cycle.
func TestScenarioLinkCycleRaceRejected(t *testing.T) {
	srv, st := newRaceServer(t)
	cycle := func(ns v1.NamespaceName) [2]call {
		fns := kindPath(ns, "functions")
		mustDo(t, srv, call{http.MethodPost, fns, linkedFunctionBody(t, ns, "a", "")})
		mustDo(t, srv, call{http.MethodPost, fns, linkedFunctionBody(t, ns, "b", "")})
		return [2]call{
			{http.MethodPut, fns + "/a", linkedFunctionBody(t, ns, "a", "b")},
			{http.MethodPut, fns + "/b", linkedFunctionBody(t, ns, "b", "a")},
		}
	}

	control := cycle("control")
	mustDo(t, srv, control[0])
	rec := do(t, srv, control[1].method, control[1].path, devToken, control[1].body)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	typ, detail := problemOf(t, rec)
	require.Equal(t, "urn:funcd:problem:invalid", typ)
	require.Contains(t, detail, "would create a dependency cycle")

	pairs := make([][2]call, racePairs)
	for i := range racePairs {
		pairs[i] = cycle(raceNS(i))
	}
	for i, recs := range race(t, srv, pairs) {
		ok, rejected := 0, 0
		for _, rec := range recs {
			if rec.Code < 300 {
				ok++
				continue
			}
			rejected++
			typ, detail := problemOf(t, rec)
			assert.Equal(t, "urn:funcd:problem:invalid", typ, "pair %d", i)
			assert.Contains(t, detail, "would create a dependency cycle", "pair %d", i)
		}
		assert.Equal(t, [2]int{1, 1}, [2]int{ok, rejected}, "pair %d: exactly one link write succeeds", i)
		links := linksOf(t, st, raceNS(i))
		assert.False(t, len(links["a"]) > 0 && len(links["b"]) > 0, "pair %d stored a cycle: %v", i, links)
	}
}

// scenario: dangling-link-race-rejected — PUT a → b and DELETE b at once: the link is stored and the delete
// fails Conflict, or the delete succeeds and the link fails Invalid; no pair stores a link to a missing Function.
func TestScenarioDanglingLinkRaceRejected(t *testing.T) {
	srv, st := newRaceServer(t)
	dangle := func(ns v1.NamespaceName) [2]call {
		fns := kindPath(ns, "functions")
		mustDo(t, srv, call{http.MethodPost, fns, linkedFunctionBody(t, ns, "a", "")})
		mustDo(t, srv, call{http.MethodPost, fns, linkedFunctionBody(t, ns, "b", "")})
		return [2]call{
			{http.MethodPut, fns + "/a", linkedFunctionBody(t, ns, "a", "b")},
			{http.MethodDelete, fns + "/b", nil},
		}
	}

	// Sequential controls, in both orders.
	control := dangle("control")
	mustDo(t, srv, control[0])
	rec := do(t, srv, control[1].method, control[1].path, devToken, nil)
	require.Equal(t, http.StatusConflict, rec.Code, "link first: the delete fails: %s", rec.Body.String())
	mustDo(t, srv, call{http.MethodPut, control[0].path, linkedFunctionBody(t, "control", "a", "")})
	mustDo(t, srv, control[1])
	rec = do(t, srv, control[0].method, control[0].path, devToken, control[0].body)
	require.Equal(t, http.StatusBadRequest, rec.Code, "delete first: the link fails: %s", rec.Body.String())
	_, detail := problemOf(t, rec)
	require.Contains(t, detail, "does not exist")

	pairs := make([][2]call, racePairs)
	for i := range racePairs {
		pairs[i] = dangle(raceNS(i))
	}
	for i, recs := range race(t, srv, pairs) {
		link, del := recs[0], recs[1]
		switch {
		case link.Code < 300:
			assert.Equal(t, http.StatusConflict, del.Code, "pair %d: link stored, so the delete fails: %s", i, del.Body.String())
		case del.Code < 300:
			assert.Equal(t, http.StatusBadRequest, link.Code, "pair %d: b deleted, so the link fails", i)
			_, detail := problemOf(t, link)
			assert.Contains(t, detail, "does not exist", "pair %d", i)
		default:
			t.Errorf("pair %d: both failed: link %d %s, delete %d %s", i, link.Code, link.Body.String(), del.Code, del.Body.String())
		}
		links := linksOf(t, st, raceNS(i))
		for from, targets := range links {
			for _, to := range targets {
				_, exists := links[to]
				assert.True(t, exists, "pair %d: %s links the missing %s", i, from, to)
			}
		}
	}
}

// scenario: quota-race-rejected — quota 3 with 2 KVStores (separately 2 Buckets) per namespace, two creates
// at once: one fails Invalid "already holds the maximum 3", as the sequential control does; none holds more than 3.
func TestScenarioQuotaRaceRejected(t *testing.T) {
	for _, tc := range []struct {
		kind   v1.Kind
		plural string
	}{{v1.KindKVStore, "kvstores"}, {v1.KindBucket, "buckets"}} {
		t.Run(string(tc.kind), func(t *testing.T) {
			srv, st := newRaceServer(t)
			fill := func(ns v1.NamespaceName) [2]call {
				p := kindPath(ns, tc.plural)
				mustDo(t, srv, call{http.MethodPost, p, quotaBody(t, tc.kind, ns, "s1")})
				mustDo(t, srv, call{http.MethodPost, p, quotaBody(t, tc.kind, ns, "s2")})
				return [2]call{
					{http.MethodPost, p, quotaBody(t, tc.kind, ns, "s3")},
					{http.MethodPost, p, quotaBody(t, tc.kind, ns, "s4")},
				}
			}

			control := fill("control")
			mustDo(t, srv, control[0])
			rec := do(t, srv, control[1].method, control[1].path, devToken, control[1].body)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			_, detail := problemOf(t, rec)
			require.Contains(t, detail, "already holds the maximum 3")

			pairs := make([][2]call, racePairs)
			for i := range racePairs {
				pairs[i] = fill(raceNS(i))
			}
			for i, recs := range race(t, srv, pairs) {
				ok := 0
				for _, rec := range recs {
					if rec.Code < 300 {
						ok++
						continue
					}
					assert.Equal(t, http.StatusBadRequest, rec.Code, "pair %d", i)
					_, detail := problemOf(t, rec)
					assert.Contains(t, detail, "already holds the maximum 3", "pair %d", i)
				}
				assert.Equal(t, 1, ok, "pair %d: exactly one create succeeds", i)
				res, err := st.List(context.Background(), tc.kind.GVK(), store.ListOptions{Namespace: raceNS(i)})
				require.NoError(t, err)
				assert.LessOrEqual(t, len(res.Items), 3, "pair %d", i)
			}
		})
	}
}
