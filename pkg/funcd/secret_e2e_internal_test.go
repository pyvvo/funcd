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

// secretHandler returns what its worker's environment, and a child process's, hold of the bound Secret, and what it
// reads through ctx.secret (ADR-0249).
const secretHandler = `import { execFileSync } from 'node:child_process';
export async function handle(ctx) {
  return {
    pid: process.pid,
    env: { STRIPE_KEY: process.env.STRIPE_KEY ?? null, MODE: process.env.MODE ?? null,
      FUNCD_SECRET_KEYS: process.env.FUNCD_SECRET_KEYS ?? null, LOG_LEVEL: process.env.LOG_LEVEL ?? null },
    child: execFileSync('env').toString(),
    key: await ctx.secret.get('STRIPE_KEY'), mode: await ctx.secret.get('MODE'), keys: await ctx.secret.list(),
    configMode: ctx.config.get('MODE') ?? null,
  };
}
`

// secretRig is a platform on the real node shim with Secret api-keys (STRIPE_KEY sk, MODE prod) and ConfigMap
// app-settings (LOG_LEVEL debug, MODE dev) in namespace default.
type secretRig struct {
	*asleepRig
	engine *envEngine
}

func newSecretRig(t *testing.T) *secretRig {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	engine := &envEngine{addr: strings.TrimPrefix(srv.URL, "http://")}
	p, err := New(InMemory(), WithoutLogCompaction(), WithRuntimeShim(node, langmod.NodeShim(t)),
		WithCatalogProviderRuntime(engine), WithPacing(asleepPacing(50*time.Millisecond)))
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	r := &secretRig{asleepRig: &asleepRig{p: p, c: c}, engine: engine}
	r.applySecret(t, "api-keys", map[string][]byte{"STRIPE_KEY": []byte("sk"), "MODE": []byte("prod")})
	obj, _ := v1.NewObject(v1.KindConfigMap)
	cm := obj.(*v1.ConfigMap)
	cm.Name, cm.Namespace, cm.ResourceGroup = "app-settings", "default", "rg1"
	cm.Spec.Data = map[string]string{"LOG_LEVEL": "debug", "MODE": "dev"}
	r.apply(t, cm)
	return r
}

func (r *secretRig) applySecret(t *testing.T, name v1.ObjectName, data map[string][]byte) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindSecret)
	s := obj.(*v1.Secret)
	s.Name, s.Namespace, s.ResourceGroup = name, "default", "rg1"
	s.Spec.Data = data
	r.apply(t, s)
}

// reader applies Function reader running secretHandler, binding app-settings and api-keys, and waits for Ready.
func (r *secretRig) reader(t *testing.T) {
	t.Helper()
	art := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(art, []byte(secretHandler), 0o600))
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "reader", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+art
	fn.Spec.Replicas = 1
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	fn.Spec.Bindings.Config = []v1.ObjectName{"app-settings"}
	fn.Spec.Bindings.Secret = []v1.ObjectName{"api-keys"}
	fn.Spec.ConfigMaps = []v1.ConfigMapEntry{{Ref: "app-settings"}}
	fn.Spec.SecretDeclarations = []v1.SecretDeclaration{{Name: "api-keys", Keys: []string{"STRIPE_KEY", "MODE"}}}
	r.apply(t, fn)
	require.Eventually(t, func() bool {
		obj, err := r.p.cfg.store.Get(context.Background(), v1.KindFunction.GVK(), "default", "reader")
		return err == nil && obj.(*v1.Function).Status.Phase == v1.PhaseReady
	}, 15*time.Second, 50*time.Millisecond, "reader is Ready")
}

type secretReply struct {
	PID        int                `json:"pid"`
	Env        map[string]*string `json:"env"`
	Child      string             `json:"child"`
	Key        *string            `json:"key"`
	Mode       *string            `json:"mode"`
	Keys       []string           `json:"keys"`
	ConfigMode *string            `json:"configMode"`
}

func (r *secretRig) call(t *testing.T) secretReply {
	t.Helper()
	resp, err := http.Post("http://"+r.p.DataPlaneAddr()+"/function/reader", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var out secretReply
	require.NoError(t, json.Unmarshal(body, &out), "%s", body)
	return out
}

// scenario: secret-not-in-env — a real worker's environment, and a child process's, hold neither a bound Secret's key
// nor FUNCD_SECRET_KEYS, while the handler reads the values through ctx.secret (secret-read-from-funcd) and a config key
// a Secret also holds reads only as a secret (secret-shadows-config).
func TestScenarioSecretNotInEnv(t *testing.T) {
	t.Parallel()
	r := newSecretRig(t)
	r.reader(t)
	out := r.call(t)

	for _, k := range []string{"STRIPE_KEY", "MODE", "FUNCD_SECRET_KEYS"} {
		require.Nil(t, out.Env[k], "%s is not in the worker env", k)
		require.NotContains(t, out.Child, k+"=", "%s is not in a child process's env", k)
	}
	require.Equal(t, "debug", *out.Env["LOG_LEVEL"], "a config key still is")
	require.Equal(t, "sk", *out.Key)
	require.Equal(t, "prod", *out.Mode)
	require.Equal(t, []string{"MODE", "STRIPE_KEY"}, out.Keys)
	require.Nil(t, out.ConfigMode, "MODE is a secret, never read through ctx.config")
}

// scenario: secret-read-live — a changed Secret value is the next read's, with no restart of the worker.
func TestScenarioSecretReadLive(t *testing.T) {
	t.Parallel()
	r := newSecretRig(t)
	r.reader(t)
	before := r.call(t)
	require.Equal(t, "sk", *before.Key)

	r.applySecret(t, "api-keys", map[string][]byte{"STRIPE_KEY": []byte("sk2"), "MODE": []byte("prod")})
	after := r.call(t)
	require.Equal(t, "sk2", *after.Key)
	require.Equal(t, before.PID, after.PID, "the same worker serves the new value")
}

// scenario: provider-env-unchanged — a CatalogService engine, which runs no handler, still receives its bound Secret's
// QUACK_TOKEN in its environment (ADR-0249 Decision 3: envresolve is unchanged for providers).
func TestScenarioProviderEnvUnchanged(t *testing.T) {
	t.Parallel()
	r := newSecretRig(t)
	r.applySecret(t, "quack", map[string][]byte{"QUACK_TOKEN": []byte("qt")})
	r.bucket(t, "lakehouse", "raw", "lake")
	cs := lakeCatalogService("default")
	cs.Spec.Bindings.Secret = []v1.ObjectName{"quack"}
	cs.Spec.SecretDeclarations = []v1.SecretDeclaration{{Name: "quack", Keys: []string{"QUACK_TOKEN"}}}
	r.apply(t, cs)
	require.Eventually(t, func() bool { return r.engine.lastEnv()["QUACK_TOKEN"] == "qt" }, 15*time.Second, 50*time.Millisecond,
		"the engine's env carries QUACK_TOKEN")
}
