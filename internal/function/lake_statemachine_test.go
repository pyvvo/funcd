package function

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controller"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	"github.com/pyvvo/funcd/internal/provider"
	lakesvc "github.com/pyvvo/funcd/internal/services/lake"
)

const (
	numConsumers     = 2
	sharedQuackToken = "shared-engine-token"
)

// TestLakePathStateMachine is a stateful property test of the lake path (ADR-0137). rapid runs random
// sequences of engine crashes, engine readiness, lake reconciles and consumer provisioning against the
// real lake reconciler, the real PEP proxy Manager and the real resolveLakeEnv, over one store; only
// the engine is faked. After every action it checks that:
//   - a consumer is provisioned only while the lake is Ready;
//   - the lake's Ready endpoint never changes, and every consumer holds it;
//   - once the proxy targets the current engine, every consumer's query reaches that engine with the shared
//     engine token swapped in.
//
// Out of scope until its board card is fixed: QUACK_TOKEN rotation, which breaks queries today. Reconciles are
// free actions here; that one follows a crash is ADR-0142's periodic requeue, covered by the lake reconciler's
// TestScenarioCrashedLakeEngineRestarts and the duckdb lane.
//
// A failure prints the minimized action sequence and saves it under testdata/rapid; later runs replay it.
func TestLakePathStateMachine(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		w := newLakeWorld(t)
		rt.Cleanup(w.close)
		m := lakeModel{proxyEngine: -1}
		rt.Repeat(map[string]func(*rapid.T){
			"": func(rt *rapid.T) { w.check(rt, m) },
			"ReconcileLake": func(rt *rapid.T) {
				w.reconcile(rt)
				m.lakeReady = m.engineReady
				if m.engineReady {
					m.proxyEngine = m.engine
				}
			},
			"EngineReady": func(rt *rapid.T) {
				if m.engineReady {
					rt.Skip("the engine is already Ready")
				}
				w.engine.ready = true
				m.engineReady = true
			},
			"EngineCrash": func(*rapid.T) {
				w.engine.crash()
				m.engine++
				m.engineReady = false
			},
			"Provision": func(rt *rapid.T) {
				i := rapid.IntRange(0, numConsumers-1).Draw(rt, "consumer")
				if requeued := w.provision(rt, i); requeued == m.lakeReady {
					rt.Fatalf("reader-%d: requeued=%t while lake Ready=%t", i, requeued, m.lakeReady)
				}
				if m.lakeReady {
					m.provisioned[i] = true
				}
			},
		})
	})
}

// lakeModel is the expected state.
type lakeModel struct {
	engine      int  // the current engine generation
	engineReady bool // the provider reports the current engine Ready
	lakeReady   bool // the stored Lake is Ready
	proxyEngine int  // the generation the proxy forwards to; -1 before the first Ready reconcile
	provisioned [numConsumers]bool
}

// lakeWorld is the system under test.
type lakeWorld struct {
	fn        *Reconciler
	lake      *lakesvc.Reconciler
	proxies   *lakegw.Manager
	engine    *engineProvider
	client    *http.Client
	consumers [numConsumers]map[string]string // the lake env each consumer's workers started with
	readyURL  string                          // the first endpoint the lake published as Ready
}

func newLakeWorld(t *testing.T) *lakeWorld {
	master := []byte("lake-state-machine-master")
	fn := newShimReconciler(t, fakeResolver{})
	fn.lakeMaster = master
	seedLakeIn(t, fn, "lake", "", "")
	bucket := &v1.Bucket{}
	bucket.TypeMeta = v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lakehouse", "default", "rg1"
	bucket.Spec.Prefixes = []v1.BucketPrefix{{Name: "gold", Owner: "lake"}}
	_, err := fn.store.Create(context.Background(), bucket)
	require.NoError(t, err)

	engine := &engineProvider{}
	engine.start()
	proxies := lakegw.NewManager("", lakegw.NewLakeKeys(master, fn.store), allowAll{}, nil)
	lake, err := lakesvc.NewReconciler(lakesvc.ReconcilerDeps{
		Store:    fn.store,
		Provider: engine,
		Secrets:  fakeResolver{env: map[string]string{"QUACK_TOKEN": sharedQuackToken}},
		Proxy:    proxies,
	})
	require.NoError(t, err)
	return &lakeWorld{
		fn:      fn,
		lake:    lake,
		proxies: proxies,
		engine:  engine,
		client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
				if err == nil {
					resetOnClose(c)
				}
				return c, err
			},
		}},
	}
}

func (w *lakeWorld) close() {
	w.client.CloseIdleConnections()
	w.proxies.Shutdown()
	w.engine.srv.Close()
}

// resetOnClose makes a closed TCP connection send RST, so it never parks a port in TIME_WAIT: rapid builds
// thousands of worlds while it minimizes a failure, and parked ports exhaust the ephemeral range.
func resetOnClose(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
}

// check asserts the invariants against the model. It queries a consumer only where the model says the
// request must reach the current engine. Failure messages carry no ports: rapid minimizes a case only while
// its re-runs fail with the same message.
func (w *lakeWorld) check(rt *rapid.T, m lakeModel) {
	obj, err := w.fn.store.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
	require.NoError(rt, err)
	cs := obj.(*v1.Lake)
	ready := cs.Status.Phase == v1.PhaseReady
	if ready && w.readyURL == "" {
		w.readyURL = cs.Status.Endpoint
	}
	switch {
	case ready != m.lakeReady:
		rt.Fatalf("lake phase %q, model Ready=%t", cs.Status.Phase, m.lakeReady)
	case ready && cs.Status.Endpoint != w.readyURL:
		rt.Logf("first Ready endpoint %s, now %s", w.readyURL, cs.Status.Endpoint)
		rt.Fatalf("the Ready endpoint moved")
	}
	for i, env := range w.consumers {
		url := env["FUNCD_LAKE_LAKE_URL"]
		switch {
		case (env != nil) != m.provisioned[i]:
			rt.Fatalf("reader-%d: provisioned=%t, model %t", i, env != nil, m.provisioned[i])
		case env == nil:
			continue
		case url != w.readyURL:
			rt.Logf("reader-%d holds %s, the Ready endpoint is %s", i, url, w.readyURL)
			rt.Fatalf("reader-%d does not hold the Ready endpoint", i)
		case m.proxyEngine != m.engine:
			continue // the proxy still targets a crashed engine until the next Ready reconcile
		}
		if q := w.query(i); q.status != http.StatusOK || q.generation != strconv.Itoa(m.engine) || q.token != sharedQuackToken {
			rt.Logf("reader-%d queried %s: %v", i, url, q.err)
			rt.Fatalf("reader-%d: status %d from engine %q with token %q, want 200 from engine %d with the shared token",
				i, q.status, q.generation, q.token, m.engine)
		}
	}
}

func (w *lakeWorld) reconcile(rt *rapid.T) {
	_, err := w.lake.Reconcile(context.Background(), controller.Request{GVK: v1.KindLake.GVK(), Namespace: "default", Name: "lake"})
	require.NoError(rt, err)
}

// provision resolves a consumer's lake env as its Function reconcile would; on success its workers start
// with it. A requeue leaves the running workers, and their env, as they were.
func (w *lakeWorld) provision(rt *rapid.T, i int) (requeued bool) {
	fn := lakeConsumerFn("lake", "lake")
	fn.Name = v1.ObjectName(fmt.Sprintf("reader-%d", i))
	env, requeue, err := w.fn.resolveLakeEnv(context.Background(), fn)
	require.NoError(rt, err)
	if !requeue {
		w.consumers[i] = env
	}
	return requeue
}

type queryResult struct {
	err        error
	status     int
	generation string // the engine generation that answered
	token      string // the token that engine received
}

// query sends one Quack handshake through consumer i's injected URL with its injected token.
func (w *lakeWorld) query(i int) queryResult {
	env := w.consumers[i]
	resp, err := w.client.Post("http://"+env["FUNCD_LAKE_LAKE_URL"], "application/octet-stream", bytes.NewReader(quackHandshake(env["FUNCD_LAKE_LAKE_TOKEN"])))
	if err != nil {
		return queryResult{err: err}
	}
	_ = resp.Body.Close()
	return queryResult{status: resp.StatusCode, generation: resp.Header.Get("X-Engine-Generation"), token: resp.Header.Get("X-Engine-Token")}
}

// engineProvider is the provider.Runtime fake. A crash closes the engine and starts the next generation on
// a new address, not Ready yet, as the provider does when it re-converges a crashed engine.
type engineProvider struct {
	generation int
	ready      bool
	srv        *httptest.Server
}

func (p *engineProvider) Converge(context.Context, provider.ProviderSpec) (provider.ProviderStatus, error) {
	return provider.ProviderStatus{Running: 1, Ready: p.ready, Address: p.addr()}, nil
}

func (p *engineProvider) Teardown(context.Context, provider.ProviderRef) error { return nil }

func (p *engineProvider) addr() string { return p.srv.Listener.Addr().String() }

func (p *engineProvider) start() {
	generation := strconv.Itoa(p.generation)
	p.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Engine-Generation", generation)
		w.Header().Set("X-Engine-Token", quackHandshakeToken(body))
	}))
	p.srv.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			resetOnClose(c)
		}
	}
	p.srv.Start()
}

func (p *engineProvider) crash() {
	p.srv.Close()
	p.generation++
	p.ready = false
	p.start()
}

// quackHandshake is a minimal Quack handshake: an 8-byte preamble, then the token field (id 0x01, 0x00, a
// uvarint length, the token), the frame the proxy swaps the token in.
func quackHandshake(token string) []byte {
	b := append(make([]byte, 8), 0x01, 0x00)
	b = binary.AppendUvarint(b, uint64(len(token)))
	return append(b, token...)
}

func quackHandshakeToken(body []byte) string {
	if len(body) < 10 {
		return ""
	}
	n, k := binary.Uvarint(body[10:])
	if k <= 0 || 10+k+int(n) > len(body) {
		return ""
	}
	return string(body[10+k : 10+k+int(n)])
}

// allowAll grants every lake::query: authorization has its own tests; this one is about routing.
type allowAll struct{}

func (allowAll) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: true}, nil
}
