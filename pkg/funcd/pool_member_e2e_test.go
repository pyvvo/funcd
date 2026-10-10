//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// poolLang is one shim language a pool member scenario runs on, with its handler sources.
type poolLang struct {
	runtime, ext, python string
	kvWrite, kvRead      string // put "v" at key k of alias t; return {value} read from it
	creds                string // return {ak, sk} from the S3 env
	hello, quiet         string // log "hello-a" through Path B; log nothing
	noHandle             string // export no handle
	dbEcho               string // return {db} from the env
}

func (l poolLang) fn(src string) shimFn { return shimFn{runtime: l.runtime, ext: l.ext, src: src} }

func nodePoolLang() poolLang {
	return poolLang{
		runtime:  "nodejs22",
		ext:      ".mjs",
		kvWrite:  "export async function handle(ctx) { await ctx.kv.put('t', 'k', 'v'); return { ok: true }; }\n",
		kvRead:   "export async function handle(ctx) { return { value: await ctx.kv.getText('t', 'k') }; }\n",
		creds:    "export function handle() { return { ak: process.env.AWS_ACCESS_KEY_ID ?? '', sk: process.env.AWS_SECRET_ACCESS_KEY ?? '' }; }\n",
		hello:    "export function handle() { console.log('hello-a'); return { ok: true }; }\n",
		quiet:    "export function handle() { return { ok: true }; }\n",
		noHandle: "export function other() { return {}; }\n",
		dbEcho:   "export function handle() { return { db: process.env.DB_PASS ?? '' }; }\n",
	}
}

func pythonPoolLang() poolLang {
	return poolLang{
		runtime:  "python314",
		ext:      ".py",
		kvWrite:  "def handle(ctx, event):\n    ctx.kv.put(\"t\", \"k\", \"v\")\n    return {\"ok\": True}\n",
		kvRead:   "def handle(ctx, event):\n    return {\"value\": ctx.kv.get_str(\"t\", \"k\")}\n",
		creds:    "import os\n\n\ndef handle(ctx, event):\n    return {\"ak\": os.environ.get(\"AWS_ACCESS_KEY_ID\", \"\"), \"sk\": os.environ.get(\"AWS_SECRET_ACCESS_KEY\", \"\")}\n",
		hello:    "import logging\n\n\ndef handle(ctx, event):\n    logging.info(\"hello-a\")\n    return {\"ok\": True}\n",
		quiet:    "def handle(ctx, event):\n    return {\"ok\": True}\n",
		noHandle: "def other(ctx, event):\n    return {}\n",
		dbEcho:   "import os\n\n\ndef handle(ctx, event):\n    return {\"db\": os.environ.get(\"DB_PASS\", \"\")}\n",
	}
}

// forPoolLangs runs body once on nodejs22 and once on python314.
func forPoolLangs(t *testing.T, body func(t *testing.T, l poolLang)) {
	t.Helper()
	t.Run("nodejs22", func(t *testing.T) { body(t, nodePoolLang()) })
	t.Run("python314", func(t *testing.T) {
		l := pythonPoolLang()
		l.python = requirePython(t, true)
		body(t, l)
	})
}

// applyObj applies obj in default/rg1.
func (h *shimRig) applyObj(t *testing.T, obj v1.Object) {
	t.Helper()
	m := obj.GetObjectMeta()
	m.Namespace, m.ResourceGroup = "default", "rg1"
	_, err := h.c.Apply(context.Background(), obj)
	require.NoError(t, err)
}

// function reads Function name.
func (h *shimRig) function(t *testing.T, name string) *v1.Function {
	t.Helper()
	obj, err := h.c.Get(context.Background(), v1.KindFunction, "default", v1.ObjectName(name))
	require.NoError(t, err)
	return obj.(*v1.Function)
}

// memberReply holds the fields the pool member handlers reply with.
type memberReply struct {
	OK    bool   `json:"ok"`
	Value string `json:"value"`
	AK    string `json:"ak"`
	SK    string `json:"sk"`
	DB    string `json:"db"`
}

// call invokes name and decodes its 200 reply.
func (h *shimRig) call(t *testing.T, name string) memberReply {
	t.Helper()
	r := h.post(name, `{}`)
	require.Equalf(t, http.StatusOK, r.status, "%s: %s", name, r.body)
	var out memberReply
	require.NoErrorf(t, json.Unmarshal([]byte(r.body), &out), "%s replies JSON: %s", name, r.body)
	return out
}

// samePool requires names to share one non-empty status.pool and returns it.
func (h *shimRig) samePool(t *testing.T, names ...string) string {
	t.Helper()
	pool := h.function(t, names[0]).Status.Pool
	require.NotEmpty(t, pool, "%s is pooled", names[0])
	for _, n := range names[1:] {
		require.Equal(t, pool, h.function(t, n).Status.Pool, "%s shares %s's pool", n, names[0])
	}
	return pool
}

func bindTable(fn *v1.Function) { fn.Spec.KV = []v1.FunctionKV{{Alias: "t", Store: "s", Table: "t"}} }

func ownedKVStore(owner v1.ObjectName) *v1.KVStore {
	obj, _ := v1.NewObject(v1.KindKVStore)
	s := obj.(*v1.KVStore)
	s.Name = "s"
	s.Spec.Tables = []v1.KVTable{{Name: "t", Owner: owner}}
	return s
}

// scenario: pooled-member-kv — a and b share a pool and bind table t, which neither owns (a solo w owns and fills
// it); a call of each reads t's value through context.kv on the pool socket.
func TestScenarioPooledMemberKV(t *testing.T) {
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		h := newShimRig(t, l.python)
		h.deploy(t, "w", l.fn(l.kvWrite).with(bindTable))
		h.applyObj(t, ownedKVStore("w"))
		for _, n := range []string{"a", "b"} {
			h.deploy(t, n, l.fn(l.kvRead).pooled("agents").with(bindTable))
		}
		waitReady(t, h.c, "w", "a", "b")
		h.samePool(t, "a", "b")

		require.True(t, h.call(t, "w").OK)
		for _, n := range []string{"a", "b"} {
			require.Equal(t, "v", h.call(t, n).Value, "%s reads t through its binding", n)
		}
	})
}

// newS3ShimRig is a shim rig whose platform also serves the S3 gateway, with master as the node master secret. It
// returns the gateway's address.
func newS3ShimRig(t *testing.T, python, master string) (*shimRig, string) {
	t.Helper()
	masterFile := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(masterFile, []byte(master), 0o600))
	var h *shimRig
	p, addr := funcd.StartWithS3Gateway(t, funcd.FreeLoopbackAddr, func(addr string, logger funcd.Option) (*funcd.Platform, error) {
		var opts []funcd.Option
		h, opts = shimRigBase(t, python)
		return funcd.New(append(opts, funcd.WithS3Gateway(addr, "", 0, masterFile, shortDataDir(t)), logger)...)
	})
	h.connect(t, p)
	return h, addr
}

// scenario: pooled-member-s3-identity — pooled a and b bind prefix raw of Bucket lake, owned by neither: each
// handler's env holds its own keypair, derived for kind Function and its name, and each keypair reads raw/x through
// the S3 gateway.
func TestScenarioPooledMemberS3Identity(t *testing.T) {
	const master = "e2e-pool-master-secret-0123456789"
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		h, s3Addr := newS3ShimRig(t, l.python, master)
		ctx := context.Background()
		require.NoError(t, h.bucket.Put(ctx, "s3/default/lake/raw/x", []byte("raw-x"), blob.PutOptions{}))
		obj, _ := v1.NewObject(v1.KindBucket)
		b := obj.(*v1.Bucket)
		b.Name = "lake"
		b.Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}}
		h.applyObj(t, b)
		for _, n := range []string{"a", "b"} {
			h.deploy(t, n, l.fn(l.creds).pooled("s3").with(func(fn *v1.Function) {
				fn.Spec.Blob = []v1.FunctionBlob{{Alias: "raw", Bucket: "lake", Prefix: "raw"}}
			}))
		}
		waitReady(t, h.c, "a", "b")
		h.samePool(t, "a", "b")

		for _, n := range []string{"a", "b"} {
			got := h.call(t, n)
			kp := s3gateway.DeriveKeypair([]byte(master), v1.KindFunction, "default", n)
			require.Equal(t, kp.AccessKey, got.AK, "%s holds its own access key", n)
			require.Equal(t, kp.SecretKey, got.SK, "%s holds its own secret key", n)
			out, err := s3Client(t, "http://"+s3Addr, got.AK, got.SK).GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("lake"), Key: aws.String("raw/x")})
			require.NoError(t, err, "%s reads raw/x with its keypair", n)
			_ = out.Body.Close()
		}
	})
}

// scenario: pooled-member-logs — a logs hello-a through Path B in a pool it shares with b: a's logs hold it and a's
// span is stored under a; b's logs do not, and nothing is stored under a pool worker's name.
func TestScenarioPooledMemberLogs(t *testing.T) {
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		h := newShimRig(t, l.python)
		h.deploy(t, "a", l.fn(l.hello).pooled("logs"))
		h.deploy(t, "b", l.fn(l.quiet).pooled("logs"))
		waitReady(t, h.c, "a", "b")
		h.samePool(t, "a", "b")

		require.True(t, h.call(t, "a").OK)
		require.True(t, h.call(t, "b").OK)
		h.logLine(t, "a", func(line logread.Line) bool { return line.Body == "hello-a" })
		ctx := context.Background()
		require.Eventually(t, func() bool {
			objs, err := h.bucket.List(ctx, "traces/default/a/")
			return err == nil && len(objs) > 0
		}, 15*time.Second, 200*time.Millisecond, "a's span is stored under a")

		lines, err := h.c.Logs(ctx, "default", "b", sdk.LogsOptions{})
		require.NoError(t, err)
		for _, line := range lines {
			require.NotEqual(t, "hello-a", line.Body, "a's record is not b's")
		}
		for _, prefix := range []string{"logs/default/", "traces/default/"} {
			objs, err := h.bucket.List(ctx, prefix)
			require.NoError(t, err)
			for _, o := range objs {
				require.NotContains(t, o.Key, "__pool__", "nothing is stored under a pool worker's name")
			}
		}
	})
}

// scenario: pool-member-load-failure — of pooled a, b and c, b exports no handle: b is Failed with ShapeInvalid and
// its load error, a and c are Ready and answer, and once the pool's manifest holds all three the pool process keeps
// its PID across two supervision periods.
func TestScenarioPoolMemberLoadFailure(t *testing.T) {
	t.Parallel()
	const supervision = time.Second
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		t.Parallel()
		manifests := shortDataDir(t)
		h := newShimRig(t, l.python, funcd.WithPoolManifestDir(manifests),
			funcd.WithPacing(funcd.Pacing{SupervisionPeriod: supervision}))
		h.deploy(t, "a", l.fn(l.quiet).pooled("mixed"))
		h.deploy(t, "b", l.fn(l.noHandle).pooled("mixed"))
		h.deploy(t, "c", l.fn(l.quiet).pooled("mixed"))
		waitReady(t, h.c, "a", "c")
		require.Eventually(t, func() bool { return h.function(t, "b").Status.Phase == v1.PhaseFailed },
			30*time.Second, 100*time.Millisecond, "b fails alone")

		b := h.function(t, "b")
		cond := func(typ v1.ConditionType) v1.Condition {
			c, ok := b.Status.Conditions.Get(typ)
			require.True(t, ok, "b has condition %s", typ)
			return c
		}
		require.Equal(t, v1.ConditionFalse, cond("Ready").Status)
		require.Equal(t, "ShapeInvalid", cond("Ready").Reason)
		require.Equal(t, v1.ConditionFalse, cond("ShapeValid").Status)
		require.NotEmpty(t, cond("ShapeValid").Message, "ShapeValid carries b's load error")
		require.Equal(t, v1.ConditionFalse, cond("RevisionReady").Status)
		require.Equal(t, "ShapeInvalid", cond("RevisionReady").Reason)
		for _, n := range []string{"a", "c"} {
			require.True(t, h.call(t, n).OK, n)
		}

		require.Eventually(t, func() bool { return manifestHolds(manifests, "a", "b", "c") },
			15*time.Second, 100*time.Millisecond, "the pool's manifest holds a, b and c")
		pid := h.poolPID(t, l.runtime, "mixed")
		require.NotZero(t, pid)
		require.Never(t, func() bool { return h.poolPID(t, l.runtime, "mixed") != pid },
			2*supervision+time.Second, 250*time.Millisecond, "a member's load failure never restarts the pool")
	})
}

// Issue #70: of pooled a and b, b redeployed to a handler that cannot load is Failed with ShapeInvalid for its new
// revision once the pool switches to the rebuilt worker, runtime.bootTimeout after it listened (ADR-0224 Decision 3),
// and a answers in the rebuilt pool.
func TestIssue70_PooledRedeployToUnloadableHandlerIsShapeInvalid(t *testing.T) {
	t.Parallel()
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		t.Parallel()
		h := newShimRig(t, l.python, funcd.WithPacing(funcd.Pacing{BootTimeout: 3 * time.Second, ActivationTimeout: time.Second}))
		h.deploy(t, "a", l.fn(l.quiet).pooled("redeploy"))
		h.deploy(t, "b", l.fn(l.quiet).pooled("redeploy"))
		waitReady(t, h.c, "a", "b")

		h.deploy(t, "b", l.fn(l.noHandle).pooled("redeploy"))
		require.Eventually(t, func() bool { return h.function(t, "b").Status.Phase == v1.PhaseFailed },
			30*time.Second, 100*time.Millisecond, "b's new revision cannot load")
		b := h.function(t, "b")
		for _, typ := range []v1.ConditionType{"Ready", "ShapeValid", "RevisionReady"} {
			c, ok := b.Status.Conditions.Get(typ)
			require.True(t, ok, "b has condition %s", typ)
			require.Equal(t, v1.ConditionFalse, c.Status, typ)
			require.Equal(t, "ShapeInvalid", c.Reason, typ)
		}
		require.Eventually(t, func() bool { return h.post("a", `{}`).status == http.StatusOK },
			30*time.Second, 200*time.Millisecond, "a answers in the rebuilt pool")
	})
}

// manifestHolds reports whether a pool manifest in dir names every member.
func manifestHolds(dir string, members ...string) bool {
	files, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			continue
		}
		var rows []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &rows) != nil {
			continue
		}
		names := map[string]bool{}
		for _, r := range rows {
			names[r.Name] = true
		}
		all := true
		for _, m := range members {
			all = all && names[m]
		}
		if all {
			return true
		}
	}
	return false
}

// poolPID is the PID of the running pool worker of worker id worker on runtime rt, or 0.
func (h *shimRig) poolPID(t *testing.T, rt, worker string) int {
	t.Helper()
	insts, err := h.rt.List(context.Background(), "default")
	require.NoError(t, err)
	for _, in := range insts {
		if strings.HasPrefix(string(in.Name), "__pool__"+rt+"__"+worker+"__") && in.State == runtime.StateRunning {
			return in.PID
		}
	}
	return 0
}

// settledPoolPID waits until the pool of worker id worker on runtime rt runs one pool worker and returns its PID. The
// first bring-up builds a worker for a's manifest, then one for a and b's beside it, and drains the first once the
// second serves, so a PID read before then can be the drained one's.
func (h *shimRig) settledPoolPID(t *testing.T, rt, worker string) int {
	t.Helper()
	var pid int
	require.Eventually(t, func() bool {
		insts, err := h.rt.List(context.Background(), "default")
		require.NoError(t, err)
		running := 0
		for _, in := range insts {
			if strings.HasPrefix(string(in.Name), "__pool__"+rt+"__"+worker+"__") && in.State == runtime.StateRunning {
				running++
				pid = in.PID
			}
		}
		return running == 1
	}, 20*time.Second, 50*time.Millisecond, "the pool settles on one worker")
	return pid
}

// scenario: workflow-steps-split-by-secrets — in a default-shared Workflow, step s1 binds Secret db and s2 none:
// both are Ready, only s1's env holds db's key, and their status.pool differ.
func TestScenarioWorkflowStepsSplitBySecrets(t *testing.T) {
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		h := newShimRig(t, l.python)
		obj, _ := v1.NewObject(v1.KindSecret)
		sec := obj.(*v1.Secret)
		sec.Name = "db"
		sec.Spec.Data = map[string][]byte{"DB_PASS": []byte("pw")}
		h.applyObj(t, sec)

		src := t.TempDir()
		file := filepath.Join(src, "step"+l.ext)
		require.NoError(t, os.WriteFile(file, []byte(l.dbEcho), 0o600))
		ref := "oci-layout://" + h.layout + ":step"
		_, err := artifact.Push(context.Background(), ref, file, nil, l.runtime, "")
		require.NoError(t, err)

		obj, _ = v1.NewObject(v1.KindWorkflow)
		wf := obj.(*v1.Workflow)
		wf.Name = "wf"
		wf.Spec.Pooling.MinReplicas = 1
		wf.Spec.Steps = []v1.WorkflowStep{
			{Name: "s1", Function: &v1.FunctionStep{Image: ref, Secrets: []v1.ObjectName{"db"}}},
			{Name: "s2", Function: &v1.FunctionStep{Image: ref}},
		}
		h.applyObj(t, wf)
		waitMaterializedReady(t, h.c, "wf-s1", "wf-s2")

		s1, s2 := h.function(t, "wf-s1"), h.function(t, "wf-s2")
		require.NotEmpty(t, s1.Status.Pool, "default-shared steps are pooled")
		require.NotEmpty(t, s2.Status.Pool, "default-shared steps are pooled")
		require.NotEqual(t, s1.Status.Pool, s2.Status.Pool, "different secrets, different pools")
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			var got memberReply
			r := h.post("wf-s1", `{}`)
			assert.Equal(c, http.StatusOK, r.status, r.body)
			assert.NoError(c, json.Unmarshal([]byte(r.body), &got))
			assert.Equal(c, "pw", got.DB, "s1's env holds db's key")
		}, 15*time.Second, 200*time.Millisecond)
		require.Empty(t, h.call(t, "wf-s2").DB, "s2's env does not hold db's key")
	})
}
