//go:build dev

package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/testkit/poll"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// requireRuntime skips a test when `funcdctl dev` would find no usable runtime shim to boot the platform,
// even for the S3 / persist lanes that never invoke a handler. It asks devShimOptions itself for the node
// handler these lanes declare, so it skips exactly when startup finds no node interpreter.
func requireRuntime(t *testing.T) {
	t.Helper()
	_, cleanup, err := devShimOptions(context.Background(), "requireRuntime", false, true)
	if fault.KindOf(err) == fault.NotFound {
		t.Skipf("funcdctl dev needs a runtime shim to boot: %v", err)
	}
	require.NoError(t, err)
	cleanup()
}

// okBuild is a build that answers every call.
const okBuild = "export function handle() { return { ok: true }; }\n"

// bucketDoc is Bucket name with one prefix per name, each owned by owner.
func bucketDoc(name, owner string, prefixes ...string) string {
	s := "apiVersion: funcd.io/v1alpha1\nkind: Bucket\nmetadata:\n  name: " + name + "\nspec:\n  prefixes:\n"
	for _, p := range prefixes {
		s += "    - name: " + p + "\n      owner:\n        kind: Function\n        name: " + owner + "\n"
	}
	return s
}

// Issue #431: on a host with no node and a python3 that cannot load the shim, funcdctl dev finds no
// runtime, yet requireRuntime saw python3 on PATH and let the gated tests run into that startup error.
func TestIssue431_RequireRuntimeSkipsWhenPythonCannotLoadShim(t *testing.T) {
	bin := t.TempDir()
	python := filepath.Join(bin, "python3")
	require.NoError(t, os.WriteFile(python, []byte("#!/bin/sh\necho \"SyntaxError: invalid syntax\" >&2\nexit 1\n"), 0o700))
	t.Setenv("PATH", bin)
	t.Setenv("FUNCD_NODE", "")
	t.Setenv("FUNCD_PYTHON", "")

	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, devProject(t, nodeFunction("api", "", okBuild)), devConfig{cacheDir: t.TempDir()})
	t.Cleanup(func() {
		cancel()
		if inst != nil {
			_ = inst.stop()
		}
	})
	require.Equal(t, fault.NotFound, fault.KindOf(err), "funcdctl dev finds no usable runtime on this host: %v", err)

	ran := false
	t.Run("gated", func(t *testing.T) {
		requireRuntime(t)
		ran = true
	})
	require.False(t, ran, "requireRuntime skips a test that funcdctl dev cannot boot")
}

// devS3Client builds a real aws-sdk-go-v2 S3 client (path-style, the printed dev creds, BaseEndpoint = the
// dev S3 endpoint) — exactly what an author's `aws`/`duckdb` uses against `funcdctl dev` (Decision 6).
func devS3Client(t *testing.T, inst *devInstance) *awss3.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(inst.s3Region),
		awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(inst.s3AccessKey, inst.s3SecretKey, "")),
	)
	require.NoError(t, err)
	endpoint := inst.s3Endpoint
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
		// Match the funcd gateway's contract (see the s3gateway harness): a plain signed payload, no
		// streaming CRC32 trailer (versitygw rejects the unsupported trailer with 501).
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
}

// TestIssue432_DevFailsWhenS3PortIsTaken: another process holds the S3 frontend's port. The gateway binds it only
// when the platform runs, so startDev must fail instead of returning an endpoint the gateway never bound.
func TestIssue432_DevFailsWhenS3PortIsTaken(t *testing.T) {
	requireRuntime(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })
	dir := devProject(t, nodeFunction("api", "", okBuild))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, devConfig{s3port: taken.Addr().(*net.TCPAddr).Port, cacheDir: t.TempDir()})
	if err == nil {
		cancel()
		_ = inst.stop()
	}
	require.ErrorIs(t, err, syscall.EADDRINUSE, "funcdctl dev showed an S3 endpoint the gateway never bound")
}

// TestIssue627_DevRepicksTakenS3Port: funcdctl dev picks the S3 frontend's port and releases it, and the gateway binds
// it only when the platform runs, so another process can take it in between. The boot must start again on a fresh
// port instead of failing, reopening the --persist drivers the failed attempt released.
func TestIssue627_DevRepicksTakenS3Port(t *testing.T) {
	requireRuntime(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })
	picks := 0
	cfg := devConfig{persist: true, persistTo: t.TempDir(), pickS3Addr: func() (string, error) {
		picks++
		if picks == 1 {
			return taken.Addr().String(), nil
		}
		return freeLocalAddr()
	}}
	dir := devProject(t, nodeFunction("api", "", okBuild))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, cfg)
	require.NoError(t, err, "the boot starts again when its picked S3 port is taken")
	t.Cleanup(func() { cancel(); _ = inst.stop() })
	require.Equal(t, 2, picks, "the boot picked a fresh port once the first was taken")
	require.NotEqual(t, "http://"+taken.Addr().String(), inst.s3Endpoint)
	_, err = devS3Client(t, inst).ListBuckets(context.Background(), &awss3.ListBucketsInput{})
	require.NoError(t, err, "the S3 frontend serves on the fresh port")
}

// scenario: dev-inspect-blob-via-s3 — a function bound to a bucket writes an object THROUGH the dev S3
// endpoint (it owns the prefix, so an owner-write is allowed), and the object is then readable back over
// the same endpoint (GET + `aws s3 ls`) with the printed dev creds — the same S3 surface prod exposes.
func TestScenarioDevInspectBlobViaS3(t *testing.T) {
	requireRuntime(t)
	dir := devProject(t, merge(nodeFunction("api", "  buckets:\n    - ref: releves\n"+
		"  bindings:\n    blob:\n      - alias: bronze\n        bucket: releves\n        prefix: bronze\n", okBuild),
		map[string]string{"releves.yaml": bucketDoc("releves", "api", "bronze")}))
	inst := runDev(t, dir)
	// startDev applied the Function (its blob binding is the S3 grant) and the Bucket synchronously, so the PEP
	// authorizes the dev keypair at once; this lane never invokes the handler.
	require.NotEmpty(t, inst.s3Endpoint, "the S3 frontend endpoint is printed")
	require.NotEmpty(t, inst.s3AccessKey, "a dev S3 keypair is minted")

	c := devS3Client(t, inst)
	ctx := context.Background()
	bucket, key := "releves", "bronze/report.parquet"
	want := []byte("PAR1-dev-rows")

	// The gateway binds its listener asynchronously (at Run); retry the first write until it is accepting.
	require.Eventually(t, func() bool {
		_, err := c.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: &bucket, Key: &key, Body: bytes.NewReader(want),
		})
		return err == nil
	}, 15*time.Second, 100*time.Millisecond, "the bound function writes its bucket through the dev S3 endpoint")

	out, err := c.GetObject(ctx, &awss3.GetObjectInput{Bucket: &bucket, Key: &key})
	require.NoError(t, err, "the object reads back through the same S3 endpoint")
	defer func() { _ = out.Body.Close() }()
	got, _ := io.ReadAll(out.Body)
	require.Equal(t, want, got, "the same bytes the function wrote through are served")

	prefix := "bronze/"
	list, err := c.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: &bucket, Prefix: &prefix})
	require.NoError(t, err, "aws s3 ls s3://releves/bronze/ lists the object")
	var keys []string
	for _, o := range list.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	require.Contains(t, keys, key, "the written object appears in the listing")
}

// scenario: dev-persist-survives-restart — startDev --persist to a dir, write a KV value + a blob object,
// STOP the platform, startDev --persist again to the SAME dir, and both are still readable (Badger +
// fileblob persistence; no re-seed). Proves Decision 7's durable-local drivers under per-service subdirs.
func TestScenarioDevPersistSurvivesRestart(t *testing.T) {
	requireRuntime(t)
	root := t.TempDir()
	dir := devProject(t, nodeFunction("api", "  kv:\n    - name: cache-kv\n      tables:\n        - name: entries\n"+
		"  bindings:\n    kv:\n      - alias: cache\n        store: cache-kv\n        table: entries\n", okBuild))
	ctx := context.Background()
	const kvKey, blobKey = "greeting", "persist/probe.bin"
	kvVal, blobVal := []byte("bonjour"), []byte("durable-rows")

	a := &cli{out: io.Discard}
	ctx1, cancel1 := context.WithCancel(ctx)
	inst1, err := a.startDev(ctx1, dir, devConfig{persist: true, persistTo: root})
	require.NoError(t, err)
	require.NotNil(t, inst1.kv, "--persist composes a durable KV driver")
	require.NotNil(t, inst1.blob, "--persist composes a durable blob driver")
	require.NoError(t, inst1.kv.Put(ctx, kvKey, kvVal))
	require.NoError(t, inst1.blob.Put(ctx, blobKey, blobVal, blob.PutOptions{}))
	cancel1()
	require.NoError(t, inst1.stop())

	require.DirExists(t, filepath.Join(root, "metastore"))
	require.DirExists(t, filepath.Join(root, "kv"))
	require.DirExists(t, filepath.Join(root, "blob"))

	ctx2, cancel2 := context.WithCancel(ctx)
	inst2, err := a.startDev(ctx2, dir, devConfig{persist: true, persistTo: root})
	require.NoError(t, err)
	t.Cleanup(func() { cancel2(); _ = inst2.stop() })

	gotKV, found, err := inst2.kv.Get(ctx, kvKey)
	require.NoError(t, err)
	require.True(t, found, "the KV value survived the restart")
	require.Equal(t, kvVal, gotKV)

	gotBlob, err := inst2.blob.Get(ctx, blobKey)
	require.NoError(t, err, "the blob object survived the restart")
	require.Equal(t, blobVal, gotBlob)
}

// Issue #1053: a function dev runs from its private bundle serves again after each --persist restart over the same
// state, and keeps its KV data. Each run used to copy the build into a new temp dir and remove it on stop, so the
// restart's rollout failed on the serving Revision's deleted artifact.
func TestIssue1053_PersistRestartServesAFunction(t *testing.T) {
	requireRuntime(t)
	root := t.TempDir()
	dir := devProject(t, nodeFunction("api", "  kv:\n    - name: counts-kv\n      tables:\n        - name: counts\n"+
		"  bindings:\n    kv:\n      - alias: counts\n        store: counts-kv\n        table: counts\n",
		"export async function handle(ctx) {\n"+
			"  const n = Number((await ctx.kv.getText('counts', 'n')) ?? 0) + 1;\n"+
			"  await ctx.kv.put('counts', 'n', String(n));\n  return { n };\n}\n"))
	a := &cli{out: io.Discard}
	for boot := 1; boot <= 2; boot++ {
		ctx, cancel := context.WithCancel(context.Background())
		inst, err := a.startDev(ctx, dir, devConfig{persist: true, persistTo: root})
		require.NoError(t, err)
		require.Equal(t, boot, addNote(t, inst), "boot %d continues the count the boots before it left", boot)
		cancel()
		require.NoError(t, inst.stop())
	}
}

// Issue #1053: a function keeps its contract across a --persist restart. The platform starts the stored replica as
// soon as it runs, and the contract that the last stop removed must be back by then: dev delivers it before the
// platform starts.
func TestIssue1053_PersistRestartKeepsTheContract(t *testing.T) {
	requireRuntime(t)
	root := t.TempDir()
	files := nodeFunction("api", "", okBuild)
	files["api.contract.yaml"] = "runtime: nodejs22\ninput:\n" +
		"  type: object\n  additionalProperties: false\n  properties:\n    name:\n      type: string\n  required:\n    - name\n" +
		"output:\n  type: object\n  additionalProperties: false\n  properties:\n    ok:\n      type: boolean\n  required:\n    - ok\n"
	dir := devProject(t, files)
	call := func(inst *devInstance, body string) (int, error) {
		resp, err := http.Post(inst.gatewayURL+"/function/api", "application/json", strings.NewReader(body))
		if err != nil {
			return 0, err
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, resp.Body.Close()
	}
	a := &cli{out: io.Discard}
	for boot := 1; boot <= 2; boot++ {
		ctx, cancel := context.WithCancel(context.Background())
		inst, err := a.startDev(ctx, dir, devConfig{persist: true, persistTo: root})
		require.NoError(t, err)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			code, cerr := call(inst, `{"name":"truck-7"}`)
			if assert.NoError(c, cerr) {
				assert.Equal(c, http.StatusOK, code)
			}
		}, 45*time.Second, 200*time.Millisecond, "boot %d serves", boot)
		code, err := call(inst, `{"name":5}`)
		require.NoError(t, err)
		require.Equal(t, http.StatusUnprocessableEntity, code, "boot %d enforces the contract", boot)
		cancel()
		require.NoError(t, inst.stop())
	}
}

// Issue #1053: a second session of the same folder over the same --persist-to fails on the state dir's lock, and the
// bundle of the session that runs, which it never removed, keeps serving and takes an edit.
func TestIssue1053_FailedSecondSessionKeepsTheBundle(t *testing.T) {
	requireRuntime(t)
	root := t.TempDir()
	dir := devProject(t, nodeFunction("api", "", "export function handle() { return { n: 1 }; }\n"))
	a := &cli{out: io.Discard}
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := a.startDev(ctx, dir, devConfig{persist: true, persistTo: root})
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = inst.stop() })
	require.Equal(t, 1, addNote(t, inst))

	_, err = a.startDev(context.Background(), dir, devConfig{persist: true, persistTo: root})
	require.Error(t, err, "a second session over the same --persist-to")
	bundles, err := filepath.Glob(filepath.Join(root, "bundles", "api-*", "api.mjs"))
	require.NoError(t, err)
	require.Len(t, bundles, 1, "the running session's bundle stays")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "api.mjs"), []byte("export function handle() { return { n: 2, edited: true }; }\n"), 0o600))
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		resp, perr := http.Post(inst.gatewayURL+"/function/api", "application/json", strings.NewReader(`{}`))
		if !assert.NoError(c, perr) {
			return
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		assert.Contains(c, string(body), `"n":2`, "the edit is reloaded into the bundle")
	}, 30*time.Second, 200*time.Millisecond)
}

// Issue #1053: a kept bundle (--persist) changes between a copied build and links to its source dir from one run to
// the next: the links replace the copied file, and the copy replaces the link rather than write through it into the
// source.
func TestIssue1053_KeptBundleSwitchesBetweenCopyAndLinks(t *testing.T) {
	src := devProject(t, map[string]string{"api.mjs": "export function handle() { return { n: 1 }; }\n"})
	before, err := os.Stat(filepath.Join(src, "api.mjs"))
	require.NoError(t, err)
	bundle := t.TempDir()
	b := devBuild{name: "api", contract: &sdk.ContractFile{}, srcDir: src, entry: "api.mjs"}
	for _, linked := range []bool{false, true, false} {
		b.inPlace = linked
		require.NoError(t, deliverBuild("op", b, bundle, src), "linked=%t", linked)
		fi, lerr := os.Lstat(filepath.Join(bundle, "api.mjs"))
		require.NoError(t, lerr)
		require.Equal(t, linked, fi.Mode()&fs.ModeSymlink != 0, "linked=%t", linked)
	}
	after, err := os.Stat(filepath.Join(src, "api.mjs"))
	require.NoError(t, err)
	require.Equal(t, before.ModTime(), after.ModTime(), "the copy never writes through a link into the source")
}

// Issue #1053: a kept bundle has one path per project across runs, and two projects that share a --persist-to never
// share one.
func TestIssue1053_KeptBundleDirIsPerProject(t *testing.T) {
	root, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	dirA, kept, err := privateBundleDir("op", root, "api", a)
	require.NoError(t, err)
	require.True(t, kept)
	again, _, err := privateBundleDir("op", root, "api", a)
	require.NoError(t, err)
	require.Equal(t, dirA, again)
	dirB, _, err := privateBundleDir("op", root, "api", b)
	require.NoError(t, err)
	require.NotEqual(t, dirA, dirB)
}

// A --persist-to inside the folder never makes dev re-apply a Function on its own: the watcher reads the folder's
// top-level files, and the state the session writes while it runs, the process registry included, is not among them.
func TestDevPersistRootInsideTheFolderDoesNotReload(t *testing.T) {
	requireRuntime(t)
	dir := devProject(t, nodeFunction("api", "", "export function handle() { return { n: 1 }; }\n"))
	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, devConfig{persist: true, persistTo: filepath.Join(dir, "state")})
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = inst.stop() })
	require.Equal(t, 1, addNote(t, inst))
	require.True(t, poll.Never(func() bool {
		obj, gerr := inst.client.Get(context.Background(), v1.KindFunction, devNamespace, "api")
		return gerr == nil && obj.GetObjectMeta().Generation > 1
	}, 3*time.Second, 100*time.Millisecond), "the function is re-applied though nothing was edited")
}

// The hot-reload watcher skips each dir the session writes while it runs, the bundles and the process registry under
// the persist root included.
func TestWatchSkipsTheSessionState(t *testing.T) {
	root := t.TempDir()
	p, err := resolvePersistPlan(devConfig{persist: true, persistTo: root})
	require.NoError(t, err)
	skips := p.watchSkips(filepath.Join(root, "process"))
	for _, sub := range []string{"metastore", "kv", "blob", "lake", "sql", "bundles", "process"} {
		require.Contains(t, skips, filepath.Join(root, sub))
	}
}

// failFirstPut answers the first PUT of the Function name with problem for the rest of the test, passing every
// other request through, and returns the URL that PUT went to (nil until it is made). startDev builds its SDK
// client from http.DefaultClient, so its transport is the only seam on an apply.
func failFirstPut(t *testing.T, name string, problem error) *atomic.Pointer[url.URL] {
	t.Helper()
	prev := http.DefaultClient.Transport
	next := prev
	if next == nil {
		next = http.DefaultTransport
	}
	f := &firstPutFault{next: next, path: "/apis/funcd.io/v1alpha1/namespaces/default/functions/" + name, problem: problem}
	http.DefaultClient.Transport = f
	t.Cleanup(func() { http.DefaultClient.Transport = prev })
	return &f.hit
}

type firstPutFault struct {
	next    http.RoundTripper
	path    string
	problem error
	hit     atomic.Pointer[url.URL]
}

func (f *firstPutFault) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPut || r.URL.Path != f.path || !f.hit.CompareAndSwap(nil, r.URL) {
		return f.next.RoundTrip(r)
	}
	rec := httptest.NewRecorder()
	fault.WriteProblem(rec, f.problem)
	return rec.Result(), nil
}

// The second boot of a --persist instance re-applies the restored resources while its controllers already
// write their status, so a re-apply can lose the optimistic update with a Conflict: the boot must re-apply,
// not fail.
func TestIssue398_DevPersistReapplyRetriesConflict(t *testing.T) {
	requireRuntime(t)
	dir := devProject(t, nodeFunction("api", "", okBuild))
	a := &cli{out: io.Discard}
	cfg := devConfig{persist: true, persistTo: t.TempDir()}

	ctx1, cancel1 := context.WithCancel(context.Background())
	inst1, err := a.startDev(ctx1, dir, cfg)
	require.NoError(t, err)
	cancel1()
	require.NoError(t, inst1.stop())

	// The store conflict the control plane returns when a controller writes the Function's status between the
	// API's read and its update (ADR-0018 read-RV-then-update).
	hit := failFirstPut(t, "api", fault.Conflictf("store.Update", "Function %q resourceVersion mismatch", "api"))

	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	inst2, err := a.startDev(ctx2, dir, cfg)
	require.NoError(t, err, "a Conflict on the re-apply is retried, not fatal to the boot")
	t.Cleanup(func() { cancel2(); _ = inst2.stop() })
	require.NotNil(t, hit.Load(), "the re-apply met the injected Conflict")
}

// A boot that fails once its platform runs must stop that platform before returning, so the platform's Shutdown
// closes each durable driver once and none is closed under its running controllers: the control port is free
// again and a retry reopens the same --persist dir.
func TestIssue426_DevFailedBootStopsPlatform(t *testing.T) {
	requireRuntime(t)
	const name = "issue426"
	dir := devProject(t, nodeFunction(name, "", okBuild))
	root, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	a := &cli{out: io.Discard}
	cfg := devConfig{persist: true, persistTo: root}

	hit := failFirstPut(t, name, fault.Invalidf("admission", "Function %q rejected", name))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, err = a.startDev(ctx, dir, cfg)
	require.Error(t, err, "a rejected apply fails the boot")
	rejected := hit.Load()
	require.NotNil(t, rejected, "the boot failed on the injected apply rejection, after its platform started")

	conn, derr := net.DialTimeout("tcp", rejected.Host, time.Second)
	if derr == nil {
		_ = conn.Close()
	}
	require.Error(t, derr, "a failed boot stops the platform it started")

	inst, err := a.startDev(ctx, dir, cfg)
	require.NoError(t, err, "the failed boot released the durable drivers")
	t.Cleanup(func() { cancel(); _ = inst.stop() })
}

// A hot-reload re-apply that loses its update to a controller's status write (Conflict) is re-applied in
// place, as the boot apply is (#398), so the reload reports no failure for an apply that took.
func TestIssue427_DevHotReloadRetriesConflict(t *testing.T) {
	dir := devProject(t, merge(nodeFunction("api", "  bindings:\n    config:\n      - app-config\n", okBuild), map[string]string{
		"app-config.yaml": "apiVersion: funcd.io/v1alpha1\nkind: ConfigMap\nmetadata:\n  name: app-config\nspec:\n  data:\n    APP_MODE: one\n",
	}))
	folder, err := loadDevFolder("test", dir)
	require.NoError(t, err)
	desired := []v1.Object{folder.functions[0].fn}
	for _, d := range folder.resources {
		desired = append(desired, d.obj)
	}

	var mu sync.Mutex
	puts := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		puts[r.Method+" "+r.URL.Path]++
		first := puts[r.Method+" "+r.URL.Path] == 1
		mu.Unlock()
		if first {
			fault.WriteProblem(w, fault.Conflictf("store.Update", "%s resourceVersion mismatch", r.URL.Path))
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL)
	require.NoError(t, err)

	require.NoError(t, applyOrdered(context.Background(), "test", c, desired), "a Conflict on a reload apply is re-applied in place, not reported")
	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, puts, "PUT /apis/funcd.io/v1alpha1/namespaces/default/functions/api")
	require.Contains(t, puts, "PUT /apis/funcd.io/v1alpha1/namespaces/default/configmaps/app-config")
	for path, n := range puts {
		require.Equal(t, 2, n, "%s met one Conflict and was re-applied once", path)
	}
}

// scenario: dev-persist-survives-restart (secrets facet) — secret values are never served from the durable store:
// dev reads them from the environment on every boot, so a --persist boot with the key unset fails naming it.
func TestScenarioDevPersistSecretsReReadFromEnv(t *testing.T) {
	requireRuntime(t)
	const key = "DEV_PERSIST_SECRET_VAR"
	root := t.TempDir()
	dir := devProject(t, nodeFunction("api", "  secrets:\n    - name: app-secret\n      keys:\n        - "+key+"\n"+
		"  bindings:\n    secret:\n      - app-secret\n", okBuild))
	a := &cli{out: io.Discard}

	t.Setenv(key, "boot1-secret")
	ctx1, cancel1 := context.WithCancel(context.Background())
	inst1, err := a.startDev(ctx1, dir, devConfig{persist: true, persistTo: root})
	require.NoError(t, err, "the secret resolves from the environment on the first persist boot")
	cancel1()
	require.NoError(t, inst1.stop())

	require.NoError(t, os.Unsetenv(key))
	_, err = a.startDev(context.Background(), dir, devConfig{persist: true, persistTo: root})
	require.Error(t, err, "an unset key fails the boot even under --persist")
	require.Contains(t, err.Error(), key)
}

// A --persist restart brings the namespace to what the folder holds (ADR-0247 Decision 8): the boot deletes what an
// earlier session applied from a removed file or root entry, and keeps an object the user applied.
func TestIssue502_DevPersistRestartPrunesRemovedResources(t *testing.T) {
	requireRuntime(t)
	const settings = "  configMaps:\n    - name: settings\n      data:\n        MODE: one\n  bindings:\n    config:\n      - settings\n"
	dir := devProject(t, merge(nodeFunction("front", settings, okBuild), nodeFunction("back", "", okBuild)))
	root, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	a := &cli{out: io.Discard}
	cfg := devConfig{persist: true, persistTo: root}

	ctx1, cancel1 := context.WithCancel(context.Background())
	inst1, err := a.startDev(ctx1, dir, cfg)
	require.NoError(t, err)
	user := &v1.ConfigMap{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: "user-settings", Namespace: devNamespace, ResourceGroup: devResourceGroup},
	}
	_, err = inst1.client.Apply(ctx1, user)
	require.NoError(t, err)
	cancel1()
	require.NoError(t, inst1.stop())

	for _, f := range []string{"back.yaml", "back.mjs", "back.contract.yaml"} {
		require.NoError(t, os.Remove(filepath.Join(dir, f)))
	}
	writeProject(t, dir, map[string]string{"front.yaml": fnDoc("front", "nodejs22", "")})
	ctx2, cancel2 := context.WithCancel(context.Background())
	inst2, err := a.startDev(ctx2, dir, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cancel2(); _ = inst2.stop() })

	for _, o := range []struct {
		kind v1.Kind
		name v1.ObjectName
	}{{v1.KindFunction, "back"}, {v1.KindRolesAssignment, "dev-blob-writer-back"}, {v1.KindConfigMap, "settings"}} {
		require.Eventually(t, func() bool {
			_, gerr := inst2.client.Get(ctx2, o.kind, devNamespace, o.name)
			return fault.KindOf(gerr) == fault.NotFound
		}, 10*time.Second, 50*time.Millisecond, "the boot deletes the %s %q the folder no longer holds", o.kind, o.name)
	}
	_, err = inst2.client.Get(ctx2, v1.KindConfigMap, devNamespace, "user-settings")
	require.NoError(t, err, "the boot keeps an object the user applied")
}

// TestResolvePersistPlanEphemeralDefault — the zero devConfig keeps every kind on memory (all dirs empty).
func TestResolvePersistPlanEphemeralDefault(t *testing.T) {
	p, err := resolvePersistPlan(devConfig{})
	require.NoError(t, err)
	require.Equal(t, persistPlan{}, p)
}

// TestResolvePersistPlanPersistSubdirs — --persist lays each service out as a subdir of the (absolute) persist root,
// mirroring the store-per-service design (Decision 7).
func TestResolvePersistPlanPersistSubdirs(t *testing.T) {
	root := t.TempDir()
	p, err := resolvePersistPlan(devConfig{persist: true, persistTo: root})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "metastore"), p.storeDir)
	require.Equal(t, filepath.Join(root, "kv"), p.kvDir)
	require.Equal(t, filepath.Join(root, "blob"), p.blobDir)
	require.Equal(t, filepath.Join(root, "lake"), p.lakeDir, "--persist makes the DuckLake catalog durable")
	require.Equal(t, filepath.Join(root, "sql"), p.sqlDir)
	require.Equal(t, filepath.Join(root, "bundles"), p.bundleDir, "--persist keeps bundles at one path (#1053)")
}

// TestResolvePersistPlanDefaultDir — an empty --persist-to falls back to the gitignored default dir.
func TestResolvePersistPlanDefaultDir(t *testing.T) {
	p, err := resolvePersistPlan(devConfig{persist: true})
	require.NoError(t, err)
	absDefault, err := filepath.Abs(devPersistDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(absDefault, "metastore"), p.storeDir)
	require.Equal(t, filepath.Join(absDefault, "kv"), p.kvDir)
	require.Equal(t, filepath.Join(absDefault, "blob"), p.blobDir)
}

// issue 434: a durable blob dir whose path holds URL syntax ('#', '?', '%') opens, and an object written through
// the bucket lands in exactly that dir.
func TestIssue434_DurableBlobDirWithURLSyntaxOpens(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"a#b", "q?x", "pct%", "p%41q"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name, "blob")
			_, _, bkt, closeAll, err := buildPersistDrivers("t", persistPlan{blobDir: dir})
			require.NoError(t, err)
			t.Cleanup(closeAll)
			require.NoError(t, bkt.Put(ctx, "k", []byte("v"), blob.PutOptions{}))
			got, err := os.ReadFile(filepath.Join(dir, "k"))
			require.NoError(t, err)
			require.Equal(t, "v", string(got))
		})
	}
}
