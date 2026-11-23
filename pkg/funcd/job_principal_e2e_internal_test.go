//go:build e2e

package funcd

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	lakegw "github.com/pyvvo/funcd/internal/lake/gateway"
	addonsvc "github.com/pyvvo/funcd/internal/services/addon"
	"github.com/pyvvo/funcd/internal/testkit/bound"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// The scenarios of ADR-0351 on the process driver and the real Node shim: a run of Job tofu and Function tofu, both of
// namespace default, call the same facets, proxies and gateway, each as itself. job-egress-named runs on the egress
// lane (e2e/egress.venom.yml).

const jobMaster = "e2e-job-principal-master-0123456789"

// principalHandler runs the ops of its event's data in order and answers what each gave: a value, or its error's
// message. A run's ops are its input, a Function's its call's body.
const principalHandler = `import { existsSync, writeFileSync } from 'node:fs';
export async function handle(ctx, event) {
  const out = [];
  for (const o of (event.data && event.data.ops) || []) {
    try {
      let v = null;
      switch (o.op) {
        case 'blob.put': await ctx.blob.put(o.alias, o.key, new TextEncoder().encode(o.value)); break;
        case 'kv.put': await ctx.kv.put(o.alias, o.key, o.value); break;
        case 'secret.put': await ctx.secret.put(o.alias, o.key, o.value); break;
        case 'secret.get': v = (await ctx.secret.get(o.key)) ?? null; break;
        case 'secret.denied':
          for (let i = 0; i < 200; i++) { await ctx.secret.get(o.key); await new Promise((r) => setTimeout(r, 25)); }
          break;
        case 'env': v = process.env[o.key] ?? null; break;
        case 'mark':
          writeFileSync(o.key + '/' + ctx.run.name, JSON.stringify({ run: ctx.run.name, socket: process.env.FUNCD_INVOKE_SOCKET }));
          break;
        case 'wait': while (!existsSync(o.key)) await new Promise((r) => setTimeout(r, 20)); break;
      }
      out.push({ ok: true, value: v });
    } catch (e) { out.push({ error: String((e && e.message) || e) }); }
  }
  return { out };
}
`

type jobOp map[string]string

type opResult struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error string          `json:"error"`
}

func (o opResult) text() string {
	var s string
	_ = json.Unmarshal(o.Value, &s)
	return s
}

func opsBody(ops ...jobOp) string {
	b, _ := json.Marshal(map[string][]jobOp{"ops": ops})
	return string(b)
}

func opResults(t *testing.T, body []byte) []opResult {
	t.Helper()
	var out struct {
		Out []opResult `json:"out"`
	}
	require.NoError(t, json.Unmarshal(body, &out), "%s", body)
	return out.Out
}

// PushJob pushes src as funcdctl push does, with the {input, output} contract ("" is {}), under a tag of its own in
// layout, and returns its reference and digest.
func PushJob(t *testing.T, layout, name, src, input string) (string, string) {
	t.Helper()
	side := func(s string) []byte {
		if s == "" {
			s = `{}`
		}
		return []byte(s)
	}
	contractBlob, err := artifact.ContractBlob(side(input), side(""))
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), name+".mjs")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o600))
	ref := "oci-layout://" + layout + ":" + name + "-" + fmt.Sprint(time.Now().UnixNano())
	digest, err := artifact.Push(context.Background(), ref, file, contractBlob, "", "")
	require.NoError(t, err)
	return ref, digest
}

// jobRig is a platform with the Node shim, a stand-in Lake engine and add-on runtime and, with s3, the S3 gateway, on
// a node master the test knows.
type jobRig struct {
	p      *Platform
	c      *sdk.Client
	layout string
	s3     string
}

func newJobRig(t *testing.T, s3 bool) *jobRig {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	engine := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(engine.Close)
	dir, err := os.MkdirTemp("", "funcd") // not t.TempDir(): a socket path under it would overrun the limit (#41)
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	opts := []Option{InMemory(), WithoutLogCompaction(), WithRuntimeShim(node, langmod.NodeShim(t)),
		WithArtifactStore(dir), WithMasterSecret([]byte(jobMaster)),
		WithLakeProviderRuntime(&toggleEngine{addr: strings.TrimPrefix(engine.URL, "http://")}),
		WithAddonRuntime(newAddonRuntime(t), "containerd"), WithAddonRegistries("ghcr.io"),
		WithCredentials(Credential{Token: addonAdminToken, Role: "admin"}), WithPacing(asleepPacing(50 * time.Millisecond))}
	r := &jobRig{layout: t.TempDir()}
	if s3 {
		s3Dir := t.TempDir()
		r.p, r.s3 = StartWithS3Gateway(t, FreeLoopbackAddr, func(addr string, logger Option) (*Platform, error) {
			return New(append(opts, WithS3Gateway(addr, "", 0, "", s3Dir), logger)...)
		})
	} else {
		r.p, err = New(opts...)
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- r.p.Run(ctx) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	r.c, err = sdk.New("http://"+r.p.Addr(), sdk.WithToken(addonAdminToken))
	require.NoError(t, err)
	return r
}

func (r *jobRig) apply(t *testing.T, obj v1.Object) {
	t.Helper()
	_, err := r.c.Apply(context.Background(), obj)
	require.NoError(t, err, "apply %s %s", obj.GroupVersionKind().Kind, obj.GetName())
}

func (r *jobRig) get(t *testing.T, kind v1.Kind, name v1.ObjectName) v1.Object {
	t.Helper()
	obj, err := r.p.cfg.store.Get(context.Background(), kind.GVK(), "default", name)
	require.NoError(t, err)
	return obj
}

// job applies Job tofu running principalHandler, as edit binds it, and waits until it is Ready.
func (r *jobRig) job(t *testing.T, edit func(*v1.JobSpec)) {
	t.Helper()
	ref, digest := PushJob(t, r.layout, "tofu", principalHandler, "")
	job := &v1.Job{TypeMeta: tmeta(v1.KindJob), ObjectMeta: v1.ObjectMeta{Name: "tofu", Namespace: "default", ResourceGroup: "rg1"}}
	job.Spec.Runtime, job.Spec.Handler, job.Spec.Image, job.Spec.ImageDigest = "nodejs22", "handle", ref, digest
	if edit != nil {
		edit(&job.Spec)
	}
	r.apply(t, job)
	require.Eventually(t, func() bool {
		j := r.get(t, v1.KindJob, "tofu").(*v1.Job)
		c, ok := j.Status.Conditions.Get("Ready")
		return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == j.Generation
	}, 10*time.Second, 20*time.Millisecond, "Job tofu is Ready")
}

// fn applies Function tofu running principalHandler, as edit binds it, and waits until it is Ready.
func (r *jobRig) fn(t *testing.T, edit func(*v1.Function)) {
	t.Helper()
	ref, digest := PushJob(t, r.layout, "tofu-fn", principalHandler, "")
	fn := &v1.Function{TypeMeta: tmeta(v1.KindFunction)}
	fn.Name, fn.Namespace, fn.ResourceGroup = "tofu", "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler, fn.Spec.Image, fn.Spec.ImageDigest = "nodejs22", "handle", ref, digest
	fn.Spec.Replicas, fn.Spec.Scaling.MinReplicas = 1, 1
	if edit != nil {
		edit(fn)
	}
	bound.Declare(fn)
	r.apply(t, fn)
	var last *v1.Function
	if !assert.Eventually(t, func() bool {
		last = r.get(t, v1.KindFunction, "tofu").(*v1.Function)
		return last.Status.Phase == v1.PhaseReady
	}, 10*time.Second, 20*time.Millisecond) {
		require.FailNow(t, "Function tofu is not Ready", "%+v", last.Status)
	}
}

// call posts ops to Function tofu and returns what each gave.
func (r *jobRig) call(t *testing.T, ops ...jobOp) []opResult {
	t.Helper()
	resp, err := http.Post("http://"+r.p.DataPlaneAddr()+"/function/tofu", "application/json", strings.NewReader(opsBody(ops...)))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", body)
	return opResults(t, body)
}

// start creates a run of Job tofu whose input is ops and returns its name.
func (r *jobRig) start(t *testing.T, ops ...jobOp) v1.ObjectName {
	t.Helper()
	run := &v1.JobRun{TypeMeta: tmeta(v1.KindJobRun),
		ObjectMeta: v1.ObjectMeta{GenerateName: "tofu-", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.JobRunSpec{Job: "tofu", Input: json.RawMessage(opsBody(ops...))}}
	created, err := r.c.Create(context.Background(), run)
	require.NoError(t, err)
	return created.GetName()
}

// result waits until run name has ended Succeeded and returns what each of its ops gave.
func (r *jobRig) result(t *testing.T, name v1.ObjectName) []opResult {
	t.Helper()
	var run *v1.JobRun
	if !assert.Eventually(t, func() bool {
		run = r.get(t, v1.KindJobRun, name).(*v1.JobRun)
		return run.Status.Phase == v1.JobRunSucceeded || run.Status.Phase == v1.JobRunFailed ||
			run.Status.Phase == v1.JobRunCancelled
	}, 15*time.Second, 20*time.Millisecond) {
		require.FailNow(t, "the run did not end", "%s: %+v", name, run.Status)
	}
	require.Equal(t, v1.JobRunSucceeded, run.Status.Phase, "run %s: %+v", name, run.Status.Conditions)
	return opResults(t, run.Status.Result)
}

func (r *jobRig) run(t *testing.T, ops ...jobOp) []opResult {
	t.Helper()
	return r.result(t, r.start(t, ops...))
}

func (r *jobRig) bucket(t *testing.T, name, prefix v1.ObjectName, owner v1.WorkloadRef) {
	t.Helper()
	b := &v1.Bucket{TypeMeta: tmeta(v1.KindBucket), ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: string(prefix), Owner: owner}}}}
	r.apply(t, b)
}

func (r *jobRig) secret(t *testing.T, name v1.ObjectName, owner v1.WorkloadRef, key, value string) {
	t.Helper()
	s := &v1.Secret{TypeMeta: tmeta(v1.KindSecret), ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.SecretSpec{Owner: owner, Data: map[string][]byte{key: []byte(value)}}}
	r.apply(t, s)
}

// tfState binds prefix state of Bucket tfstate as tf.
func tfState() []v1.FunctionBlob {
	return []v1.FunctionBlob{{Alias: "tf", Bucket: "tfstate", Prefix: "state"}}
}

// scenario: job-writes-as-itself — a run of Job tofu writes the prefix that the Job owns; Function tofu, which binds
// it too, gets 403 on the same write (ADR-0351 Decisions 1 and 3).
func TestScenarioJobWritesAsItself(t *testing.T) {
	t.Parallel()
	r := newJobRig(t, false)
	r.bucket(t, "tfstate", "state", v1.JobOwner("tofu"))
	r.job(t, func(s *v1.JobSpec) { s.Bindings.Blob, s.Buckets = tfState(), []v1.BucketEntry{{Ref: "tfstate"}} })
	r.fn(t, func(fn *v1.Function) { fn.Spec.Bindings.Blob = tfState() })

	put := jobOp{"op": "blob.put", "alias": "tf", "key": "terraform.tfstate", "value": "{}"}
	got := r.run(t, put)
	require.True(t, got[0].OK, "the run writes as the Job: %s", got[0].Error)
	got = r.call(t, put)
	require.Contains(t, got[0].Error, "403", "the Function of the Job's name is not its owner")
}

// scenario: job-writes-its-secret — a run writes the Secret that the Job owns, and gets 403 on the one that Function
// tofu owns; Function tofu gets 403 on the Job's (ADR-0351 Decision 3, ADR-0327).
func TestScenarioJobWritesItsSecret(t *testing.T) {
	t.Parallel()
	r := newJobRig(t, false)
	r.secret(t, "creds", v1.JobOwner("tofu"), "TOKEN", "t1")
	r.secret(t, "fn-creds", v1.FunctionOwner("tofu"), "TOKEN", "f1")
	names := []v1.ObjectName{"creds", "fn-creds"}
	decls := []v1.SecretDeclaration{{Name: "creds", Keys: []string{"TOKEN"}}, {Name: "fn-creds", Keys: []string{"TOKEN"}}}
	r.job(t, func(s *v1.JobSpec) { s.Bindings.Secret, s.SecretDeclarations = names, decls })
	r.fn(t, func(fn *v1.Function) { fn.Spec.Bindings.Secret, fn.Spec.SecretDeclarations = names, decls })

	got := r.run(t, jobOp{"op": "secret.put", "alias": "creds", "key": "TOKEN", "value": "t2"},
		jobOp{"op": "secret.put", "alias": "fn-creds", "key": "TOKEN", "value": "x"})
	require.True(t, got[0].OK, "the run writes the Job's Secret: %s", got[0].Error)
	require.Contains(t, got[1].Error, "403", "the run writes no Secret its namesake owns")
	got = r.call(t, jobOp{"op": "secret.put", "alias": "creds", "key": "TOKEN", "value": "x"})
	require.Contains(t, got[0].Error, "403", "the Function of the Job's name writes no Secret the Job owns")
	require.Equal(t, "t2", string(r.get(t, v1.KindSecret, "creds").(*v1.Secret).Spec.Data["TOKEN"]))
	require.Equal(t, "f1", string(r.get(t, v1.KindSecret, "fn-creds").(*v1.Secret).Spec.Data["TOKEN"]))
}

// scenario: job-policy-forbid — under a Policy that forbids secret::read to Job::"default/tofu", the run's
// ctx.secret.get answers 403 and Function tofu still reads (ADR-0351 Decision 1). The forbid lands once the run's
// worker is ready and in its handler: a run that starts under it never gets ready, as its readiness checks its bound
// Secrets (ADR-0215), and ends StartFailed.
func TestScenarioJobPolicyForbid(t *testing.T) {
	t.Parallel()
	r := newJobRig(t, false)
	r.secret(t, "api-keys", v1.WorkloadRef{}, "KEY", "sk")
	decls := []v1.SecretDeclaration{{Name: "api-keys", Keys: []string{"KEY"}}}
	names := []v1.ObjectName{"api-keys"}
	r.job(t, func(s *v1.JobSpec) { s.Bindings.Secret, s.SecretDeclarations = names, decls })
	r.fn(t, func(fn *v1.Function) { fn.Spec.Bindings.Secret, fn.Spec.SecretDeclarations = names, decls })

	dir := t.TempDir()
	gate := filepath.Join(dir, "open")
	run := r.start(t, jobOp{"op": "mark", "key": dir}, jobOp{"op": "wait", "key": gate},
		jobOp{"op": "secret.denied", "key": "KEY"})
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, string(run)))
		return err == nil
	}, 10*time.Second, 20*time.Millisecond, "the run is in its handler")
	pol := &v1.Policy{TypeMeta: tmeta(v1.KindPolicy), ObjectMeta: v1.ObjectMeta{Name: "no-tofu-reads", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.PolicySpec{Cedar: `forbid(principal == Job::"default/tofu", action == Action::"secret::read", resource);`}}
	r.apply(t, pol)
	require.NoError(t, os.WriteFile(gate, nil, 0o600))

	got := r.result(t, run)
	require.Contains(t, got[2].Error, "403", "the run's read is forbidden")
	require.Contains(t, got[2].Error, "no-tofu-reads")
	require.Equal(t, "sk", r.call(t, jobOp{"op": "secret.get", "key": "KEY"})[0].text(), "the Function of the Job's name reads")
}

// handshake is a Quack handshake that carries token, the frame the Lake proxy reads it from (ADR-0137).
func handshake(token string) []byte {
	b := make([]byte, 8)
	b = append(b, 0x01, 0x00)
	b = binary.AppendUvarint(b, uint64(len(token)))
	return append(b, token...)
}

// scenario: job-tokens-name-their-kind — the run's Lake and add-on proxy tokens, which name the Job, pass the Lake
// proxy and the add-on proxy for the Lake and the object that only the Job binds; Function tofu's own tokens get 403
// there (ADR-0351 Decision 4).
func TestScenarioJobTokensNameTheirKind(t *testing.T) {
	t.Parallel()
	r := newJobRig(t, false)
	r.bucket(t, "lakehouse", "raw", v1.LakeOwner("lake"))
	r.apply(t, lakeResource("default"))
	r.apply(t, rauthyAddon("rauthy"))
	r.apply(t, identityProvider("default"))
	require.Eventually(t, func() bool {
		lake := r.get(t, v1.KindLake, "lake").(*v1.Lake)
		obj, err := r.p.cfg.store.Get(context.Background(), v1.KindAddonObject.GVK(), "default", "login")
		return lake.Status.Phase == v1.PhaseReady && err == nil && obj.(*v1.AddonObject).Status.Phase == v1.PhaseReady
	}, 10*time.Second, 20*time.Millisecond, "Lake lake and IdentityProvider login are Ready")
	r.job(t, func(s *v1.JobSpec) {
		s.Bindings.Lake, s.Lakes = []v1.FunctionLake{{Alias: "lake", Lake: "lake"}}, []v1.LakeEntry{{Ref: "lake"}}
		s.Bindings.Addon = []v1.FunctionAddon{{Alias: "idp", Object: "login"}}
	})
	r.fn(t, nil)

	var env []string
	for _, o := range r.run(t, jobOp{"op": "env", "key": "FUNCD_LAKE_LAKE_URL"}, jobOp{"op": "env", "key": "FUNCD_LAKE_LAKE_TOKEN"},
		jobOp{"op": "env", "key": "FUNCD_ADDON_IDP_URL"}, jobOp{"op": "env", "key": "FUNCD_ADDON_IDP_TOKEN"}) {
		require.NotEmpty(t, o.text(), "%+v", o)
		env = append(env, o.text())
	}
	lakeURL, jobLake, addonURL, jobAddon := "http://"+env[0]+"/", env[1], env[2]+"/auth/v1/x", env[3]
	fnLake, err := lakegw.DeriveLakeToken([]byte(jobMaster), "default", v1.KindFunction, "tofu")
	require.NoError(t, err)
	fnAddon := addonsvc.Token([]byte(jobMaster), "default", v1.KindFunction, "tofu")

	lakeCall := func(token string) int {
		resp, err := http.Post(lakeURL, "application/octet-stream", bytes.NewReader(handshake(token)))
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	addonCall := func(token string) int {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, addonURL, http.NoBody)
		require.NoError(t, err)
		req.Header.Set(local.AddonTokenHeader, token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusOK, lakeCall(jobLake), "the run's lake token passes the Lake's proxy")
	require.Equal(t, http.StatusForbidden, lakeCall(fnLake), "the Function's does not")
	require.Equal(t, http.StatusOK, addonCall(jobAddon), "the run's proxy token reaches the object's engine")
	require.Equal(t, http.StatusForbidden, addonCall(fnAddon), "the Function's does not")
}

// scenario: job-s3-keys — with the S3 gateway on, the run's worker holds the key pair derived for the Job, which writes
// the prefix the Job owns; the pair derived for Function tofu does not open it (ADR-0351 Decision 4).
func TestScenarioJobS3Keys(t *testing.T) {
	t.Parallel()
	r := newJobRig(t, true)
	r.bucket(t, "tfstate", "state", v1.JobOwner("tofu"))
	r.job(t, func(s *v1.JobSpec) { s.Bindings.Blob, s.Buckets = tfState(), []v1.BucketEntry{{Ref: "tfstate"}} })

	got := r.run(t, jobOp{"op": "env", "key": "AWS_ACCESS_KEY_ID"}, jobOp{"op": "env", "key": "AWS_SECRET_ACCESS_KEY"})
	keys := r.p.S3Keys()
	job := keys.Keypair(v1.KindJob, "default", "tofu")
	require.Equal(t, job.AccessKey, got[0].text(), "the run holds the Job's access key")
	require.Equal(t, job.SecretKey, got[1].text())

	client := func(kp s3gateway.Keypair) *awss3.Client {
		cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"),
			awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(kp.AccessKey, kp.SecretKey, "")))
		require.NoError(t, err)
		endpoint := "http://" + r.s3
		return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
			o.BaseEndpoint, o.UsePathStyle, o.RetryMaxAttempts = &endpoint, true, 1
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		})
	}
	ctx := context.Background()
	_, err := client(job).PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("tfstate"), Key: aws.String("state/x"),
		Body: strings.NewReader("{}")})
	require.NoError(t, err, "the Job's pair writes its prefix")
	_, err = client(keys.Keypair(v1.KindFunction, "default", "tofu")).GetObject(ctx,
		&awss3.GetObjectInput{Bucket: aws.String("tfstate"), Key: aws.String("state/x")})
	require.Error(t, err, "the pair of the Function of the Job's name does not open it")
	require.Contains(t, err.Error(), "403")
}

// scenario: job-writer-role — a RolesAssignment that grants KV Data Writer on a table to {kind: Job, name: tofu} lets
// the run write it, and Function tofu, which binds it too, gets 403 (ADR-0351 Decision 5).
func TestScenarioJobWriterRole(t *testing.T) {
	t.Parallel()
	r := newJobRig(t, false)
	r.apply(t, &v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: v1.ObjectMeta{Name: "state", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "runs"}}}})
	ra := &v1.RolesAssignment{TypeMeta: tmeta(v1.KindRolesAssignment),
		ObjectMeta: v1.ObjectMeta{Name: "tofu-writes", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.RolesAssignmentSpec{Assignments: []v1.AssignmentEntry{{Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindJob, Name: "tofu"},
			RoleRef: v1.RoleRef{Kind: v1.RoleRefKindBuiltin, Name: "KV Data Writer"}, Scope: &v1.ScopeRef{Kind: v1.ScopeKindKVStore, Name: "state/runs"}}}}}
	r.apply(t, ra)
	runs := []v1.FunctionKV{{Alias: "runs", Store: "state", Table: "runs"}}
	r.job(t, func(s *v1.JobSpec) { s.Bindings.KV, s.KVStores = runs, []v1.KVStoreEntry{{Ref: "state"}} })
	r.fn(t, func(fn *v1.Function) { fn.Spec.Bindings.KV = runs })

	put := jobOp{"op": "kv.put", "alias": "runs", "key": "last", "value": "v"}
	got := r.run(t, put)
	require.True(t, got[0].OK, "the role lets the run write: %s", got[0].Error)
	require.Contains(t, r.call(t, put)[0].Error, "403", "the role names the Job, not the Function of its name")
}

// The scenario's GET /job/run assertion joins this test with ADR-0348's implementation (ruling: https://github.com/pyvvo/funcd/issues/1018#issuecomment-7116120716).
// scenario: job-runs-have-their-own-sockets — two Running runs of a Job with concurrency 2 hold two different local API
// sockets, and each run's ctx.run names that run (ADR-0351 Decision 2).
func TestScenarioJobRunsHaveTheirOwnSockets(t *testing.T) {
	t.Parallel()
	r := newJobRig(t, false)
	r.job(t, func(s *v1.JobSpec) { s.Concurrency = 2 })

	dir := t.TempDir()
	gate := filepath.Join(dir, "open")
	runs := []v1.ObjectName{r.start(t, jobOp{"op": "mark", "key": dir}, jobOp{"op": "wait", "key": gate}),
		r.start(t, jobOp{"op": "mark", "key": dir}, jobOp{"op": "wait", "key": gate})}
	sockets := map[string]v1.ObjectName{}
	for _, run := range runs {
		var mark struct {
			Run    v1.ObjectName `json:"run"`
			Socket string        `json:"socket"`
		}
		require.Eventually(t, func() bool {
			b, err := os.ReadFile(filepath.Join(dir, string(run))) //nolint:gosec // a test-owned path
			return err == nil && json.Unmarshal(b, &mark) == nil
		}, 10*time.Second, 20*time.Millisecond, "run %s is in its handler", run)
		require.Equal(t, run, mark.Run, "ctx.run names the run")
		require.NotEmpty(t, mark.Socket)
		sockets[mark.Socket] = run
	}
	require.Len(t, sockets, 2, "the two runs hold two sockets: %v", sockets)
	require.NoError(t, os.WriteFile(gate, nil, 0o600))
	for _, run := range runs {
		r.result(t, run)
	}
}
