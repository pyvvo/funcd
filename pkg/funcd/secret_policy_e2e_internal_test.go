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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// policyHandler reads KEY through ctx.secret and answers the value or the error, so a denied read is an answer.
const policyHandler = `export async function handle(ctx) {
  try { return { pid: process.pid, key: (await ctx.secret.get('KEY')) ?? null }; }
  catch (e) { return { pid: process.pid, error: String((e && e.message) || e) }; }
}
`

const (
	// policyWait bounds every wait below; the rig's pacing (asleepPacing) settles each in well under a second.
	policyWait = 15 * time.Second
	policyTick = 25 * time.Millisecond
	// policyHold is how long a Function must stay Ready after its Secret is deleted: many 100 ms supervision periods.
	policyHold = time.Second
)

// countingEngine is the rig's fake Lake engine that also counts the stops a held Lake asks for.
type countingEngine struct {
	*envEngine
	teardowns atomic.Int32
}

func (e *countingEngine) Teardown(ctx context.Context, ref provider.ProviderRef) error {
	e.teardowns.Add(1)
	return e.envEngine.Teardown(ctx, ref)
}

// newPolicyRig is a platform on the real node shim with Secret api-keys (STRIPE_KEY sk, MODE prod) in namespace
// default and a fake Lake engine that counts its stops; extra options join the platform's.
func newPolicyRig(t *testing.T, extra ...Option) (*secretRig, *countingEngine) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	engine := &countingEngine{envEngine: &envEngine{addr: strings.TrimPrefix(srv.URL, "http://")}}
	p, err := New(append([]Option{InMemory(), WithoutLogCompaction(), WithRuntimeShim(node, langmod.NodeShim(t)),
		WithLakeProviderRuntime(engine), WithPacing(asleepPacing(50 * time.Millisecond))}, extra...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(DevToken))
	require.NoError(t, err)
	r := &secretRig{asleepRig: &asleepRig{p: p, c: c}, engine: engine.envEngine}
	r.applySecret(t, "api-keys", map[string][]byte{"STRIPE_KEY": []byte("sk"), "MODE": []byte("prod")})
	return r, engine
}

var apiKeys = v1.SecretDeclaration{Name: "api-keys", Keys: []string{"STRIPE_KEY", "MODE"}}

// policyFn applies solo Function name running policyHandler on key, binding the declared Secrets in their order.
func (r *secretRig) policyFn(t *testing.T, name v1.ObjectName, key string, decls ...v1.SecretDeclaration) {
	t.Helper()
	art := filepath.Join(t.TempDir(), "handler.mjs")
	require.NoError(t, os.WriteFile(art, []byte(strings.Replace(policyHandler, "'KEY'", "'"+key+"'", 1)), 0o600))
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = name, "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image = "nodejs22", "handle", "file://"+art
	fn.Spec.Replicas = 1
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	for _, d := range decls {
		fn.Spec.Bindings.Secret = append(fn.Spec.Bindings.Secret, d.Name)
	}
	fn.Spec.SecretDeclarations = decls
	r.apply(t, fn)
}

// fnStatus is a Function's phase and its Ready and RevisionReady conditions.
type fnStatus struct {
	phase                v1.Phase
	ready, revisionReady string
	message              string
}

func (s fnStatus) String() string {
	return string(s.phase) + " Ready=" + s.ready + " RevisionReady=" + s.revisionReady + ": " + s.message
}

func (r *secretRig) status(t *testing.T, name v1.ObjectName) fnStatus {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), v1.KindFunction.GVK(), "default", name)
	require.NoError(t, err)
	fn := obj.(*v1.Function)
	rc, _ := fn.Status.Conditions.Get("Ready")
	rr, _ := fn.Status.Conditions.Get("RevisionReady")
	msg := rc.Message
	if rr.Reason == reasonDependencyNotReady {
		msg = rr.Message
	}
	return fnStatus{phase: fn.Status.Phase, ready: rc.Reason, revisionReady: rr.Reason, message: msg}
}

const reasonDependencyNotReady = "DependencyNotReady"

func notReadyForDependency(s fnStatus) bool {
	return s.ready == reasonDependencyNotReady || s.revisionReady == reasonDependencyNotReady
}

func isReady(s fnStatus) bool { return s.phase == v1.PhaseReady }

// waitStatus waits until ok holds of the Function's status.
func (r *secretRig) waitStatus(t *testing.T, name v1.ObjectName, ok func(fnStatus) bool) fnStatus {
	t.Helper()
	var last fnStatus
	require.Eventually(t, func() bool { last = r.status(t, name); return ok(last) }, policyWait, policyTick, "%s", name)
	return last
}

type policyReply struct {
	PID   int     `json:"pid"`
	Key   *string `json:"key"`
	Error *string `json:"error"`
}

func (r *secretRig) policyCall(t *testing.T, name v1.ObjectName) policyReply {
	t.Helper()
	resp, err := http.Post("http://"+r.p.DataPlaneAddr()+"/function/"+string(name), "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	var out policyReply
	require.NoError(t, json.Unmarshal(body, &out), "%s", body)
	return out
}

// waitRead calls name until ok holds of its reply.
func (r *secretRig) waitRead(t *testing.T, name v1.ObjectName, ok func(policyReply) bool) policyReply {
	t.Helper()
	var last policyReply
	require.Eventually(t, func() bool { last = r.policyCall(t, name); return ok(last) }, policyWait, policyTick, "%s", name)
	return last
}

func reads(want string) func(policyReply) bool {
	return func(s policyReply) bool { return s.Key != nil && *s.Key == want }
}

func denied(s policyReply) bool { return s.Error != nil }

func (r *secretRig) applyPolicy(t *testing.T, name v1.ObjectName, cedar string) {
	t.Helper()
	obj, _ := v1.NewObject(v1.KindPolicy)
	p := obj.(*v1.Policy)
	p.Name, p.Namespace, p.ResourceGroup = name, "default", "rg1"
	p.Spec.Cedar = cedar
	r.apply(t, p)
}

func forbidRead(principal, secret string) string {
	return `forbid(principal == ` + principal + `, action == Action::"secret::read", resource == Secret::"default/` + secret + `");`
}

func lakeEngineHasKey(r *secretRig) bool { return r.engine.lastEnv()["QUACK_TOKEN"] == "qt" }

func (r *secretRig) lakeReady(t *testing.T) (reason, message string) {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), v1.KindLake.GVK(), "default", "lake")
	require.NoError(t, err)
	c, _ := obj.(*v1.Lake).Status.Conditions.Get("Ready")
	return c.Reason, c.Message
}

// applyQuackLake applies Lake lake binding Secret quack (QUACK_TOKEN qt) over its lakehouse bucket.
func (r *secretRig) applyQuackLake(t *testing.T) {
	t.Helper()
	r.applySecret(t, "quack", map[string][]byte{"QUACK_TOKEN": []byte("qt")})
	r.bucket(t, "lakehouse", "raw", v1.LakeOwner("lake"))
	cs := lakeResource("default")
	cs.Spec.Bindings.Secret = []v1.ObjectName{"quack"}
	cs.Spec.SecretDeclarations = []v1.SecretDeclaration{{Name: "quack", Keys: []string{"QUACK_TOKEN"}}}
	r.apply(t, cs)
}

// scenario: secret-bound-reads — a Function reads the Secret it binds with no Policy (ADR-0321 Decision 2).
func TestScenarioSecretBoundReads(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.policyFn(t, "checkout", "STRIPE_KEY", apiKeys)
	r.waitStatus(t, "checkout", isReady)
	require.Equal(t, "sk", *r.policyCall(t, "checkout").Key)
}

// scenario: secret-forbid-one-read — a forbid on one of reports' Secrets fails each of its reads, of its other
// Secret's key too, naming the policy; checkout, which binds the same Secret, still reads (ADR-0321 Decision 3).
func TestScenarioSecretForbidOneRead(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.applySecret(t, "settings", map[string][]byte{"LEVEL": []byte("info")})
	r.policyFn(t, "checkout", "STRIPE_KEY", apiKeys)
	r.policyFn(t, "reports", "LEVEL", v1.SecretDeclaration{Name: "settings", Keys: []string{"LEVEL"}}, apiKeys)
	r.waitStatus(t, "checkout", isReady)
	r.waitStatus(t, "reports", isReady)
	require.Equal(t, "info", *r.policyCall(t, "reports").Key)

	r.applyPolicy(t, "no-stripe-for-reports", forbidRead(`Function::"default/reports"`, "api-keys"))
	got := r.waitRead(t, "reports", denied)
	require.Contains(t, *got.Error, "403")
	require.Contains(t, *got.Error, "forbidden by policy default/no-stripe-for-reports")
	require.Equal(t, "sk", *r.policyCall(t, "checkout").Key)
}

// scenario: secret-forbid-readiness — the forbid fails reports' readiness check: it is Degraded, DependencyNotReady
// naming the secret binding, and is not restarted; a new Function the forbid names runs a worker that is not ready,
// never SecretResolveFailed (ADR-0321 Decision 4).
func TestScenarioSecretForbidReadiness(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.policyFn(t, "reports", "STRIPE_KEY", apiKeys)
	r.waitStatus(t, "reports", isReady)

	r.applyPolicy(t, "no-stripe-for-reports", forbidRead(`Function::"default/reports"`, "api-keys"))
	st := r.waitStatus(t, "reports", func(s fnStatus) bool { return s.phase == v1.PhaseDegraded && notReadyForDependency(s) })
	require.Contains(t, st.message, `secret binding "api-keys"`, st.String())

	r.applyPolicy(t, "no-stripe-for-late", forbidRead(`Function::"default/late"`, "api-keys"))
	r.policyFn(t, "late", "STRIPE_KEY", apiKeys)
	st = r.waitStatus(t, "late", notReadyForDependency)
	require.NotEqual(t, v1.PhaseFailed, st.phase, st.String())
	require.NotEqual(t, "SecretResolveFailed", st.ready, st.String())
}

// scenario: secret-forbid-lifted — deleting the forbids lets reports read at its next read, on the worker it had
// before the forbid, and both Functions are Ready again with no edit (ADR-0321 Decision 4).
func TestScenarioSecretForbidLifted(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.policyFn(t, "reports", "STRIPE_KEY", apiKeys)
	r.waitStatus(t, "reports", isReady)
	before := r.policyCall(t, "reports")
	r.applyPolicy(t, "no-stripe-for-reports", forbidRead(`Function::"default/reports"`, "api-keys"))
	r.applyPolicy(t, "no-stripe-for-late", forbidRead(`Function::"default/late"`, "api-keys"))
	r.policyFn(t, "late", "STRIPE_KEY", apiKeys)
	r.waitStatus(t, "reports", notReadyForDependency)
	r.waitStatus(t, "late", notReadyForDependency)

	r.del(t, v1.KindPolicy, "no-stripe-for-reports")
	r.del(t, v1.KindPolicy, "no-stripe-for-late")
	got := r.waitRead(t, "reports", reads("sk"))
	require.Equal(t, before.PID, got.PID, "the same worker serves again")
	r.waitStatus(t, "reports", isReady)
	r.waitStatus(t, "late", isReady)
	require.Equal(t, "sk", *r.policyCall(t, "late").Key)
}

// scenario: secret-missing-unchanged — a Function that binds a Secret not applied yet is held Pending, SecretNotFound,
// as today, even with a Policy that permits its reads, until the Secret is applied; a bound Secret no forbid names,
// deleted while a worker serves, leaves it serving and reading nothing. That the readiness check reads no Secret
// (ADR-0321 Decision 4) is TestCheckRead_AsksTheDecisionAlone's: here the lost binding holds the Function's next pass
// at its start gate (docs/behavior/functions.md), so its readiness is not read again.
func TestScenarioSecretMissingUnchanged(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.applyPolicy(t, "secrets-for-early", `permit(principal == Function::"default/early", action == Action::"secret::read", resource);`)
	r.policyFn(t, "early", "LATE_KEY", v1.SecretDeclaration{Name: "later", Keys: []string{"LATE_KEY"}})
	r.waitStatus(t, "early", func(s fnStatus) bool { return s.phase == v1.PhasePending && s.ready == "SecretNotFound" })
	r.applySecret(t, "later", map[string][]byte{"LATE_KEY": []byte("here")})
	r.waitStatus(t, "early", isReady)
	require.Equal(t, "here", *r.policyCall(t, "early").Key)

	r.policyFn(t, "checkout", "STRIPE_KEY", apiKeys)
	r.waitStatus(t, "checkout", isReady)
	r.del(t, v1.KindSecret, "api-keys")
	r.waitRead(t, "checkout", func(s policyReply) bool { return s.Error == nil && s.Key == nil })
	for end := time.Now().Add(policyHold); time.Now().Before(end); time.Sleep(policyTick) {
		require.Equal(t, v1.PhaseReady, r.status(t, "checkout").phase, "the Function stays Ready")
	}
}

// scenario: lake-secret-forbid-before-start — a Lake a forbid names is held before its engine gets the key, and its
// engine runs with the key once the forbid is deleted (ADR-0321 Decision 5).
//
// oracle: docs/design/lake/secret-forbid.fizz#NoEngineStartsWhileForbidden
// oracle: docs/design/lake/secret-forbid.fizz#AHeldLakeComesBack
func TestScenarioLakeSecretForbidBeforeStart(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.applyPolicy(t, "no-quack-for-lake", forbidRead(`Lake::"default/lake"`, "quack"))
	r.applyQuackLake(t)
	require.Eventually(t, func() bool { reason, _ := r.lakeReady(t); return reason == "BindingResolveFailed" }, policyWait, policyTick)
	_, msg := r.lakeReady(t)
	require.Contains(t, msg, "forbidden by policy default/no-quack-for-lake")
	require.False(t, lakeEngineHasKey(r), "the engine never got the key")

	r.del(t, v1.KindPolicy, "no-quack-for-lake")
	require.Eventually(t, func() bool { return lakeEngineHasKey(r) }, policyWait, policyTick, "the engine runs with the key")
}

// scenario: lake-secret-forbid-while-running — a forbid applied while the Lake's engine runs holds the Lake at a later
// pass, which stops the engine (ADR-0321 Decision 5).
//
// oracle: docs/design/lake/secret-forbid.fizz#AForbidReachesARunningEngine
func TestScenarioLakeSecretForbidWhileRunning(t *testing.T) {
	t.Parallel()
	r, engine := newPolicyRig(t)
	r.applyQuackLake(t)
	require.Eventually(t, func() bool { return lakeEngineHasKey(r) }, policyWait, policyTick, "the engine runs with the key")
	stopsBefore := engine.teardowns.Load()

	r.applyPolicy(t, "no-quack-for-lake", forbidRead(`Lake::"default/lake"`, "quack"))
	require.Eventually(t, func() bool { reason, _ := r.lakeReady(t); return reason == "BindingResolveFailed" }, policyWait, policyTick)
	require.Greater(t, engine.teardowns.Load(), stopsBefore, "the hold stops the running engine")
}

// A person's read of a Secret through the API stays RBAC's (ADR-0321 Decision 6): a forbid on every secret::read of the
// namespace does not reach it. The API half of scenario secret-backup-and-api-unchanged, whose backup half is
// internal/secrets' TestScenarioSecretBackupAndAPIUnchanged.
func TestSecretAPIReadStaysRBACUnderAPolicy(t *testing.T) {
	t.Parallel()
	r, _ := newPolicyRig(t)
	r.applyPolicy(t, "no-secrets", `forbid(principal, action == Action::"secret::read", resource);`)
	obj, err := r.c.Get(context.Background(), v1.KindSecret, "default", "api-keys")
	require.NoError(t, err)
	require.Equal(t, "sk", string(obj.(*v1.Secret).Spec.Data["STRIPE_KEY"]))
}
