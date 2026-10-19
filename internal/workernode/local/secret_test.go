package local_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/secrets"
	secretsvc "github.com/pyvvo/funcd/internal/services/secret"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// secretPDP allows or denies every request, standing in for an authorizer that lets the namespace's developer read
// its Secrets or not (ADR-0057 Decision 4).
type secretPDP bool

func (p secretPDP) Authorize(context.Context, auth.Request) (auth.Decision, error) {
	return auth.Decision{Allowed: bool(p)}, nil
}

// secretStore holds Secret api-keys (STRIPE_KEY sk, MODE prod) and Functions names in team-a, each binding it.
func secretStore(t *testing.T, names ...v1.ObjectName) store.Store {
	t.Helper()
	st := store.New(memory.New())
	ctx := context.Background()
	obj, _ := v1.NewObject(v1.KindSecret)
	sec := obj.(*v1.Secret)
	sec.Name, sec.Namespace, sec.ResourceGroup = "api-keys", "team-a", "rg1"
	sec.Spec.Data = map[string][]byte{"STRIPE_KEY": []byte("sk"), "MODE": []byte("prod")}
	_, err := st.Create(ctx, sec)
	require.NoError(t, err)
	for _, n := range names {
		obj, _ := v1.NewObject(v1.KindFunction)
		fn := obj.(*v1.Function)
		fn.Name, fn.Namespace, fn.ResourceGroup = n, "team-a", "rg1"
		fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file:///unused.mjs"
		fn.Spec.Bindings.Secret = []v1.ObjectName{"api-keys"}
		_, err := st.Create(ctx, fn)
		require.NoError(t, err)
	}
	return st
}

func secretFacade(t *testing.T, st store.Store, allow bool) *secretsvc.Facade {
	t.Helper()
	res, err := secrets.NewResolver(secrets.Deps{Store: st, Authorizer: secretPDP(allow)})
	require.NoError(t, err)
	f, err := secretsvc.NewFacade(secretsvc.Deps{Store: st, Resolver: res})
	require.NoError(t, err)
	return f
}

func secretHandler(t *testing.T, allow bool) http.Handler {
	t.Helper()
	return local.NewHandler(local.Ref{Namespace: "team-a", Function: "fn"}, nil, nil, nil, nil, nil, nil, nil,
		local.WithSecrets(secretFacade(t, secretStore(t, "fn"), allow)))
}

// scenario: secret-read-from-funcd — GET /secret/{key} returns a bound Secret's value and GET /secret its sorted keys,
// resolved by funcd on the read (ADR-0249 Decision 2); scenario: secret-unbound — a key no bound Secret holds is 404.
func TestScenarioSecretReadFromFuncd(t *testing.T) {
	t.Parallel()
	h := secretHandler(t, true)

	rec := do(t, h, http.MethodGet, "/secret/STRIPE_KEY", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"value":"sk"}`, rec.Body.String())

	rec = do(t, h, http.MethodGet, "/secret", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"keys":["MODE","STRIPE_KEY"]}`, rec.Body.String())

	rec = do(t, h, http.MethodGet, "/secret/NOPE", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// scenario: secret-denied — an authorizer that denies the namespace's Secret reads to the secret-injector identity
// makes both routes 403.
func TestScenarioSecretDenied(t *testing.T) {
	t.Parallel()
	h := secretHandler(t, false)
	for _, path := range []string{"/secret/STRIPE_KEY", "/secret"} {
		rec := do(t, h, http.MethodGet, path, "")
		require.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", path, rec.Body.String())
	}
}

// scenario: secret-pooled-member — on a pool's local API each member reads the pool's Secrets as itself, a member
// that names its sibling reads the same Secrets, and a request naming no member is refused (ADR-0158).
func TestScenarioSecretPooledMember(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "fl") // not t.TempDir(): a unix socket path is capped near 104 bytes
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	st := secretStore(t, "a", "b")
	m := local.NewManager(dir, st, nil, nil, nil, nil, nil, slog.New(slog.DiscardHandler), local.WithSecrets(secretFacade(t, st, true)))
	t.Cleanup(m.Close)
	sock, err := m.PoolSocketFor("team-a", testPool, []v1.ObjectName{"a", "b"})
	require.NoError(t, err)
	c := unixClient(t, sock)

	read := func(member string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://local/secret/STRIPE_KEY", http.NoBody)
		require.NoError(t, err)
		if member != "" {
			req.Header.Set(local.MemberHeader, member)
		}
		resp, err := c.Do(req)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var v struct {
			Value string `json:"value"`
		}
		_ = json.Unmarshal(body, &v)
		return resp.StatusCode, v.Value
	}
	for _, member := range []string{"a", "b"} {
		code, value := read(member)
		require.Equal(t, http.StatusOK, code, member)
		require.Equal(t, "sk", value, member)
	}
	code, _ := read("")
	require.Equal(t, http.StatusForbidden, code, "a request naming no member is refused")
}
