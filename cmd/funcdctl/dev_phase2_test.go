//go:build dev

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// requireRuntime skips a test when neither node nor python3 is on PATH — `funcdctl dev` needs a runtime
// shim to boot the platform (devShimOptions), even for the S3 / persist lanes that never invoke a handler.
func requireRuntime(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("node"); err == nil {
		return
	}
	if _, err := exec.LookPath("python3"); err == nil {
		return
	}
	t.Skip("neither node nor python3 on PATH; funcdctl dev needs a runtime shim to boot")
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

// conflictOnFirstPut answers the first PUT of one Function with the store conflict the control plane returns
// when a controller writes the object's status between the API's read and its update (ADR-0018 read-RV-then-
// update), and passes every other request through.
type conflictOnFirstPut struct {
	next http.RoundTripper
	name string
	hit  atomic.Bool
}

func (c *conflictOnFirstPut) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPut || r.URL.Path != "/apis/funcd.io/v1alpha1/namespaces/default/functions/"+c.name ||
		!c.hit.CompareAndSwap(false, true) {
		return c.next.RoundTrip(r)
	}
	rec := httptest.NewRecorder()
	fault.WriteProblem(rec, fault.Conflictf("store.Update", "Function %q resourceVersion mismatch", c.name))
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

	// startDev builds its SDK client from http.DefaultClient, so its transport is the only seam on the re-apply.
	prev := http.DefaultClient.Transport
	next := prev
	if next == nil {
		next = http.DefaultTransport
	}
	conflict := &conflictOnFirstPut{next: next, name: name}
	http.DefaultClient.Transport = conflict
	t.Cleanup(func() { http.DefaultClient.Transport = prev })

	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	inst2, err := a.startDev(ctx2, dir, "", cfg)
	require.NoError(t, err, "a Conflict on the re-apply is retried, not fatal to the boot")
	t.Cleanup(func() { cancel2(); _ = inst2.stop() })
	require.True(t, conflict.hit.Load(), "the re-apply met the injected Conflict")
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
