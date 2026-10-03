//go:build dev

package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// requireRuntime skips a test when `funcdctl dev` would find no usable runtime shim to boot the platform,
// even for the S3 / persist lanes that never invoke a handler. It asks devShimOptions itself for the node
// handler these lanes declare, so it skips exactly when startup finds no node interpreter.
func requireRuntime(t *testing.T) {
	t.Helper()
	_, cleanup, err := devShimOptions(context.Background(), "requireRuntime", sdk.Dev{}, "", false, true)
	if fault.KindOf(err) == fault.NotFound {
		t.Skipf("funcdctl dev needs a runtime shim to boot: %v", err)
	}
	require.NoError(t, err)
	cleanup()
}

// Issue #431: on a host with no node and a python3 that cannot load the shim, funcdctl dev finds no
// runtime, yet requireRuntime saw python3 on PATH and let the gated tests run into that startup error.
func TestIssue431_RequireRuntimeSkipsWhenPythonCannotLoadShim(t *testing.T) {
	bin := t.TempDir()
	python := filepath.Join(bin, "python3")
	require.NoError(t, os.WriteFile(python, []byte("#!/bin/sh\necho \"ModuleNotFoundError: No module named 'fastjsonschema'\" >&2\nexit 1\n"), 0o700))
	t.Setenv("PATH", bin)
	t.Setenv("FUNCD_NODE", "")
	t.Setenv("FUNCD_PYTHON", "")

	ctx, cancel := context.WithCancel(context.Background())
	inst, err := (&cli{out: io.Discard}).startDev(ctx, devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"handler.mjs":   "export function handle() { return {}; }\n",
	}), "", devConfig{})
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
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"handler.mjs":   "export function handle() { return { ok: true }; }\n",
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	inst, err := (&cli{out: io.Discard}).startDev(ctx, dir, "", devConfig{s3port: taken.Addr().(*net.TCPAddr).Port})
	if err == nil {
		cancel()
		_ = inst.stop()
	}
	require.ErrorIs(t, err, syscall.EADDRINUSE, "funcdctl dev showed an S3 endpoint the gateway never bound")
}

// scenario: dev-inspect-blob-via-s3 — a function bound to a bucket writes an object THROUGH the dev S3
// endpoint (it owns the prefix, so an owner-write is allowed), and the object is then readable back over
// the same endpoint (GET + `aws s3 ls`) with the printed dev creds — the same S3 surface prod exposes.
func TestScenarioDevInspectBlobViaS3(t *testing.T) {
	requireRuntime(t)
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n  blob:\n    - alias: bronze\n      bucket: releves\n      prefix: bronze\n" +
			permissiveContract,
		"handler.mjs": "export function handle() { return { ok: true }; }\n",
	})
	inst := runDev(t, dir)
	// startDev applied the Function (spec.blob binding = the S3 grant) + the Bucket (prefix owned by the
	// function) synchronously, so the PEP authorizes the dev keypair immediately — no need to wait for the
	// worker to reach Ready (this lane never invokes the handler).
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
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n" +
			"  kv:\n    - alias: cache\n      store: cache-kv\n      table: entries\n" +
			"  blob:\n    - alias: bronze\n      bucket: releves\n      prefix: bronze\n" +
			permissiveContract,
		"handler.mjs": "export function handle() { return { ok: true }; }\n",
	})
	ctx := context.Background()
	const kvKey, blobKey = "greeting", "persist/probe.bin"
	kvVal, blobVal := []byte("bonjour"), []byte("durable-rows")

	// Boot 1 — persist, write state, stop.
	a := &cli{out: io.Discard}
	ctx1, cancel1 := context.WithCancel(ctx)
	inst1, err := a.startDev(ctx1, dir, "", devConfig{persist: true, persistTo: root})
	require.NoError(t, err)
	require.NotNil(t, inst1.kv, "--persist composes a durable KV driver")
	require.NotNil(t, inst1.blob, "--persist composes a durable blob driver")
	require.NoError(t, inst1.kv.Put(ctx, kvKey, kvVal))
	require.NoError(t, inst1.blob.Put(ctx, blobKey, blobVal))
	cancel1()
	require.NoError(t, inst1.stop())

	// The per-service subdirs are on disk (store-per-service layout, Decision 7).
	require.DirExists(t, filepath.Join(root, "metastore"))
	require.DirExists(t, filepath.Join(root, "kv"))
	require.DirExists(t, filepath.Join(root, "blob"))

	// Boot 2 — same dir; the state must still be there (no re-seed).
	ctx2, cancel2 := context.WithCancel(ctx)
	inst2, err := a.startDev(ctx2, dir, "", devConfig{persist: true, persistTo: root})
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

// failFirstPut answers the first PUT of the Function name with problem for the rest of the test, passing every
// other request through, and reports whether that PUT was made. startDev builds its SDK client from
// http.DefaultClient, so its transport is the only seam on an apply.
func failFirstPut(t *testing.T, name string, problem error) *atomic.Bool {
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
	hit     atomic.Bool
}

func (f *firstPutFault) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPut || r.URL.Path != f.path || !f.hit.CompareAndSwap(false, true) {
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
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"handler.mjs":   "export function handle() { return { ok: true }; }\n",
	})
	a := &cli{out: io.Discard}
	cfg := devConfig{persist: true, persistTo: t.TempDir()}

	ctx1, cancel1 := context.WithCancel(context.Background())
	inst1, err := a.startDev(ctx1, dir, "", cfg)
	require.NoError(t, err)
	name := inst1.functions[0]
	cancel1()
	require.NoError(t, inst1.stop())

	// The store conflict the control plane returns when a controller writes the Function's status between the
	// API's read and its update (ADR-0018 read-RV-then-update).
	hit := failFirstPut(t, name, fault.Conflictf("store.Update", "Function %q resourceVersion mismatch", name))

	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	inst2, err := a.startDev(ctx2, dir, "", cfg)
	require.NoError(t, err, "a Conflict on the re-apply is retried, not fatal to the boot")
	t.Cleanup(func() { cancel2(); _ = inst2.stop() })
	require.True(t, hit.Load(), "the re-apply met the injected Conflict")
}

// A boot that fails once its platform runs must stop that platform before returning, so the platform's Shutdown
// closes each durable driver once and none is closed under its running controllers: the control port is free
// again and a retry reopens the same --persist dir.
func TestIssue426_DevFailedBootStopsPlatform(t *testing.T) {
	requireRuntime(t)
	const name = "issue426"
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" + permissiveContract,
		"handler.mjs":   "export function handle() { return { ok: true }; }\n",
	})
	root, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	cport := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	a := &cli{out: io.Discard}
	cfg := devConfig{persist: true, persistTo: root, cport: cport, name: name}

	hit := failFirstPut(t, name, fault.Invalidf("admission", "Function %q rejected", name))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, err = a.startDev(ctx, dir, "", cfg)
	require.Error(t, err, "a rejected apply fails the boot")
	require.True(t, hit.Load(), "the boot failed on the injected apply rejection, after its platform started")

	conn, derr := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if derr == nil {
		_ = conn.Close()
	}
	require.Error(t, derr, "a failed boot stops the platform it started")

	inst, err := a.startDev(ctx, dir, "", cfg)
	require.NoError(t, err, "the failed boot released the control port and the durable drivers")
	t.Cleanup(func() { cancel(); _ = inst.stop() })
}

// A hot-reload re-apply that loses its update to a controller's status write (Conflict) is re-applied in
// place, as the boot apply is (#398), so the poll reports no failure for a reload that took.
func TestIssue427_DevHotReloadRetriesConflict(t *testing.T) {
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\nbindings:\n  config:\n    - app-config\n" + permissiveContract +
			"dev:\n  config:\n    app-config:\n      APP_MODE: one\n",
		"handler.mjs": "export function handle() { return { ok: true }; }\n",
	})
	pfs, err := resolveDevPlan("test", dir, "", devConfig{})
	require.NoError(t, err)

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

	h := &devHandler{pf: pfs[0], bundle: filepath.Join(dir, pfs[0].entry)}
	require.NoError(t, reloadChanged(context.Background(), "test", c, []*devHandler{h}, &[]v1.Object{}, nil),
		"a Conflict on a hot-reload apply is re-applied in place, not reported")
	mu.Lock()
	defer mu.Unlock()
	require.Contains(t, puts, "PUT /apis/funcd.io/v1alpha1/namespaces/default/functions/"+string(pfs[0].name))
	for path, n := range puts {
		require.Equal(t, 2, n, "%s met one Conflict and was re-applied once", path)
	}
}

// scenario: dev-persist-survives-restart (secrets facet) — secrets are NEVER served from the durable
// store: they are re-read from ${ENV} on EVERY boot. Booting --persist with the referenced var UNSET on
// the second run fails fast (it is not silently recovered from the persisted metastore).
func TestScenarioDevPersistSecretsReReadFromEnv(t *testing.T) {
	requireRuntime(t)
	const varName = "DEV_PERSIST_SECRET_VAR"
	root := t.TempDir()
	dir := devProject(t, map[string]string{
		"funcdctl.yaml": "runtime: nodejs22\nhandler: handle\n" +
			"bindings:\n  secrets:\n    - app-secret\n" +
			permissiveContract +
			"dev:\n  secrets:\n    app-secret:\n      API_KEY: ${" + varName + "}\n",
		"handler.mjs": "export function handle() { return { key: process.env.API_KEY }; }\n",
	})
	a := &cli{out: io.Discard}

	// Boot 1 — the var is set; the secret resolves and the platform boots.
	t.Setenv(varName, "boot1-secret")
	ctx1, cancel1 := context.WithCancel(context.Background())
	inst1, err := a.startDev(ctx1, dir, "", devConfig{persist: true, persistTo: root})
	require.NoError(t, err, "the env-sourced secret resolves on the first persist boot")
	cancel1()
	require.NoError(t, inst1.stop())

	// Boot 2 — the var is now UNSET; because secrets are re-read from env (never served from the durable
	// store), the boot fails fast naming the missing var.
	require.NoError(t, os.Unsetenv(varName))
	_, err = a.startDev(context.Background(), dir, "", devConfig{persist: true, persistTo: root})
	require.Error(t, err, "a missing env var fails fast even under --persist (secret not recovered from disk)")
	require.Contains(t, err.Error(), varName)
}

// A --persist restart brings the dev namespace to what the current manifests describe (ADR-0125): the boot deletes
// what an earlier session synthesized for a removed manifest or binding, and keeps an object the user applied.
func TestIssue502_DevPersistRestartPrunesRemovedResources(t *testing.T) {
	requireRuntime(t)
	manifest := func(bindings string) string {
		return "runtime: nodejs22\nhandler: handle\n" + bindings + permissiveContract
	}
	const handler = "export function handle() { return { ok: true }; }\n"
	dir := devProject(t, map[string]string{
		"front.funcdctl.yaml": manifest("bindings:\n  config:\n    - settings\n") + "dev:\n  config:\n    settings:\n      MODE: one\n",
		"front.mjs":           handler,
		"back.funcdctl.yaml":  manifest(""),
		"back.mjs":            handler,
	})
	root, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	a := &cli{out: io.Discard}
	cfg := devConfig{persist: true, persistTo: root}

	ctx1, cancel1 := context.WithCancel(context.Background())
	inst1, err := a.startDev(ctx1, dir, "", cfg)
	require.NoError(t, err)
	user := &v1.ConfigMap{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
		ObjectMeta: v1.ObjectMeta{Name: "user-settings", Namespace: devNamespace, ResourceGroup: devResourceGroup},
	}
	_, err = inst1.client.Apply(ctx1, user)
	require.NoError(t, err)
	cancel1()
	require.NoError(t, inst1.stop())

	require.NoError(t, os.Remove(filepath.Join(dir, "back.funcdctl.yaml")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "front.funcdctl.yaml"), []byte(manifest("")), 0o600))
	ctx2, cancel2 := context.WithCancel(context.Background())
	inst2, err := a.startDev(ctx2, dir, "", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { cancel2(); _ = inst2.stop() })

	for _, o := range []struct {
		kind v1.Kind
		name v1.ObjectName
	}{{v1.KindFunction, "back"}, {v1.KindRolesAssignment, "dev-blob-writer-back"}, {v1.KindConfigMap, "settings"}} {
		require.Eventually(t, func() bool {
			_, gerr := inst2.client.Get(ctx2, o.kind, devNamespace, o.name)
			return fault.KindOf(gerr) == fault.NotFound
		}, 10*time.Second, 50*time.Millisecond, "the boot deletes the %s %q no manifest names", o.kind, o.name)
	}
	_, err = inst2.client.Get(ctx2, v1.KindConfigMap, devNamespace, "user-settings")
	require.NoError(t, err, "the boot keeps an object the user applied")
}

// TestResolvePersistPlanEphemeralDefault — the zero devConfig keeps every kind on memory (all dirs empty).
func TestResolvePersistPlanEphemeralDefault(t *testing.T) {
	p, err := resolvePersistPlan(devConfig{}, &sdk.Manifest{})
	require.NoError(t, err)
	require.Empty(t, p.storeDir)
	require.Empty(t, p.kvDir)
	require.Empty(t, p.blobDir)
	require.Empty(t, p.catalogDir, "the DuckLake catalog is ephemeral (temp) without --persist")
}

// TestResolvePersistPlanPersistSubdirs — --persist lays metastore/kv/blob out as per-service subdirs of
// the (absolute) persist root, mirroring the store-per-service design (Decision 7).
func TestResolvePersistPlanPersistSubdirs(t *testing.T) {
	root := t.TempDir()
	p, err := resolvePersistPlan(devConfig{persist: true, persistTo: root}, &sdk.Manifest{})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "metastore"), p.storeDir)
	require.Equal(t, filepath.Join(root, "kv"), p.kvDir)
	require.Equal(t, filepath.Join(root, "blob"), p.blobDir)
	require.Equal(t, filepath.Join(root, "catalog"), p.catalogDir, "--persist makes the DuckLake catalog durable")
}

// TestResolvePersistPlanDefaultDir — an empty --persist-to falls back to the gitignored default dir.
func TestResolvePersistPlanDefaultDir(t *testing.T) {
	p, err := resolvePersistPlan(devConfig{persist: true}, &sdk.Manifest{})
	require.NoError(t, err)
	absDefault, err := filepath.Abs(devPersistDir)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(absDefault, "metastore"), p.storeDir)
	require.Equal(t, filepath.Join(absDefault, "kv"), p.kvDir)
	require.Equal(t, filepath.Join(absDefault, "blob"), p.blobDir)
}

// TestResolvePersistPlanBackendOverrides — dev.backends is GLOBAL per kind: a file:// override makes a
// kind durable even WITHOUT --persist (the file:// blob-inspection path), "memory" pins a kind ephemeral
// even WITH --persist, and the metastore tracks --persist alone (no dev.backends knob).
func TestResolvePersistPlanBackendOverrides(t *testing.T) {
	// file:// blob override, no --persist: blob durable, kv+store still memory.
	m := &sdk.Manifest{Dev: sdk.Dev{Backends: sdk.Backends{Blob: "file://.funcd-dev/blob"}}}
	p, err := resolvePersistPlan(devConfig{}, m)
	require.NoError(t, err)
	require.Empty(t, p.storeDir, "no --persist ⇒ ephemeral metastore")
	require.Empty(t, p.kvDir, "no kv override + no --persist ⇒ memory KV")
	absBlob, err := filepath.Abs(".funcd-dev/blob")
	require.NoError(t, err)
	require.Equal(t, absBlob, p.blobDir, "the file:// blob override resolves to an absolute dir")

	// A bare-path kv override resolves absolute too.
	m2 := &sdk.Manifest{Dev: sdk.Dev{Backends: sdk.Backends{KV: "state/kv"}}}
	p2, err := resolvePersistPlan(devConfig{}, m2)
	require.NoError(t, err)
	absKV, err := filepath.Abs("state/kv")
	require.NoError(t, err)
	require.Equal(t, absKV, p2.kvDir)

	// "memory" pins a kind ephemeral even under --persist (explicit override wins).
	m3 := &sdk.Manifest{Dev: sdk.Dev{Backends: sdk.Backends{KV: "memory"}}}
	root := t.TempDir()
	p3, err := resolvePersistPlan(devConfig{persist: true, persistTo: root}, m3)
	require.NoError(t, err)
	require.Empty(t, p3.kvDir, "an explicit memory override stays ephemeral under --persist")
	require.Equal(t, filepath.Join(root, "metastore"), p3.storeDir, "metastore is still durable under --persist")
	require.Equal(t, filepath.Join(root, "blob"), p3.blobDir, "blob defaults to durable under --persist")
}

// issue 434: a durable blob dir whose path holds URL syntax ('#', '?', '%') opens, and an object written through
// the bucket lands in exactly that dir.
func TestIssue434_DurableBlobDirWithURLSyntaxOpens(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"a#b", "q?x", "pct%", "p%41q"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name, "blob")
			m := &sdk.Manifest{Dev: sdk.Dev{Backends: sdk.Backends{Blob: dir}}}
			_, _, bkt, closeAll, err := buildPersistDrivers("t", devConfig{}, m)
			require.NoError(t, err)
			t.Cleanup(closeAll)
			require.NoError(t, bkt.Put(ctx, "k", []byte("v")))
			got, err := os.ReadFile(filepath.Join(dir, "k"))
			require.NoError(t, err)
			require.Equal(t, "v", string(got))
		})
	}
}
