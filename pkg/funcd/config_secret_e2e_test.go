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

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
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
	src := "export function handle(_, e) {" +
		" return { config: process.env.APP_MODE, secret: process.env.API_KEY, shared: process.env.SHARED }; }\n"
	require.NoError(t, os.WriteFile(p, []byte(src), 0o600))
	return "file://" + p
}

// scenario (e2e): function-config-and-secret-env-injected — a Function binding BOTH spec.config (a
// ConfigMap, ADR-0093) and spec.secrets (a Secret, ADR-0057) receives BOTH sets of Data as worker env at
// runtime, proven by invoking a handler that echoes them back. A key present in both is won by the SECRET
// (config-then-secrets merge order, ADR-0092/0093). This is the end-to-end proof (reconcile → resolve →
// materialize → process shim → handler) that the injection reaches a running function. Node-gated.
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
	fn.Spec.Config = []v1.ObjectName{"app-config"}  // ADR-0093
	fn.Spec.Secrets = []v1.ObjectName{"app-secret"} // ADR-0057
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
		Config string `json:"config"`
		Secret string `json:"secret"`
		Shared string `json:"shared"`
	}
	require.NoError(t, json.Unmarshal(body, &out), "handler body: %s", body)
	require.Equal(t, "prod", out.Config, "spec.config ConfigMap value reaches the handler as env")
	require.Equal(t, "s3cr3t", out.Secret, "spec.secrets Secret value reaches the handler as env")
	require.Equal(t, "from-secret", out.Shared, "a key in both ConfigMap+Secret: the secret wins (config-then-secrets)")
}
