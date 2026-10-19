//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// applyConfigMap creates a ConfigMap (non-sensitive Data → env via spec.config, ADR-0093).
func applyConfigMap(t *testing.T, c *sdk.Client, name string, data map[string]string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindConfigMap)
	cm := obj.(*v1.ConfigMap)
	cm.Name, cm.Namespace, cm.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	cm.Spec.Data = data
	_, err := c.Apply(context.Background(), cm)
	require.NoError(t, err)
}

// applySecret creates a Secret (sensitive Data → env via spec.secrets, ADR-0057).
func applySecret(t *testing.T, c *sdk.Client, name string, data map[string][]byte) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindSecret)
	s := obj.(*v1.Secret)
	s.Name, s.Namespace, s.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	s.Spec.Data = data
	_, err := c.Apply(context.Background(), s)
	require.NoError(t, err)
}

// writeEnvEchoArtifact writes an inline handler that echoes the env vars we assert were injected.
func writeEnvEchoArtifact(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "handler.mjs")
	src := "export async function handle(ctx) { return { config: process.env.APP_MODE ?? null," +
		" secret: await ctx.secret.get('API_KEY'), envSecret: process.env.API_KEY ?? null," +
		" shared: process.env.SHARED ?? null, sharedSecret: await ctx.secret.get('SHARED') }; }\n"
	require.NoError(t, os.WriteFile(p, []byte(src), 0o600))
	return "file://" + p
}

// scenario (e2e): function-config-and-secret-env-injected — a Function binding BOTH a ConfigMap (ADR-0093) and a
// Secret (ADR-0057) receives the ConfigMap's Data as worker env and reads the Secret's through ctx.secret, never from
// its env (ADR-0249). A key present in both is the SECRET's (config-then-secrets, ADR-0092/0093), so it reads only
// through ctx.secret. The end-to-end proof (reconcile → resolve → materialize → process shim → handler). Node-gated.
func TestScenarioFunctionConfigAndSecretEnvInjected(t *testing.T) {
	c, dpURL, _ := shimPlatform(t)

	applyConfigMap(t, c, "app-config", map[string]string{"APP_MODE": "prod", "SHARED": "from-config"})
	applySecret(t, c, "app-secret", map[string][]byte{"API_KEY": []byte("s3cr3t"), "SHARED": []byte("from-secret")})

	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = "env-echo", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = "nodejs22", "handle"
	fn.Spec.Image = writeEnvEchoArtifact(t)
	fn.Spec.Replicas = 1
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	fn.Spec.Bindings.Config = []v1.ObjectName{"app-config"} // ADR-0093
	fn.Spec.Bindings.Secret = []v1.ObjectName{"app-secret"} // ADR-0057
	fn.Spec.ConfigMaps = []v1.ConfigMapEntry{{Ref: "app-config"}}
	fn.Spec.SecretDeclarations = []v1.SecretDeclaration{{Name: "app-secret", Keys: []string{"API_KEY", "SHARED"}}}
	_, err := c.Apply(context.Background(), fn)
	require.NoError(t, err)

	// Ready requires BOTH bindings to resolve (fail-closed otherwise) — so Ready already proves resolution.
	require.Eventually(t, func() bool { return phaseOf(t, c, "env-echo") == v1.PhaseReady },
		15*time.Second, 50*time.Millisecond, "the config+secret-bound function reconciles to Ready (both resolved)")

	resp, err := http.Post(dpURL+"/function/env-echo", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "the invocation reaches the function: %s", body)

	var out struct {
		Config       *string `json:"config"`
		Secret       *string `json:"secret"`
		EnvSecret    *string `json:"envSecret"`
		Shared       *string `json:"shared"`
		SharedSecret *string `json:"sharedSecret"`
	}
	require.NoError(t, json.Unmarshal(body, &out), "handler body: %s", body)
	require.Equal(t, "prod", *out.Config, "the ConfigMap value reaches the handler as env")
	require.Equal(t, "s3cr3t", *out.Secret, "the Secret value reaches the handler through ctx.secret")
	require.Nil(t, out.EnvSecret, "and not through its env (ADR-0249)")
	require.Nil(t, out.Shared, "a key in both ConfigMap+Secret is the secret's, so it is not in the env")
	require.Equal(t, "from-secret", *out.SharedSecret, "and ctx.secret reads the secret's value")
}
