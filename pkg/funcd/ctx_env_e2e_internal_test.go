//go:build e2e

package funcd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// ctxEnvHandler returns the bound variables as the worker's environment holds them, and what the handler reads
// through ctx.
const ctxEnvHandler = `const vars = ['APP_MODE', 'API_KEY', 'FUNCD_CONFIG_KEYS', 'FUNCD_SECRET_KEYS',
  'FUNCD_CATALOG_LAKE_URL', 'FUNCD_CATALOG_LAKE_TOKEN'];
export async function handle(ctx) {
  return {
    env: Object.fromEntries(vars.map((k) => [k, process.env[k]])),
    config: ctx.config.list(), mode: ctx.config.get('APP_MODE'),
    secrets: await ctx.secret.list(), key: await ctx.secret.get('API_KEY'),
    lake: ctx.catalog.get('lake'),
  };
}
`

// scenario: ctx-env-unchanged — a real worker's environment holds its bound config keys and its catalog pair as
// variables, plus FUNCD_CONFIG_KEYS, and its handler reads the same values through ctx (ADR-0245). Its bound secrets
// and FUNCD_SECRET_KEYS are not in it: ctx.secret reads them from funcd (ADR-0249).
func TestScenarioCtxEnvUnchanged(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	engine := &toggleEngine{addr: strings.TrimPrefix(srv.URL, "http://")}
	p, err := New(InMemory(), WithoutLogCompaction(), WithRuntimeShim(node, langmod.NodeShim(t)),
		WithCatalogProviderRuntime(engine), WithPacing(asleepPacing(50*time.Millisecond)))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	r := &asleepRig{p: p, c: c, engine: engine}
	r.catalog(t)
	r.secret(t)
	obj, _ := v1.NewObject(v1.KindConfigMap)
	cm := obj.(*v1.ConfigMap)
	cm.Name, cm.Namespace, cm.ResourceGroup = "settings", "default", "rg1"
	cm.Spec.Data = map[string]string{"APP_MODE": "prod"}
	r.apply(t, cm)

	art := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(art, []byte(ctxEnvHandler), 0o600))
	obj, _ = v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "reader", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+art
	fn.Spec.Replicas = 1
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	fn.Spec.Bindings.Config = []v1.ObjectName{"settings"}
	fn.Spec.Bindings.Secret = []v1.ObjectName{"creds"}
	fn.Spec.Bindings.Catalog = []v1.FunctionCatalog{{Alias: "lake", Catalog: "lake"}}
	fn.Spec.ConfigMaps = []v1.ConfigMapEntry{{Ref: "settings"}}
	fn.Spec.SecretDeclarations = []v1.SecretDeclaration{{Name: "creds", Keys: []string{"API_KEY"}}}
	fn.Spec.CatalogServices = []v1.CatalogEntry{{Ref: "lake"}}
	r.apply(t, fn)
	require.Eventually(t, func() bool {
		obj, err := p.cfg.store.Get(context.Background(), v1.KindFunction.GVK(), "default", "reader")
		return err == nil && obj.(*v1.Function).Status.Phase == v1.PhaseReady
	}, 15*time.Second, 50*time.Millisecond, "reader is Ready")

	resp, err := http.Post("http://"+p.DataPlaneAddr()+"/function/reader", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var out struct {
		Env     map[string]string `json:"env"`
		Config  []string          `json:"config"`
		Mode    string            `json:"mode"`
		Secrets []string          `json:"secrets"`
		Key     string            `json:"key"`
		Lake    map[string]string `json:"lake"`
	}
	require.NoError(t, json.Unmarshal(body, &out), "%s", body)
	require.Equal(t, "prod", out.Env["APP_MODE"], "a bound config key stays a variable")
	require.Empty(t, out.Env["API_KEY"], "a bound secret is not a variable (ADR-0249)")
	require.NotEmpty(t, out.Env["FUNCD_CATALOG_LAKE_URL"])
	require.NotEmpty(t, out.Env["FUNCD_CATALOG_LAKE_TOKEN"])
	require.Equal(t, "APP_MODE", out.Env["FUNCD_CONFIG_KEYS"])
	require.Empty(t, out.Env["FUNCD_SECRET_KEYS"])
	require.Equal(t, []string{"APP_MODE"}, out.Config)
	require.Equal(t, "prod", out.Mode)
	require.Equal(t, []string{"API_KEY"}, out.Secrets)
	require.Equal(t, "k", out.Key)
	require.Equal(t, map[string]string{"url": out.Env["FUNCD_CATALOG_LAKE_URL"], "token": out.Env["FUNCD_CATALOG_LAKE_TOKEN"]}, out.Lake)
}

// secret applies Secret creds, holding API_KEY.
func (r *asleepRig) secret(t *testing.T) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindSecret)
	s := obj.(*v1.Secret)
	s.Name, s.Namespace, s.ResourceGroup = "creds", "default", "rg1"
	s.Spec.Data = map[string][]byte{"API_KEY": []byte("k")}
	r.apply(t, s)
}
