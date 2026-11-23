//go:build !e2e

package funcd_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/secrets"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// secretBodyCap is the local API's cap on a Secret write's body, maxSecretBytes.
const secretBodyCap = 1 << 20

// secretCauseEnv runs a platform with opts whose default namespace holds the secret matrix's objects: Secret s, which
// writer owns, and Secret binary, whose value is not UTF-8 text; writer, reader and denied bind s, and a Policy forbids
// denied to read it; dangling binds nope, a Secret that does not exist, and binder binds binary.
func secretCauseEnv(t *testing.T, opts ...funcd.Option) *causeEnv {
	t.Helper()
	p := answersPlatform(t, opts...)
	c, base := answersClient(), "http://"+p.Addr()
	for name, bound := range map[v1.ObjectName]v1.SecretDeclaration{"writer": {Name: "s", Keys: []string{"K"}},
		"reader": {Name: "s", Keys: []string{"K"}}, "denied": {Name: "s", Keys: []string{"K"}},
		"dangling": {Name: "nope", Keys: []string{"K"}}, "binder": {Name: "binary", Keys: []string{"B"}}} {
		fn := function(name)
		fn.Spec.SecretDeclarations = []v1.SecretDeclaration{bound}
		fn.Spec.Bindings.Secret = []v1.ObjectName{bound.Name}
		create(t, c, base, fn)
	}
	s := secretWithData("s", map[string][]byte{"K": []byte("v")})
	s.Spec.Owner = v1.FunctionOwner("writer")
	for _, sec := range []*v1.Secret{s, secretWithData("binary", map[string][]byte{"B": {0xff}})} {
		create(t, c, base, sec)
	}
	create(t, c, base, &v1.Policy{TypeMeta: v1.TypeMeta{APIVersion: v1.KindPolicy.GVK().APIVersion(), Kind: v1.KindPolicy},
		ObjectMeta: v1.ObjectMeta{Name: "no-reads", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.PolicySpec{Cedar: `forbid(principal == Function::"default/denied", action == Action::"secret::read", resource);`}})
	return &causeEnv{p: p}
}

// secretWithData is a Secret of default that holds data.
func secretWithData(name v1.ObjectName, data map[string][]byte) *v1.Secret {
	return &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.SecretSpec{Data: data}}
}

// contendedSecrets is a store in which each update of a Secret meets another write of it, so no write of a key lands.
type contendedSecrets struct{ store.Store }

func (s contendedSecrets) Update(ctx context.Context, obj v1.Object) (v1.Object, error) {
	if obj.GroupVersionKind().Kind == v1.KindSecret {
		return nil, fault.Conflictf("contendedSecrets.Update", "the Secret changed since it was read")
	}
	return s.Store.Update(ctx, obj)
}

// TestSecretCauseMatrix is the secret service's matrix: each cause that a secret endpoint declares, provoked by one
// request.
func TestSecretCauseMatrix(t *testing.T) {
	t.Parallel()
	runCauseMatrix(t, secretCauseRows(secretEnvs{
		main:      secretCauseEnv(t),
		contended: secretCauseEnv(t, funcd.WithStore(contendedSecrets{store.New(memory.New())})),
	}))
}

// secretEnvs are the platforms of the secret matrix: main, and contended, whose Secrets change under every write.
type secretEnvs struct {
	main, contended *causeEnv
}

// secretCauseRows is the secret matrix over envs.
func secretCauseRows(envs secretEnvs) []causeRow {
	const (
		list, key, put, del = "GET /secret", "GET /secret/{key}", "PUT /secret/{binding}/{key}",
			"DELETE /secret/{binding}/{key}"
		value = `{"value": "v"}`
	)
	get := func(path string) causeRequest { return causeRequest{method: http.MethodGet, path: path} }
	write := func(path, body string) causeRequest {
		return causeRequest{method: http.MethodPut, path: path, body: body}
	}
	remove := func(path string) causeRequest { return causeRequest{method: http.MethodDelete, path: path} }
	main, contended := envs.main, envs.contended
	var rows []causeRow
	for _, e := range []struct{ endpoint, path string }{{list, "/secret"}, {key, "/secret/K"}} {
		rows = append(rows,
			causeRow{endpoint: e.endpoint, cause: secrets.ErrCallerMissing, env: main, caller: "ghost", request: get(e.path)},
			causeRow{endpoint: e.endpoint, cause: secrets.ErrReadDenied, env: main, caller: "denied", request: get(e.path)},
			causeRow{endpoint: e.endpoint, cause: secrets.ErrSecretMissing, env: main, caller: "dangling", request: get(e.path)},
			causeRow{endpoint: e.endpoint, cause: secrets.ErrValueNotText, env: main, caller: "binder", request: get(e.path)})
	}
	rows = append(rows,
		causeRow{endpoint: key, cause: local.ErrSecretKeyNotFound, env: main, caller: "writer", request: get("/secret/NONE")},

		causeRow{endpoint: put, cause: local.ErrBodyTooLarge, env: main, caller: "writer",
			request: write("/secret/s/K", strings.Repeat("v", secretBodyCap+1))},
		causeRow{endpoint: put, cause: local.ErrBodyUnreadable, env: main, caller: "writer", request: causeRequest{
			raw: "PUT /secret/s/K HTTP/1.1\r\nHost: local\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n"}},
		causeRow{endpoint: put, cause: local.ErrSecretBodyInvalid, env: main, caller: "writer",
			request: write("/secret/s/K", `{"value": 5}`)},
		causeRow{endpoint: put, cause: secrets.ErrValueNotText, env: main, caller: "writer",
			request: write("/secret/s/K", `{"value": "v\u0000"}`)},
		causeRow{endpoint: put, cause: secrets.ErrKeyInvalid, env: main, caller: "writer", request: write("/secret/s/not-a-name", value)})
	for _, e := range []struct {
		endpoint string
		request  func(binding string) causeRequest
	}{
		{put, func(b string) causeRequest { return write("/secret/"+b+"/K", value) }},
		{del, func(b string) causeRequest { return remove("/secret/" + b + "/K") }},
	} {
		rows = append(rows,
			causeRow{endpoint: e.endpoint, cause: secrets.ErrCallerMissing, env: main, caller: "ghost", request: e.request("s")},
			causeRow{endpoint: e.endpoint, cause: secrets.ErrNoBinding, env: main, caller: "writer", request: e.request("binary")},
			causeRow{endpoint: e.endpoint, cause: secrets.ErrSecretMissing, env: main, caller: "dangling", request: e.request("nope")},
			causeRow{endpoint: e.endpoint, cause: secrets.ErrWriteDenied, env: main, caller: "reader", request: e.request("s")},
			causeRow{endpoint: e.endpoint, cause: secrets.ErrWriteContended, env: contended, caller: "writer",
				request: e.request("s")})
	}
	return rows
}
