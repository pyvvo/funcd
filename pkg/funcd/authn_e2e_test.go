//go:build e2e

package funcd_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/bus/nats"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/runtime/process"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/funcd"
)

// scenario: authn-required-401 / valid-token (e2e, F77/ADR-0113) — a real funcd with WithEdgeAuth and
// a namespace whose edgeDefaults.auth is `authenticated`. The data-plane PEP 401s an anonymous request
// and authorizes a namespace-scoped bearer — end to end through funcd.Run. (No artifact deployed: the
// authn decision needs no running function; a valid token routes PAST the gate to the activator.)
func TestScenarioE2EEdgeAuth(t *testing.T) {
	bucket, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(context.Background(), nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)

	st := store.New(memory.New())
	// Namespace `team`: authenticated edge stance (seeded directly — cluster-scoped, off-limits to dev-auth over the SDK).
	n := &v1.Namespace{TypeMeta: v1.TypeMeta{APIVersion: v1.KindNamespace.GVK().APIVersion(), Kind: v1.KindNamespace}}
	n.Name = "team"
	n.Spec.EdgeDefaults = &v1.EdgeDefaults{Auth: &v1.EdgeAuth{Mode: v1.AuthAuthenticated}}
	_, err = st.Create(context.Background(), n)
	require.NoError(t, err)
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	fn.Name, fn.Namespace, fn.ResourceGroup = "api", "team", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///tmp/x"
	_, err = st.Create(context.Background(), fn)
	require.NoError(t, err)

	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(st), funcd.WithRuntime(process.New()),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		// The dev token → developer scoped to `elsewhere` (NOT team), so it authenticates but the PDP
		// denies invoking a `team` function ⇒ 403. (The valid-serves path is covered by the dataplane
		// integration test with a warm upstream; here we prove wiring + delegation without a slow
		// activator dial to an undeployed function.)
		funcd.WithDevAuth(funcd.DevToken, "elsewhere"),
		funcd.WithEdgeAuth(), // enable the F77 PEP
		funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	base := "http://" + p.DataPlaneAddr()

	// Anonymous → 401 (the PEP rejects before store.Get + the activator).
	resp, err := postFn(base, "team", "")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "an anonymous request to an authenticated namespace is 401")

	// Authenticated but scoped elsewhere → the PDP denies ⇒ 403 (the PEP rejects, no activator).
	resp2, err := postFn(base, "team", funcd.DevToken)
	require.NoError(t, err)
	_ = resp2.Body.Close()
	require.Equal(t, http.StatusForbidden, resp2.StatusCode, "authenticated but not scoped to team ⇒ the delegated PDP denies (403)")
}

func postFn(base, ns, bearer string) (*http.Response, error) {
	req, _ := http.NewRequest("POST", base+"/function/api", nil)
	req.Header.Set("X-Funcd-Namespace", ns)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return http.DefaultClient.Do(req)
}
