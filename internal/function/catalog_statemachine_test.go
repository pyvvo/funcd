package function

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/commands"
	"github.com/leanovate/gopter/gen"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	cataloggw "github.com/pyvvo/funcd/internal/catalog/gateway"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/provider"
	catalogsvc "github.com/pyvvo/funcd/internal/services/catalog"
)

const (
	numConsumers     = 2
	sharedQuackToken = "shared-engine-token"
)

// TestCatalogPathStateMachine is a stateful property test of the catalog path (ADR-0137). gopter runs random
// sequences of engine crashes, engine readiness, catalog reconciles and consumer provisioning against the
// real catalog reconciler, the real PEP proxy Manager and the real resolveCatalogEnv, over one store; only
// the engine is faked. After every step it checks that:
//   - a consumer is provisioned only while the catalog is Ready, and never with an engine address;
//   - the catalog's Ready endpoint never changes, and every consumer holds it;
//   - once the proxy targets the current engine, every consumer's query reaches that engine with the shared
//     engine token swapped in.
//
// Out of scope until their board cards are fixed: QUACK_TOKEN rotation, which breaks queries today, and
// restarting a crashed engine with no CatalogService change, which this model hides because any step may
// reconcile.
//
// A failure prints the shrunk command sequence and the seed that reproduces it.
func TestCatalogPathStateMachine(t *testing.T) {
	t.Parallel()
	params := gopter.DefaultTestParameters()
	params.MaxSize = 40
	props := gopter.NewProperties(params)
	props.Property("consumers keep working across engine moves", commands.Prop(catalogCommands(t)))
	props.TestingRun(t)
}

// catalogModel is the expected state after each command.
type catalogModel struct {
	engine       int  // the current engine generation
	engineReady  bool // the provider reports the current engine Ready
	catalogReady bool // the stored CatalogService is Ready
	proxyEngine  int  // the generation the proxy forwards to; -1 before the first Ready reconcile
	provisioned  [numConsumers]bool
}

func catalogCommands(t *testing.T) *commands.ProtoCommands {
	reconcile := &commands.ProtoCommand{
		Name:    "ReconcileCatalog",
		RunFunc: func(s commands.SystemUnderTest) commands.Result { return s.(*catalogWorld).reconcile() },
		NextStateFunc: func(s commands.State) commands.State {
			m := s.(catalogModel)
			m.catalogReady = m.engineReady
			if m.engineReady {
				m.proxyEngine = m.engine
			}
			return m
		},
		PostConditionFunc: checkCatalogWorld,
	}
	engineReady := &commands.ProtoCommand{
		Name:             "EngineReady",
		RunFunc:          func(s commands.SystemUnderTest) commands.Result { return s.(*catalogWorld).engineReady() },
		PreConditionFunc: func(s commands.State) bool { return !s.(catalogModel).engineReady },
		NextStateFunc: func(s commands.State) commands.State {
			m := s.(catalogModel)
			m.engineReady = true
			return m
		},
		PostConditionFunc: checkCatalogWorld,
	}
	engineCrash := &commands.ProtoCommand{
		Name:    "EngineCrash",
		RunFunc: func(s commands.SystemUnderTest) commands.Result { return s.(*catalogWorld).engineCrash() },
		NextStateFunc: func(s commands.State) commands.State {
			m := s.(catalogModel)
			m.engine++
			m.engineReady = false
			return m
		},
		PostConditionFunc: checkCatalogWorld,
	}
	cmds := []gopter.Gen{gen.Const(reconcile), gen.Const(engineReady), gen.Const(engineCrash)}
	for i := range numConsumers {
		cmds = append(cmds, gen.Const(&commands.ProtoCommand{
			Name:    fmt.Sprintf("Provision(reader-%d)", i),
			RunFunc: func(s commands.SystemUnderTest) commands.Result { return s.(*catalogWorld).provision(i) },
			NextStateFunc: func(s commands.State) commands.State {
				m := s.(catalogModel)
				if m.catalogReady {
					m.provisioned[i] = true
				}
				return m
			},
			PostConditionFunc: checkCatalogWorld,
		}))
	}
	return &commands.ProtoCommands{
		NewSystemUnderTestFunc:     func(commands.State) commands.SystemUnderTest { return newCatalogWorld(t) },
		DestroySystemUnderTestFunc: func(s commands.SystemUnderTest) { s.(*catalogWorld).close() },
		InitialStateGen:            gen.Const(catalogModel{proxyEngine: -1}),
		GenCommandFunc:             func(commands.State) gopter.Gen { return gen.OneGenOf(cmds...) },
	}
}

// observation is what the harness reads back after a command: the stored catalog status and each
// consumer's injected URL. query sends one request through consumer i as its worker would; the postcondition
// calls it only where the model says the request must reach the engine.
type observation struct {
	err       error
	phase     v1.Phase
	endpoint  string
	readyURL  string // the first endpoint the catalog published as Ready
	provision int    // the consumer a Provision command resolved, else -1
	requeued  bool
	consumers [numConsumers]consumerView
	query     func(i int) queryResult
}

type consumerView struct {
	provisioned bool
	url         string
	engineAddr  bool // the URL is an engine address, not the proxy
}

type queryResult struct {
	err        error
	status     int
	generation string // the engine generation that answered
	token      string // the token that engine received
}

func checkCatalogWorld(state commands.State, result commands.Result) *gopter.PropResult {
	m, o := state.(catalogModel), result.(observation)
	fail := func(format string, args ...any) *gopter.PropResult {
		return gopter.NewPropResult(false, fmt.Sprintf(format, args...))
	}
	switch {
	case o.err != nil:
		return fail("step failed: %v", o.err)
	case (o.phase == v1.PhaseReady) != m.catalogReady:
		return fail("catalog phase %q, model Ready=%t", o.phase, m.catalogReady)
	case o.phase == v1.PhaseReady && o.endpoint != o.readyURL:
		return fail("the Ready endpoint moved from %s to %s", o.readyURL, o.endpoint)
	case o.provision >= 0 && o.requeued == m.catalogReady:
		return fail("reader-%d: requeued=%t while catalog Ready=%t", o.provision, o.requeued, m.catalogReady)
	}
	for i, c := range o.consumers {
		switch {
		case c.provisioned != m.provisioned[i]:
			return fail("reader-%d: provisioned=%t, model %t", i, c.provisioned, m.provisioned[i])
		case !c.provisioned:
			continue
		case c.engineAddr || c.url != o.readyURL:
			return fail("reader-%d holds %s, not the proxy URL %s", i, c.url, o.readyURL)
		case m.proxyEngine != m.engine:
			continue // the proxy still targets a crashed engine until the next Ready reconcile
		}
		if q := o.query(i); q.status != http.StatusOK || q.generation != strconv.Itoa(m.engine) || q.token != sharedQuackToken {
			return fail("reader-%d via %s: status %d err %v from engine %q with token %q, want 200 from engine %d with the shared token",
				i, c.url, q.status, q.err, q.generation, q.token, m.engine)
		}
	}
	return gopter.NewPropResult(true, "")
}

// catalogWorld is the system under test.
type catalogWorld struct {
	fn          *Reconciler
	catalog     *catalogsvc.Reconciler
	proxies     *cataloggw.Manager
	engine      *engineProvider
	client      *http.Client
	consumers   [numConsumers]map[string]string // the catalog env each consumer's workers started with
	readyURL    string
	engineAddrs map[string]bool
}

func newCatalogWorld(t *testing.T) *catalogWorld {
	master := []byte("catalog-state-machine-master")
	fn := newShimReconciler(t, fakeResolver{})
	fn.catalogMaster = master
	seedCatalogServiceIn(t, fn, "lake", "", "")
	bucket := &v1.Bucket{}
	bucket.TypeMeta = v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lakehouse", "default", "rg1"
	bucket.Spec.Prefixes = []v1.BucketPrefix{{Name: "gold", Owner: "lake"}}
	_, err := fn.store.Create(context.Background(), bucket)
	require.NoError(t, err)

	engine := &engineProvider{}
	engine.start()
	proxies := cataloggw.NewManager("", "", cataloggw.NewCatalogKeys(master, fn.store), allowAll{}, nil)
	catalog, err := catalogsvc.NewReconciler(catalogsvc.ReconcilerDeps{
		Store:    fn.store,
		Provider: engine,
		Secrets:  fakeResolver{env: map[string]string{"QUACK_TOKEN": sharedQuackToken}},
		Proxy:    proxies,
	})
	require.NoError(t, err)
	return &catalogWorld{
		fn:          fn,
		catalog:     catalog,
		proxies:     proxies,
		engine:      engine,
		client:      &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}},
		engineAddrs: map[string]bool{engine.addr(): true},
	}
}

func (w *catalogWorld) close() {
	w.proxies.Shutdown()
	w.engine.srv.Close()
}

func (w *catalogWorld) reconcile() observation {
	_, err := w.catalog.Reconcile(context.Background(), controller.Request{GVK: v1.KindCatalogService.GVK(), Namespace: "default", Name: "lake"})
	o := w.observe()
	o.err = errors.Join(err, o.err)
	return o
}

func (w *catalogWorld) engineReady() observation {
	w.engine.ready = true
	return w.observe()
}

func (w *catalogWorld) engineCrash() observation {
	w.engine.crash()
	w.engineAddrs[w.engine.addr()] = true
	return w.observe()
}

// provision resolves a consumer's catalog env as its Function reconcile would; on success its workers start
// with it. A requeue leaves the running workers, and their env, as they were.
func (w *catalogWorld) provision(i int) observation {
	fn := catalogConsumerFn("lake", "lake")
	fn.Name = v1.ObjectName(fmt.Sprintf("reader-%d", i))
	env, requeue, err := w.fn.resolveCatalogEnv(context.Background(), fn)
	if err == nil && !requeue {
		w.consumers[i] = env
	}
	o := w.observe()
	o.err = errors.Join(err, o.err)
	o.provision, o.requeued = i, requeue
	return o
}

func (w *catalogWorld) observe() observation {
	o := observation{provision: -1, query: w.query}
	obj, err := w.fn.store.Get(context.Background(), v1.KindCatalogService.GVK(), "default", "lake")
	if err != nil {
		o.err = err
		return o
	}
	cs := obj.(*v1.CatalogService)
	o.phase, o.endpoint = cs.Status.Phase, cs.Status.Endpoint
	if o.phase == v1.PhaseReady && w.readyURL == "" {
		w.readyURL = o.endpoint
	}
	o.readyURL = w.readyURL
	for i, env := range w.consumers {
		if url := env["FUNCD_CATALOG_LAKE_URL"]; env != nil {
			o.consumers[i] = consumerView{provisioned: true, url: url, engineAddr: w.engineAddrs[url]}
		}
	}
	return o
}

// query sends one Quack handshake through consumer i's injected URL with its injected token.
func (w *catalogWorld) query(i int) queryResult {
	env := w.consumers[i]
	resp, err := w.client.Post("http://"+env["FUNCD_CATALOG_LAKE_URL"], "application/octet-stream", bytes.NewReader(quackHandshake(env["FUNCD_CATALOG_LAKE_TOKEN"])))
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
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Engine-Generation", generation)
		w.Header().Set("X-Engine-Token", quackHandshakeToken(body))
	}))
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

// allowAll grants every catalog::query: authorization has its own tests; this one is about routing.
type allowAll struct{}

func (allowAll) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: true}, nil
}
