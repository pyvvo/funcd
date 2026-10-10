package escrow_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/escrow"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func randomKey(t *testing.T) []byte {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// escrowDir lays out an escrow set: files under secrets/ and master/, any names.
func escrowDir(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, b := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
		require.NoError(t, os.WriteFile(p, b, 0o600))
	}
	return dir
}

func manifest(secretsKey, master []byte) backup.Manifest {
	m := backup.Manifest{Generation: 40}
	if secretsKey != nil {
		m.SecretsKey = envelope.Fingerprint(secretsKey)
	}
	if master != nil {
		m.MasterSecret = envelope.Fingerprint(master)
	}
	return m
}

// scenario: restore-names-secrets-key — generation 40 written under K1 and a restore configured with K2 refuses,
// naming K1's fingerprint and the escrow file matching it, if any.
func TestScenarioRestoreNamesSecretsKey(t *testing.T) {
	k1, k2 := randomKey(t), randomKey(t)
	m := manifest(k1, nil)
	dir := escrowDir(t, map[string][]byte{"secrets/2026-10.key": k1, "secrets/old.key": k2})

	err := escrow.CheckSecretsKey(m, k2, dir)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, envelope.Fingerprint(k1))
	require.ErrorContains(t, err, filepath.Join(dir, "secrets", "2026-10.key"))

	err = escrow.CheckSecretsKey(m, k2, escrowDir(t, map[string][]byte{"secrets/old.key": k2}))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, envelope.Fingerprint(k1))
	require.ErrorContains(t, err, "no escrow file matches it")

	err = escrow.CheckSecretsKey(m, nil, dir)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a generation with a secrets key and a restore without one")
	err = escrow.CheckSecretsKey(manifest(nil, nil), k1, dir)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a restore with a secrets key and a generation without one")
	require.NoError(t, escrow.CheckSecretsKey(m, k1, ""))
	require.NoError(t, escrow.CheckSecretsKey(manifest(nil, nil), nil, ""))
}

// scenario: master-secret-required — a generation recording master F and an escrow without F refuses naming F; with F
// escrowed it is installed at Decision 7's path and a restored spec.blob Function keeps its S3 keypair. Tested at
// the escrow API: ADR-0206's restore command writes the plan.
func TestScenarioMasterSecretRequired(t *testing.T) {
	f, other := randomKey(t), randomKey(t)
	m := manifest(nil, f)
	dataDir := t.TempDir()

	_, err := escrow.PlanMaster(m, "", dataDir, escrowDir(t, map[string][]byte{"master/node-a.key": other}), false)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, envelope.Fingerprint(f))
	require.ErrorContains(t, err, envelope.Fingerprint(other), "the fingerprints found are named")

	plan, err := escrow.PlanMaster(m, "", dataDir, escrowDir(t, map[string][]byte{"master/node-a.key": f}), false)
	require.NoError(t, err)
	require.Equal(t, f, plan.Install)
	require.Equal(t, filepath.Join(dataDir, "s3gateway", "master.key"), plan.Path)
	require.False(t, plan.List)

	require.NoError(t, os.MkdirAll(filepath.Dir(plan.Path), 0o700))
	require.NoError(t, os.WriteFile(plan.Path, plan.Install, 0o600))
	loaded, err := s3gateway.LoadOrCreateMaster("", dataDir)
	require.NoError(t, err)
	require.Equal(t, s3gateway.DeriveKeypair(f, v1.KindFunction, "default", "a"),
		s3gateway.DeriveKeypair(loaded, v1.KindFunction, "default", "a"))

	file := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(file, other, 0o600))
	_, err = escrow.PlanMaster(m, file, dataDir, "", false)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a set masterSecretFile must have F")
	require.ErrorContains(t, err, envelope.Fingerprint(f))
	require.ErrorContains(t, err, file)
	require.NoError(t, os.WriteFile(file, f, 0o600))
	plan, err = escrow.PlanMaster(m, file, dataDir, "", false)
	require.NoError(t, err)
	require.Equal(t, escrow.MasterPlan{Path: file}, plan)

	plan, err = escrow.PlanMaster(m, "", dataDir, "", true)
	require.NoError(t, err, "--new-master-secret proceeds")
	require.True(t, plan.List)
	require.Nil(t, plan.Install)
}

// scenario: new-master-secret-lists-changes — with --new-master-secret, Functions a (spec.blob) and b (spec.catalogs)
// and CatalogService c (spec.blob) are listed, not Function d or CatalogService e; the store holds the same either
// way, so the list does not depend on the gateway.
func TestScenarioNewMasterSecretListsChanges(t *testing.T) {
	ctx := context.Background()
	eng := memory.New()
	st := store.New(eng)
	blobs := []v1.FunctionBlob{{Alias: "data", Bucket: "lake", Prefix: "raw"}}
	catalog := func(o v1.Object) {
		cs := o.(*v1.CatalogService)
		cs.Spec.Blob, cs.Spec.Catalog = blobs, v1.CatalogRef{Bucket: "lake", Prefix: "raw"}
	}
	create := func(kind v1.Kind, ns, name string, set func(v1.Object)) {
		obj, ok := v1.NewObject(kind)
		require.True(t, ok)
		meta := obj.GetObjectMeta()
		meta.Name, meta.Namespace, meta.ResourceGroup = v1.ObjectName(name), v1.NamespaceName(ns), "rg"
		set(obj)
		_, err := st.Create(ctx, obj)
		require.NoError(t, err)
	}
	create(v1.KindFunction, "team", "a", func(o v1.Object) { o.(*v1.Function).Spec.Blob = blobs })
	create(v1.KindFunction, "team", "b", func(o v1.Object) {
		o.(*v1.Function).Spec.Catalogs = []v1.FunctionCatalog{{Alias: "lake", Catalog: "c"}}
	})
	create(v1.KindFunction, "team", "d", func(v1.Object) {})
	create(v1.KindCatalogService, "team", "c", catalog)
	create(v1.KindCatalogService, "team", "e", catalog)
	dropBlob(t, eng, "e")

	plan, err := escrow.PlanMaster(manifest(nil, randomKey(t)), "", t.TempDir(), "", true)
	require.NoError(t, err)
	got, err := escrow.ListChanged(ctx, plan, st)
	require.NoError(t, err)
	require.Equal(t, []escrow.Derived{
		{Kind: v1.KindCatalogService, Namespace: "team", Name: "c", Credential: "s3-keypair"},
		{Kind: v1.KindFunction, Namespace: "team", Name: "a", Credential: "s3-keypair"},
		{Kind: v1.KindFunction, Namespace: "team", Name: "b", Credential: "catalog-token"},
	}, got)

	none, err := escrow.ListChanged(ctx, escrow.MasterPlan{}, st)
	require.NoError(t, err)
	require.Nil(t, none, "nothing to list when the master is kept")
}

// dropBlob rewrites the stored CatalogService name without spec.blob, which admission no longer accepts but an
// older store may hold.
func dropBlob(t *testing.T, eng store.Engine, name string) {
	t.Helper()
	ctx := context.Background()
	var key, val []byte
	_, err := eng.Snapshot(ctx, func(r snapshot.Record) error {
		if bytes.Contains(r.Value, []byte(`"kind":"CatalogService"`)) && bytes.Contains(r.Value, []byte(`"name":"`+name+`"`)) {
			key, val = r.Key, r.Value
		}
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, key)
	var obj, spec map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(val, &obj))
	require.NoError(t, json.Unmarshal(obj["spec"], &spec))
	delete(spec, "blob")
	obj["spec"], err = json.Marshal(spec)
	require.NoError(t, err)
	val, err = json.Marshal(obj)
	require.NoError(t, err)
	bucket, k, ok := bytes.Cut(key, []byte{0})
	require.True(t, ok)
	require.NoError(t, eng.Update(ctx, func(tx store.Txn) error { return tx.Put(string(bucket), string(k), val) }))
}

// Find matches by content, whatever the file name or depth, and names the fingerprints it found.
func TestFindByContent(t *testing.T) {
	k, other := randomKey(t), randomKey(t)
	dir := escrowDir(t, map[string][]byte{"secrets/a/whatever": k, "secrets/b": other})
	p, err := escrow.Find(dir, escrow.SecretsDir, envelope.Fingerprint(k))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "secrets", "a", "whatever"), p)

	_, err = escrow.Find(dir, escrow.SecretsDir, envelope.Fingerprint(randomKey(t)))
	require.Equal(t, fault.NotFound, fault.KindOf(err))
	require.ErrorContains(t, err, envelope.Fingerprint(other))

	_, err = escrow.Find(dir, escrow.MasterDir, envelope.Fingerprint(k))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "an absent directory finds nothing")
	_, err = escrow.Find("", escrow.MasterDir, envelope.Fingerprint(k))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "no escrow directory finds nothing")

	_, err = escrow.Find(escrowDir(t, map[string][]byte{"secrets/k": append(k, '\n')}), escrow.SecretsDir, envelope.Fingerprint(k))
	require.Equal(t, fault.NotFound, fault.KindOf(err), "an escrow copy must be byte-exact")
}

// A generation naming no master: no check, nothing installed, the list printed as credentials that may change.
func TestPlanMasterAbsent(t *testing.T) {
	dataDir := t.TempDir()
	plan, err := escrow.PlanMaster(manifest(nil, nil), "", dataDir, "", false)
	require.NoError(t, err)
	require.Equal(t, escrow.MasterPlan{Path: filepath.Join(dataDir, "s3gateway", "master.key"), List: true}, plan)
}

// PlanMaster reads and never writes: the data dir and the escrow set are unchanged.
func TestPlanMasterWritesNothing(t *testing.T) {
	f := randomKey(t)
	dataDir, dir := t.TempDir(), escrowDir(t, map[string][]byte{"master/k": f})
	before := tree(t, dataDir, dir)
	for _, accept := range []bool{false, true} {
		for _, m := range []backup.Manifest{manifest(nil, f), manifest(nil, randomKey(t)), manifest(nil, nil)} {
			_, _ = escrow.PlanMaster(m, "", dataDir, dir, accept)
		}
	}
	require.Equal(t, before, tree(t, dataDir, dir))
}

func tree(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p) //nolint:gosec // a test temp path
			out[p] = string(b)
			return err
		}))
	}
	return out
}
