//go:build e2e

package funcd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
)

// writeHandler runs the op a call posts: a put or a del of a key of a bound Secret, or a get; a refusal is an answer.
const writeHandler = `export async function handle(ctx, event) {
  const d = (event && event.data) || {};
  try {
    if (d.op === 'put') { await ctx.secret.put(d.binding, d.key, d.value); return { ok: true }; }
    if (d.op === 'del') { await ctx.secret.del(d.binding, d.key); return { ok: true }; }
    return { ok: true, key: (await ctx.secret.get(d.key)) ?? null };
  } catch (e) { return { error: String((e && e.message) || e) }; }
}
`

// oauthDecl is Secret oauth as its Functions declare it: they read STAY, and write TOKEN.
var oauthDecl = v1.SecretDeclaration{Name: "oauth", Keys: []string{"STAY"}}

type writeReply struct {
	OK    bool    `json:"ok"`
	Key   *string `json:"key"`
	Error *string `json:"error"`
}

// op is the body of a call to writeHandler.
func op(verb, binding, key, value string) string {
	b, _ := json.Marshal(map[string]string{"op": verb, "binding": binding, "key": key, "value": value})
	return string(b)
}

func wrote(w writeReply) bool        { return w.OK }
func refusedWrite(w writeReply) bool { return w.Error != nil && strings.Contains(*w.Error, "403") }

// applyOAuth applies Secret oauth, owned by owner, holding data.
func (r *secretRig) applyOAuth(t *testing.T, owner v1.ObjectName, data map[string][]byte) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindSecret)
	s := obj.(*v1.Secret)
	s.Name, s.Namespace, s.ResourceGroup = "oauth", "default", "rg1"
	s.Spec.Owner, s.Spec.Data = owner, data
	r.apply(t, s)
}

// writerFn applies Function name running writeHandler and binding oauth: solo, or in pool worker shared.
func (r *secretRig) writerFn(t *testing.T, name v1.ObjectName, pooled bool) {
	t.Helper()
	art := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(art, []byte(writeHandler), 0o600))
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = name, "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+art
	if pooled {
		fn.Spec.Pooling.Worker = "shared"
	} else {
		fn.Spec.Replicas = 1
		fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	}
	fn.Spec.Bindings.Secret = []v1.ObjectName{"oauth"}
	fn.Spec.SecretDeclarations = []v1.SecretDeclaration{oauthDecl}
	r.apply(t, fn)
}

func (r *secretRig) writeCall(t *testing.T, name v1.ObjectName, body string) writeReply {
	t.Helper()
	resp, err := http.Post("http://"+r.p.DataPlaneAddr()+"/function/"+string(name), "application/json", strings.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", raw)
	var out writeReply
	require.NoError(t, json.Unmarshal(raw, &out), "%s", raw)
	return out
}

// waitWrite calls name with body until ok holds of its answer.
func (r *secretRig) waitWrite(t *testing.T, name v1.ObjectName, body string, ok func(writeReply) bool) writeReply {
	t.Helper()
	var last writeReply
	require.Eventually(t, func() bool { last = r.writeCall(t, name, body); return ok(last) }, policyWait, policyTick, "%s", name)
	return last
}

func (r *secretRig) secretData(t *testing.T, name v1.ObjectName) map[string][]byte {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), v1.KindSecret.GVK(), "default", name)
	require.NoError(t, err)
	return obj.(*v1.Secret).Spec.Data
}

func (r *secretRig) grant(t *testing.T, name, principal v1.ObjectName, role string, scope v1.ScopeRef) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindRolesAssignment)
	ra := obj.(*v1.RolesAssignment)
	ra.Name, ra.Namespace, ra.ResourceGroup = name, "default", "rg1"
	ra.Spec.Principal = &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: principal}
	ra.Spec.Assignments = []v1.AssignmentEntry{{RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: role}, Scope: &scope}}
	r.apply(t, ra)
}

func (r *secretRig) pool(t *testing.T, name v1.ObjectName) string {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), v1.KindFunction.GVK(), "default", name)
	require.NoError(t, err)
	return obj.(*v1.Function).Status.Pool
}

// writers applies oauth (STAY s, TOKEN t1) owned by refresher, and the solo Functions refresher and api that bind it.
func (r *secretRig) writers(t *testing.T) {
	t.Helper()
	r.applyOAuth(t, "refresher", map[string][]byte{"STAY": []byte("s"), "TOKEN": []byte("t1")})
	r.writerFn(t, "refresher", false)
	r.writerFn(t, "api", false)
	r.waitStatus(t, "refresher", isReady)
	r.waitStatus(t, "api", isReady)
}

// scenario: secret-owner-writes — the owner writes a key of the Secret it binds, and api, which binds it too, reads the
// value at its next read; the owner's del removes it (ADR-0327 Decisions 4 and 7, ADR-0328).
func TestScenarioSecretOwnerWrites(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.writers(t)
	require.True(t, r.writeCall(t, "refresher", op("put", "oauth", "TOKEN", "t2")).OK)
	require.Equal(t, "t2", *r.writeCall(t, "api", op("get", "", "TOKEN", "")).Key)
	require.True(t, r.writeCall(t, "refresher", op("del", "oauth", "TOKEN", "")).OK)
	require.Nil(t, r.writeCall(t, "api", op("get", "", "TOKEN", "")).Key)
	require.Equal(t, "s", *r.writeCall(t, "api", op("get", "", "STAY", "")).Key, "the other key stays")
}

// scenario: secret-write-refused — a Function that is no writer, a Secret the caller does not bind and a Policy forbid
// each get 403, and oauth is unchanged (ADR-0327 Decisions 1 and 4).
func TestScenarioSecretWriteRefused(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.writers(t)
	got := r.writeCall(t, "api", op("put", "oauth", "TOKEN", "x"))
	require.True(t, refusedWrite(got), "%+v", got)
	require.Contains(t, *got.Error, "builtin/secret-single-writer")
	require.True(t, refusedWrite(r.writeCall(t, "refresher", op("put", "api-keys", "STRIPE_KEY", "x"))), "a Secret it does not bind")

	r.applyPolicy(t, "no-writes-for-refresher",
		`forbid(principal == Function::"default/refresher", action == Action::"secret::write", resource == Secret::"default/oauth");`)
	got = r.waitWrite(t, "refresher", op("put", "oauth", "TOKEN", "x"), refusedWrite)
	require.Contains(t, *got.Error, "forbidden by policy default/no-writes-for-refresher")
	require.Equal(t, "t1", string(r.secretData(t, "oauth")["TOKEN"]), "oauth is unchanged")
}

// scenario: secret-writer-role — Secret Data Writer on oauth, or on the namespace, lets api write; with the grant gone it
// writes no more; Secret Data Reader lets it write nothing, and api reads only the Secrets it binds (ADR-0327
// Decision 3).
func TestScenarioSecretWriterRole(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.writers(t)
	for name, scope := range map[v1.ObjectName]v1.ScopeRef{
		"api-writes-oauth": {Kind: v1.ScopeKindSecret, Name: "oauth"},
		"api-writes-all":   {Kind: v1.ScopeKindNamespace},
	} {
		r.grant(t, name, "api", "Secret Data Writer", scope)
		r.waitWrite(t, "api", op("put", "oauth", "TOKEN", string(name)), wrote)
		require.Equal(t, string(name), string(r.secretData(t, "oauth")["TOKEN"]))
		r.del(t, v1.KindRolesAssignment, name)
		r.waitWrite(t, "api", op("put", "oauth", "TOKEN", "x"), refusedWrite)
	}
	r.grant(t, "api-reads-all", "api", "Secret Data Reader", v1.ScopeRef{Kind: v1.ScopeKindNamespace})
	require.True(t, refusedWrite(r.writeCall(t, "api", op("put", "oauth", "TOKEN", "x"))), "the reader writes nothing")
	require.Nil(t, r.writeCall(t, "api", op("get", "", "STRIPE_KEY", "")).Key, "nor reads a Secret api does not bind")
}

// scenario: secret-apply-replaces-written-keys — a person's apply replaces the data, the key the owner wrote included
// (ADR-0327 Decision 7).
func TestScenarioSecretApplyReplacesWrittenKeys(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.writers(t)
	require.True(t, r.writeCall(t, "refresher", op("put", "oauth", "TOKEN", "t2")).OK)
	r.applyOAuth(t, "refresher", map[string][]byte{"STAY": []byte("s2")})
	require.Equal(t, map[string][]byte{"STAY": []byte("s2")}, r.secretData(t, "oauth"))
	require.Nil(t, r.writeCall(t, "api", op("get", "", "TOKEN", "")).Key)
}

// scenario: secret-owner-splits-the-pool — two pooled Functions that bind oauth share a pool worker until a person names
// one of them its owner; within one pass they run in different pool workers, and the owner writes from its own
// (ADR-0327 Decision 6).
func TestScenarioSecretOwnerSplitsThePool(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	r, _ := newPolicyRig(t, WithPoolShim(node, langmod.PoolShim(t)))
	r.applyOAuth(t, "", map[string][]byte{"STAY": []byte("s")})
	r.writerFn(t, "refresher", true)
	r.writerFn(t, "api", true)
	for _, n := range []v1.ObjectName{"refresher", "api"} {
		r.waitWrite(t, n, op("get", "", "STAY", ""), func(w writeReply) bool { return w.Key != nil && *w.Key == "s" })
	}
	require.NotEmpty(t, r.pool(t, "refresher"))
	require.Equal(t, r.pool(t, "refresher"), r.pool(t, "api"), "the same access shares a pool")

	r.applyOAuth(t, "refresher", map[string][]byte{"STAY": []byte("s")})
	require.Eventually(t, func() bool {
		a, b := r.pool(t, "refresher"), r.pool(t, "api")
		return a != "" && b != "" && a != b
	}, policyWait, policyTick, "the owner leaves the shared pool")
	r.waitWrite(t, "refresher", op("put", "oauth", "TOKEN", "t2"), wrote)
	require.True(t, refusedWrite(r.writeCall(t, "api", op("put", "oauth", "TOKEN", "x"))), "api is no writer")
}
