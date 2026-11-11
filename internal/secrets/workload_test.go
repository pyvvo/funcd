package secrets_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/cedar"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// recordingAuthz records every request and denies the Secrets named in deny.
type recordingAuthz struct {
	deny map[v1.ObjectName]bool
	asks []auth.Request
}

func (a *recordingAuthz) Authorize(_ context.Context, req auth.Request) (auth.Decision, error) {
	a.asks = append(a.asks, req)
	if req.Resource != nil && a.deny[req.Resource.Name] {
		return auth.Decision{Allowed: false, Reason: "forbidden by policy test"}, nil
	}
	return auth.Decision{Allowed: true}, nil
}

// A workload's read asks secret::read for each Secret, as the workload, before reading it; a read with no principal asks
// one coarse question (ADR-0321 Decision 3).
func TestResolveEnv_AsksEachSecretAsTheWorkload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	createSecret(t, st, "api-keys", "STRIPE_KEY", "sk")
	createSecret(t, st, "settings", "LEVEL", "info")
	authz := &recordingAuthz{deny: map[v1.ObjectName]bool{}}
	r, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: authz})
	require.NoError(t, err)

	id := secrets.WorkloadIdentity(v1.KindFunction, "team-a", "reports")
	env, err := r.ResolveEnv(ctx, id, "team-a", []string{"api-keys", "settings"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{"STRIPE_KEY": "sk", "LEVEL": "info"}, env)
	require.Len(t, authz.asks, 2, "one decision per Secret")
	for i, name := range []v1.ObjectName{"api-keys", "settings"} {
		ask := authz.asks[i]
		require.Equal(t, auth.ActionSecretRead, ask.Action)
		require.Equal(t, &auth.EntityRef{Type: v1.KindSecret, Namespace: "team-a", Name: name}, ask.Resource)
		require.Equal(t, &auth.EntityRef{Type: v1.KindFunction, Namespace: "team-a", Name: "reports"}, ask.Identity.Principal)
	}

	authz.deny["api-keys"] = true
	_, err = r.ResolveEnv(ctx, id, "team-a", []string{"settings", "api-keys"})
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "a denied Secret fails the whole read")
	require.Contains(t, err.Error(), `read secret "api-keys" in "team-a" denied: forbidden by policy test`)

	authz.asks = nil
	_, err = r.ResolveEnv(ctx, secrets.InjectorIdentity("team-a"), "team-a", []string{"api-keys", "settings"})
	require.NoError(t, err, "the start check's coarse question names no Secret")
	require.Len(t, authz.asks, 1)
	require.Empty(t, authz.asks[0].Action)
}

// The readiness check's question is the decision alone (ADR-0321 Decision 4): a missing Secret passes, a denied one is
// Forbidden.
func TestCheckRead_AsksTheDecisionAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	authz := &recordingAuthz{deny: map[v1.ObjectName]bool{"api-keys": true}}
	r, err := secrets.NewResolver(secrets.Deps{Store: store.New(memory.New()), Authorizer: authz})
	require.NoError(t, err)
	id := secrets.WorkloadIdentity(v1.KindFunction, "team-a", "reports")
	require.NoError(t, r.CheckRead(ctx, id, "team-a", "never-applied"), "a missing Secret is the start check's")
	err = r.CheckRead(ctx, id, "team-a", "api-keys")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
	require.Equal(t, auth.ActionSecretRead, authz.asks[1].Action)
}

// scenario: secret-backup-and-api-unchanged — with a forbid on every secret::read of the namespace, a Backup run's coarse
// read of its credential Secret still resolves, a coarse read never reaches Cedar, and only a workload is denied. The
// API half, a person's real read through the API, is pkg/funcd's TestSecretAPIReadStaysRBACUnderAPolicy.
func TestScenarioSecretBackupAndAPIUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	createSecret(t, st, "target-creds", "AWS_ACCESS_KEY_ID", "AKID")
	reg, err := cedar.NewRegistry([]cedar.Capability{cedar.KVCapability(), cedar.SecretCapability()},
		[]cedar.PrincipalSource{cedar.FunctionPrincipalSource(), cedar.LakePrincipalSource()})
	require.NoError(t, err)
	ep, err := reg.EntityProvider(st)
	require.NoError(t, err)
	forbidAll := v1.Policy{ObjectMeta: v1.ObjectMeta{Name: "no-secrets", Namespace: "team-a"},
		Spec: v1.PolicySpec{Cedar: `forbid(principal, action == Action::"secret::read", resource);`}}
	pdp, err := cedar.New(cedar.Deps{Entities: ep, Policies: fixedPolicies{forbidAll}, Builtins: reg.Builtins()})
	require.NoError(t, err)
	authz, err := auth.NewRoutingAuthorizer(rbac.New(), pdp)
	require.NoError(t, err)
	r, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: authz})
	require.NoError(t, err)

	backup := auth.Identity{Subject: "system:backup:team-a", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}}
	env, err := r.ResolveEnv(ctx, backup, "team-a", []string{"target-creds"})
	require.NoError(t, err, "a Backup run's read asks the coarse question, which no Policy reaches")
	require.Equal(t, "AKID", env["AWS_ACCESS_KEY_ID"])

	developer := auth.Identity{Subject: "alice", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: developer, Verb: auth.VerbGet, Kind: v1.KindSecret, Namespace: "team-a"})
	require.NoError(t, err)
	require.True(t, dec.Allowed, "a coarse read goes to RBAC, never to the forbid")

	_, err = r.ResolveEnv(ctx, secrets.WorkloadIdentity(v1.KindFunction, "team-a", "reports"), "team-a", []string{"target-creds"})
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "a workload's read meets the forbid")
}

// fixedPolicies is a PolicySource over a fixed list.
type fixedPolicies []v1.Policy

func (f fixedPolicies) Policies(context.Context) ([]v1.Policy, string, error) { return f, "1", nil }
