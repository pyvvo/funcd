package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const backupScheduleManifest = `apiVersion: funcd.io/v1alpha1
kind: BackupSchedule
metadata:
  name: orders-nightly
  namespace: team-a
  resourceGroup: rg1
spec:
  schedule: 0 3 * * *
  timeZone: Europe/Paris
  ttl: 48h
  scope:
    kvStores:
      - orders-state
  target:
    url: s3://orders-backup?region=eu-west-3
    credentialsSecret: backup-writer
  encryption:
    recipients:
      - %s
      - %s
`

const backupManifest = `apiVersion: funcd.io/v1alpha1
kind: Backup
metadata:
  name: before-migration
  namespace: team-a
  resourceGroup: rg1
spec:
  schedule: orders-nightly
  ttl: %s
`

const restoreManifest = `apiVersion: funcd.io/v1alpha1
kind: Restore
metadata:
  name: orders-back
  namespace: team-a
  resourceGroup: rg1
spec:
  backup: before-migration
  source:
    credentialsSecret: backup-reader
    identitySecret: owner-key
  members:
    - kind: KVStore
      name: orders-state
      %s
`

// scenario: cli-apply-workload-backup (ADR-0222 Decisions 1, 6, 10) — `funcdctl apply` creates a BackupSchedule, a
// Backup and a Restore through the REAL control plane (stampTypeMeta, the backup admissions and the store) for a
// developer; a viewer reads them and creates none; a Backup's ttl above its schedule's, a changed Backup spec and an
// in-place Restore by a developer are refused.
func TestScenarioCLIApplyWorkloadBackup(t *testing.T) {
	t.Parallel()
	const adminToken = "admin-secret"
	authz := rbac.New()
	st := store.New(memory.New())
	h, err := controlplane.NewServer(controlplane.Deps{
		Store: st, Authorizer: authz,
		Credentials: middleware.NewStaticCredentials(map[string]auth.Identity{
			devToken:    {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
			viewerToken: {Subject: "viewer", Role: auth.RoleViewer, Namespaces: []v1.NamespaceName{"team-a"}},
			adminToken:  {Subject: "ops", Role: auth.RoleAdmin},
		}),
		Admissions: []admission.Admission{admission.NewBackupScheduleAdmission("s3://store?region=eu-west-3"),
			admission.NewBackupSpecImmutableAdmission(), admission.NewBackupTTLAdmission(storeList{st}),
			admission.NewRestoreInPlaceAdmission(authz, storeList{st}, nil)},
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client := func(token string) *sdk.Client {
		c, err := sdk.New(srv.URL, sdk.WithToken(token))
		require.NoError(t, err)
		return c
	}
	dev, viewer, admin := client(devToken), client(viewerToken), client(adminToken)
	var recipients [2]string
	for i := range recipients {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		recipients[i] = id.Recipient().String()
	}
	schedule := fmt.Sprintf(backupScheduleManifest, recipients[0], recipients[1])

	require.Equal(t, fault.Forbidden, fault.KindOf(execCLI(&bytes.Buffer{}, viewer, "apply", "-f", writeManifest(t, schedule))))
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, dev, "apply", "-f", writeManifest(t, schedule)))
	require.Contains(t, out.String(), "applied BackupSchedule/orders-nightly")
	out.Reset()
	require.NoError(t, execCLI(&out, viewer, "get", "backupschedules", "orders-nightly", "-n", "team-a", "-o", "json"))
	require.Contains(t, out.String(), `"apiVersion": "funcd.io/v1alpha1"`, "the server stamps TypeMeta")
	require.Contains(t, out.String(), `"timeZone": "Europe/Paris"`)

	err = execCLI(&bytes.Buffer{}, dev, "apply", "-f", writeManifest(t, fmt.Sprintf(backupManifest, "72h")))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a ttl above the schedule's")
	require.ErrorContains(t, err, "spec.ttl")
	require.NoError(t, execCLI(&bytes.Buffer{}, dev, "apply", "-f", writeManifest(t, fmt.Sprintf(backupManifest, "24h"))))
	err = execCLI(&bytes.Buffer{}, dev, "apply", "-f", writeManifest(t, fmt.Sprintf(backupManifest, "12h")))
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a Backup's spec is immutable")
	require.ErrorContains(t, err, "immutable")

	inPlace := fmt.Sprintf(restoreManifest, "inPlace: true")
	require.Equal(t, fault.Forbidden, fault.KindOf(execCLI(&bytes.Buffer{}, dev, "apply", "-f", writeManifest(t, inPlace))))
	out.Reset()
	require.NoError(t, execCLI(&out, dev, "apply", "-f", writeManifest(t, fmt.Sprintf(restoreManifest, "as: orders-state-r"))))
	require.Contains(t, out.String(), "applied Restore/orders-back")
	require.NoError(t, execCLI(&bytes.Buffer{}, admin, "delete", "restore", "orders-back", "-n", "team-a"))
	require.NoError(t, execCLI(&bytes.Buffer{}, admin, "apply", "-f", writeManifest(t, inPlace)), "an admin restores in place")
	out.Reset()
	require.NoError(t, execCLI(&out, viewer, "get", "backups", "-n", "team-a"))
	require.True(t, strings.Contains(out.String(), "before-migration"), out.String())
}

// storeList reads the store as the platform's admissions do.
type storeList struct{ st store.Store }

func (r storeList) List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	l, err := r.st.List(ctx, gvk, store.ListOptions{Namespace: ns})
	return l.Items, err
}
