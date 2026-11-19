//go:build dev

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// scenario: dev-addon-deletion — under --persist, echo with deletion: delete leaves the folder, so dev's prune deletes
// it, and its volume goes; back in the folder it starts empty; with retain, its volume moves into the quarantine.
func TestScenarioDevAddonDeletion(t *testing.T) {
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	ctx := context.Background()
	withDeletion := func(policy string) string {
		return strings.Replace(echoObject, "spec: {}", "spec:\n  deletion: "+policy, 1)
	}
	dir := devProject(t, merge(nodeFunction("api", "", okBuild), map[string]string{"addon.yaml": echoAddon, "echo.yaml": withDeletion("delete")}))
	cfg := devConfig{persist: true, persistTo: t.TempDir()}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	name := "funcd-dev-addon-" + key + "-default-echo"
	inst, _ := startAddonDev(t, dir, cfg)
	waitObject(t, inst, "EchoService", "echo", "Ready", ready)
	require.NoError(t, inst.addons.Put(ctx, devNamespace, "echo", "notes", "marker", strings.NewReader("kept"), 10001, 10001))
	require.Equal(t, []string{name}, d.volumes(key))

	require.NoError(t, os.Remove(filepath.Join(dir, "echo.yaml")))
	require.Eventually(t, func() bool {
		_, err := inst.client.Get(ctx, "EchoService", devNamespace, "echo")
		return fault.KindOf(err) == fault.NotFound
	}, time.Minute, 100*time.Millisecond, "the reload's prune deletes echo")
	require.Eventually(t, func() bool { return len(d.volumes(key)) == 0 && len(d.containers(key)) == 0 }, time.Minute,
		100*time.Millisecond, "delete: the engine and its volume go")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "echo.yaml"), []byte(withDeletion("delete")), 0o600))
	waitObject(t, inst, "EchoService", "echo", "Ready", ready)
	requireMarker(t, inst, false)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "echo.yaml"), []byte(withDeletion("retain")), 0o600))
	waitObject(t, inst, "EchoService", "echo", "Ready and retain", func(o *v1.AddonObject) bool {
		return ready(o) && o.Spec.Deletion == v1.DeletionRetain && o.Status.ObservedGeneration == o.Generation
	})
	require.NoError(t, inst.addons.Put(ctx, devNamespace, "echo", "notes", "marker", strings.NewReader("kept"), 10001, 10001))
	require.NoError(t, os.Remove(filepath.Join(dir, "echo.yaml")))
	require.Eventually(t, func() bool {
		es, err := inst.addons.Quarantine().List(ctx)
		return err == nil && len(es) == 1
	}, time.Minute, 200*time.Millisecond, "retain: the volume moves into the quarantine")
	es, err := inst.addons.Quarantine().List(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{name + "-" + quarantine.Stamp(es[0].Since)}, d.volumes(key))
}

// snapshotAddon is echoAddon with ADR-0333's snapshot and restore folders under its storage.
func snapshotAddon() string {
	return strings.Replace(echoAddon, "    gid: 10001\n", "    gid: 10001\n  snapshot:\n    path: /data/snapshots\n"+
		"  restore:\n    path: /data/restore\n    env:\n      RESTORE_FROM: ${{ restore.file }}\n", 1)
}

// backupBucket is the Bucket the Backup writes to through dev's S3 frontend, whose keypair is the Function api's: api
// owns its prefix and may read it, as a Backup reads its own manifest back.
const backupBucket = `apiVersion: funcd.io/v1alpha1
kind: Bucket
metadata:
  name: backups
spec:
  prefixes:
    - name: default
      owner:
        kind: Function
        name: api
---
apiVersion: funcd.io/v1alpha1
kind: RolesAssignment
metadata:
  name: api-reads-backups
spec:
  principal:
    kind: Function
    name: api
  assignments:
    - roleRef:
        kind: BuiltinRole
        name: Blob Data Reader
      scope:
        kind: BucketPrefix
        name: backups/default
`

// scenario: dev-addon-backup — a Backup and an in-place Restore of echo run under dev as ADR-0333's
// addon-backup-member and addon-restore-in-place say: the Backup copies the engine's newest snapshot, and the Restore
// starts a new engine with the copy, then empties restore.path and ends.
func TestScenarioDevAddonBackup(t *testing.T) {
	requireRuntime(t)
	sock := requireDocker(t)
	d := newDockerEngine(t, sock)
	ctx := context.Background()
	dir := devProject(t, merge(nodeFunction("api", "", okBuild),
		map[string]string{"addon.yaml": snapshotAddon(), "echo.yaml": echoObject, "bucket.yaml": backupBucket}))
	cfg := devConfig{}
	key := sessionKey(t, cfg, dir)
	removeKeyAtCleanup(t, sock, key)
	name := "funcd-dev-addon-" + key + "-default-echo"
	inst, _ := startAddonDev(t, dir, cfg)
	waitObject(t, inst, "EchoService", "echo", "Ready", ready)
	require.NoError(t, inst.addons.Put(ctx, devNamespace, "echo", "snapshots", "snap_1.json", strings.NewReader(`{"v":1}`), 10001, 10001))

	admin, err := sdk.New(inst.controlURL, sdk.WithToken(funcd.DevAdminToken))
	require.NoError(t, err)
	apply := func(obj v1.Object) {
		t.Helper()
		_, err := admin.Apply(ctx, obj)
		require.NoError(t, err)
	}
	meta := func(name v1.ObjectName) v1.ObjectMeta {
		return v1.ObjectMeta{Name: name, Namespace: devNamespace, ResourceGroup: devResourceGroup}
	}
	typed := func(k v1.Kind) v1.TypeMeta { return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k} }
	first, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	second, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	apply(&v1.Secret{TypeMeta: typed(v1.KindSecret), ObjectMeta: meta("backups-key"), Spec: v1.SecretSpec{Data: map[string][]byte{
		"AWS_ACCESS_KEY_ID": []byte(inst.s3AccessKey), "AWS_SECRET_ACCESS_KEY": []byte(inst.s3SecretKey)}}})
	apply(&v1.Secret{TypeMeta: typed(v1.KindSecret), ObjectMeta: meta("backups-identity"), Spec: v1.SecretSpec{Data: map[string][]byte{
		"AGE_IDENTITY": []byte(first.String())}}})
	apply(&v1.BackupSchedule{TypeMeta: typed(v1.KindBackupSchedule), ObjectMeta: meta("nightly"), Spec: v1.BackupScheduleSpec{
		Schedule: "0 3 * * *", Scope: v1.BackupScope{Addons: []v1.ObjectName{"echo"}},
		Target: v1.BackupTarget{URL: "s3://backups?endpoint=" + inst.s3Endpoint + "&region=" + inst.s3Region + "&use_path_style=true",
			CredentialsSecret: "backups-key"},
		Encryption: v1.BackupEncryption{Recipients: []string{first.Recipient().String(), second.Recipient().String()}}}})
	apply(&v1.Backup{TypeMeta: typed(v1.KindBackup), ObjectMeta: meta("b1"), Spec: v1.BackupSpec{Schedule: "nightly"}})

	var backup *v1.Backup
	require.Eventually(t, func() bool {
		obj, err := admin.Get(ctx, v1.KindBackup, devNamespace, "b1")
		if err != nil {
			return false
		}
		backup = obj.(*v1.Backup)
		return backup.Status.Phase == v1.RunSucceeded || backup.Status.Phase == v1.RunFailed
	}, 2*time.Minute, 200*time.Millisecond, "the Backup ends")
	require.Equal(t, v1.RunSucceeded, backup.Status.Phase, "%+v", backup.Status.Conditions)
	require.Len(t, backup.Status.Members, 1)
	require.Equal(t, v1.Kind("EchoService"), backup.Status.Members[0].Kind, "the member carries its add-on's kind")
	require.Positive(t, backup.Status.Members[0].Bytes)

	before := d.id(name)
	apply(&v1.Restore{TypeMeta: typed(v1.KindRestore), ObjectMeta: meta("r1"), Spec: v1.RestoreSpec{Backup: "b1",
		Source:  v1.RestoreSource{CredentialsSecret: "backups-key", IdentitySecret: "backups-identity"},
		Members: []v1.RestoreMember{{Kind: "EchoService", Name: "echo", InPlace: true}}}})
	var restore *v1.Restore
	require.Eventually(t, func() bool {
		obj, err := admin.Get(ctx, v1.KindRestore, devNamespace, "r1")
		if err != nil {
			return false
		}
		restore = obj.(*v1.Restore)
		return restore.Ended()
	}, 3*time.Minute, 200*time.Millisecond, "the Restore ends")
	require.Equal(t, v1.RunSucceeded, restore.Status.Phase, "%+v", restore.Status.Conditions)
	o := waitObject(t, inst, "EchoService", "echo", "Ready with no restore mark", func(o *v1.AddonObject) bool {
		return ready(o) && o.Status.Restore == nil
	})
	require.Nil(t, o.Status.Restore)
	_, _, err = inst.addons.Newest(ctx, devNamespace, "echo", "restore", 0)
	require.Equal(t, fault.NotFound, fault.KindOf(err), "the copy left restore.path: %v", err)
	require.NotEqual(t, before, d.id(name), "the engine was started again for the copy")
}
