//go:build unix

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
	"github.com/pyvvo/funcd/pkg/sdk"
)

func nsObj(ns v1.NamespaceName, name string) v1.ObjectMeta {
	return v1.ObjectMeta{Namespace: ns, ResourceGroup: "rg", Name: v1.ObjectName(name)}
}

// scenario: platform-restore-holds-namespace — G holds schedules in orders and none in web: restore run holds orders
// alone; once G is released web's timers fire while orders' timers and slots wait for `funcdctl hold release
// --namespace orders`, and a Backup by API in orders runs meanwhile.
func TestScenarioPlatformRestoreHoldsNamespace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stub := s3stub.New(t, nil)
	var recipients []string
	for range 2 {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		recipients = append(recipients, id.Recipient().String())
	}
	g := newGSource(t, nil)
	for _, ns := range []v1.NamespaceName{"orders", "web"} {
		g.create(t,
			&v1.EventSource{TypeMeta: tmeta(v1.KindEventSource), ObjectMeta: nsObj(ns, "tick"), Spec: v1.EventSourceSpec{
				Timer: &v1.TimerSource{Events: []v1.TimerEvent{{Name: "t", Interval: v1.Duration(100 * time.Millisecond)}}}}},
			&v1.Sensor{TypeMeta: tmeta(v1.KindSensor), ObjectMeta: nsObj(ns, "s"), Spec: v1.SensorSpec{
				On: []v1.Dependency{{Name: "tick", Source: "tick", Event: "t"}}, Do: []v1.Action{{Name: "a", On: "tick", Workflow: "wf"}}}})
	}
	g.create(t,
		&v1.KVStore{TypeMeta: tmeta(v1.KindKVStore), ObjectMeta: nsObj("orders", "orders-state")},
		&v1.Secret{TypeMeta: tmeta(v1.KindSecret), ObjectMeta: nsObj("orders", "backup-writer"), Spec: v1.SecretSpec{
			Data: map[string][]byte{"AWS_ACCESS_KEY_ID": []byte("AKIDWRITER"), "AWS_SECRET_ACCESS_KEY": []byte("w")}}},
		&v1.BackupSchedule{TypeMeta: tmeta(v1.KindBackupSchedule), ObjectMeta: nsObj("orders", "nightly"), Spec: v1.BackupScheduleSpec{
			Schedule: "0 3 * * *", TimeZone: "Europe/Paris", Scope: v1.BackupScope{KVStores: []v1.ObjectName{"orders-state"}},
			Target: v1.BackupTarget{URL: stub.URL(), CredentialsSecret: "backup-writer"}, Encryption: v1.BackupEncryption{Recipients: recipients}}})
	obj, err := g.meta.Get(ctx, v1.KindBackupSchedule.GVK(), "orders", "nightly")
	require.NoError(t, err)
	sched := obj.(*v1.BackupSchedule)
	twoDays := v1.NewTimestamp(time.Now().Add(-48 * time.Hour))
	sched.Status.LastScheduleTime = &twoDays // a slot is due as soon as the namespace may take it
	_, err = g.meta.Update(ctx, sched)
	require.NoError(t, err)
	g.cut(t)

	n := newNode(t, nil, restorePacing+"runtime:\n  supervisionPeriod: 200ms\n")
	out, err := n.restore("run", "latest", "--from", g.target)
	require.NoError(t, err)
	require.Contains(t, out, "held namespaces with a BackupSchedule, whose data this restore does not bring back: orders;")
	require.FileExists(t, filepath.Join(n.dataDir, hold.NamespaceDir, "orders.data"))
	require.NoFileExists(t, filepath.Join(n.dataDir, hold.NamespaceDir, "web.data"))

	c, _ := n.start(t)
	require.NoError(t, c.ReleaseHold(ctx, nil))
	sensorRuns := func(ns v1.NamespaceName) int {
		runs, err := c.List(ctx, v1.KindWorkflowRun, ns)
		require.NoError(t, err)
		k := 0
		for _, r := range runs {
			if strings.HasPrefix(string(r.GetName()), "s-") {
				k++
			}
		}
		return k
	}
	backups := func() []v1.Object {
		l, err := c.List(ctx, v1.KindBackup, "orders")
		require.NoError(t, err)
		return l
	}
	require.Eventually(t, func() bool { return sensorRuns("web") > 0 }, 15*time.Second, 20*time.Millisecond, "web's timers fire")
	require.Never(t, func() bool { return sensorRuns("orders") > 0 || len(backups()) > 0 }, time.Second, 50*time.Millisecond,
		"orders' timers and slots wait")

	_, err = c.Create(ctx, &v1.Backup{TypeMeta: tmeta(v1.KindBackup), ObjectMeta: nsObj("orders", "by-api"),
		Spec: v1.BackupSpec{Schedule: "nightly"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return phase(t, c, "by-api") == v1.RunSucceeded }, 15*time.Second, 20*time.Millisecond,
		"a Backup by API runs in the held namespace\n%s", n.logs.String())
	require.Len(t, backups(), 1)

	require.NoError(t, c.ReleaseNamespaceHold(ctx, []string{"orders"}))
	require.Eventually(t, func() bool { return sensorRuns("orders") > 0 && len(backups()) == 2 }, 15*time.Second, 20*time.Millisecond,
		"after the release orders' timers fire and its due slot creates a Backup")
}

func phase(t *testing.T, c *sdk.Client, name v1.ObjectName) v1.RunPhase {
	t.Helper()
	obj, err := c.Get(context.Background(), v1.KindBackup, "orders", name)
	require.NoError(t, err)
	return obj.(*v1.Backup).Status.Phase
}
