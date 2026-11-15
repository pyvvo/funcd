//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/runner"
	kvmemory "github.com/pyvvo/funcd/internal/kvstore/memory"
	funcdconfig "github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/pkg/funcd"
)

const (
	// The two age recipients of the disaster recovery example, whose identities are published test keys.
	drOwner    = "age1gvngj00ucwlleuk898r0s3umcacr5mxhkv4fy67evygxsmlgpanq43ttm3"
	drRecovery = "age1263g0rath75xcgy9wh9cd6p9tpdn6wq35t5fa5pajnna4u7tgyhsxd384c"
	// drStore is the blob.target that the held platform compares targets with; nothing listens at either endpoint,
	// and no Backup of a held platform runs.
	drStore  = "s3://store?endpoint=http://127.0.0.1:1&region=us-east-1"
	drTarget = "s3://backups?endpoint=http://127.0.0.1:2&region=us-east-1"
)

func drMeta(ns v1.NamespaceName, name v1.ObjectName) v1.ObjectMeta {
	return v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: "rg1"}
}

// drSchedule is BackupSchedule name of default, which copies the KVStore orders to drTarget every night.
func drSchedule(name v1.ObjectName) *v1.BackupSchedule {
	return &v1.BackupSchedule{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBackupSchedule.GVK().APIVersion(), Kind: v1.KindBackupSchedule},
		ObjectMeta: drMeta("default", name),
		Spec: v1.BackupScheduleSpec{Schedule: "0 3 * * *", Scope: v1.BackupScope{KVStores: []v1.ObjectName{"orders"}},
			Target:     v1.BackupTarget{URL: drTarget, CredentialsSecret: "backup-writer"},
			Encryption: v1.BackupEncryption{Recipients: []string{drOwner, drRecovery}}},
	}
}

// drBackup is Backup name of ns, made by hand from the schedule nightly.
func drBackup(ns v1.NamespaceName, name v1.ObjectName) *v1.Backup {
	return &v1.Backup{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBackup.GVK().APIVersion(), Kind: v1.KindBackup},
		ObjectMeta: drMeta(ns, name), Spec: v1.BackupSpec{Schedule: "nightly"}}
}

// drRestore is Restore name of ns, which loads member from the Backup b1.
func drRestore(ns v1.NamespaceName, name v1.ObjectName, member v1.RestoreMember) *v1.Restore {
	return &v1.Restore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindRestore.GVK().APIVersion(), Kind: v1.KindRestore},
		ObjectMeta: drMeta(ns, name), Spec: v1.RestoreSpec{Backup: "b1",
			Source:  v1.RestoreSource{CredentialsSecret: "backup-reader", IdentitySecret: "backup-identity"},
			Members: []v1.RestoreMember{member}}}
}

// drUnlistable is a platform backup target whose listing fails, as one whose bucket does not exist.
type drUnlistable struct{}

func (drUnlistable) Ready(context.Context) error { return errors.New("the backup target is gone") }
func (drUnlistable) Conditional() bool           { return true }
func (drUnlistable) Close() error                { return nil }

func (drUnlistable) List(context.Context) ([]backup.Entry, error) {
	return nil, fault.NotFoundf("drUnlistable", "no bucket backups")
}

func (drUnlistable) Write(context.Context, snapshot.Source, snapshot.Source, snapshot.Source,
	backup.WriteOptions) (backup.Manifest, error) {
	return backup.Manifest{}, errors.New("the backup target is gone")
}

// drUnlistedEnv runs a platform whose platform backup cannot list its target.
func drUnlistedEnv(t *testing.T) *answerEnv {
	t.Helper()
	quiet := slog.New(slog.DiscardHandler)
	sealer, err := envelope.New(envelope.Config{None: true, NoSecrets: true, Logger: quiet})
	require.NoError(t, err)
	r, err := runner.New(runner.Config{Target: drUnlistable{}, Sealer: sealer, Logger: quiet,
		Times: funcdconfig.BackupTimes{Interval: time.Hour, RPO: 2 * time.Hour, RetryInterval: time.Hour}})
	require.NoError(t, err)
	return newAnswerEnv(t, funcd.WithPlatformBackup(r))
}

// drQuarantined is the number of entries that the quarantine of e holds.
func drQuarantined(t *testing.T, e *answerEnv) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, e.api+"/apis/funcd.io/v1alpha1/hold", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+answersAdmin)
	resp, err := e.c.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var ev hold.Evidence
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ev))
	return len(ev.Quarantine)
}

// The answers of the disaster recovery feature's operations, one row per case of each: BackupSchedule, Backup,
// Restore, the hold, the quarantine's return and the platform backup's status.
func TestDisasterRecoveryAnswersMatrix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	since := v1.NewTimestamp(time.Now())

	// held is a held platform: its Backups stay Pending, so the Restores that read them never end.
	dir := t.TempDir()
	require.NoError(t, hold.Write(dir, hold.Marker{Reason: hold.ReasonRestore, Since: since}))
	hd, err := hold.Open(dir)
	require.NoError(t, err)
	held := newAnswerEnv(t, funcd.WithHold(hd), funcd.WithBlobStoreURL(drStore))
	copied := v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders", As: "orders-copy"}
	inPlace := v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders", InPlace: true}
	for _, obj := range []v1.Object{drSchedule("nightly"), drBackup("default", "b1"), drRestore("default", "r1", copied),
		drRestore("default", "active", inPlace), drBackup("loading", "b1"), drRestore("loading", "pending", copied)} {
		create(t, held.c, held.api, obj)
	}
	require.NoError(t, hd.HoldNamespace("spare", hold.Marker{Reason: hold.ReasonData, Since: since}))
	require.NoError(t, hd.HoldNamespace("marked", hold.Marker{Reason: hold.ReasonRestore, Since: since, Restore: "earlier"}))
	require.NoError(t, hd.HoldNamespace("loading", hold.Marker{Reason: hold.ReasonRestore, Since: since, Restore: "pending"}))

	// free is not held, and its start moved the data of three KVStores that do not exist into the quarantine.
	kv := kvmemory.New()
	for _, name := range []string{"old", "full", "lost"} {
		require.NoError(t, kv.Put(ctx, "default/"+name+"/t/k", []byte("kept by the quarantine")))
	}
	free := newAnswerEnv(t, funcd.WithKVStore(kv))
	create(t, free.c, free.api, kvStore("fresh", "t"))
	require.Equal(t, 3, drQuarantined(t, free), "the start quarantines old, full and lost")
	unlisted := drUnlistedEnv(t)

	const (
		apis      = "/apis/funcd.io/v1alpha1"
		schedules = apis + "/namespaces/default/backupschedules"
		backups   = apis + "/namespaces/default/backups"
		restores  = apis + "/namespaces/default/restores"
		release   = apis + "/hold/release"
		giveBack  = apis + "/quarantine/return"
	)
	brokenSchedule := drSchedule("nightly")
	brokenSchedule.Spec.Encryption.Recipients = []string{drOwner}
	unsealed, inside := drSchedule("nightly"), drSchedule("nightly")
	unsealed.Spec.Encryption.Recipients = []string{"age1bogus", "age1other"}
	inside.Spec.Target.URL = drStore
	brokenBackup := drBackup("default", "b1")
	brokenBackup.ResourceGroup = ""
	longer, changedBackup := drBackup("default", "longer"), drBackup("default", "b1")
	longer.Spec.TTL, changedBackup.Spec.TTL = v1.Duration(721*time.Hour), v1.Duration(time.Hour)
	brokenRestore := drRestore("default", "r1", copied)
	brokenRestore.Spec.Members = []v1.RestoreMember{}
	changedRestore := drRestore("default", "r1", v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders", As: "orders-other"})
	scheduled := kindFixture{kind: "BackupSchedule", kinds: "BackupSchedules", path: "backupschedules", name: "nightly",
		json: jsonOf(t, drSchedule("nightly")), broken: jsonOf(t, brokenSchedule), detail: "want at least 2"}
	quarantined := func(name string) string {
		return `{"kind":"KVStore","namespace":"default","name":"` + name + `"}`
	}
	plain := []string{"Content-Type", "text/plain"}

	rows := slices.Concat(
		scheduled.sharedRows(held),
		kindFixture{kind: "Backup", kinds: "Backups", path: "backups", name: "b1", json: jsonOf(t, drBackup("default", "b1")),
			broken: jsonOf(t, brokenBackup), detail: `resourceGroup is required for kind "Backup"`}.sharedRows(held),
		kindFixture{kind: "Restore", kinds: "Restores", path: "restores", name: "r1",
			json: jsonOf(t, drRestore("default", "r1", copied)), broken: jsonOf(t, brokenRestore),
			detail: "spec.members is empty"}.sharedRows(held),
		[]answerRow{
			{op: "createBackupSchedule", when: "is not an age recipient", on: held, ask: ask{
				what: "a recipient that is no age key", method: http.MethodPost, path: schedules, token: answersDev,
				body: scheduled.renamed(jsonOf(t, unsealed), "unsealed"), detail: "is not one X25519"}},
			{op: "createBackupSchedule", when: "names the bucket of blob.target", on: held, ask: ask{
				what: "a target in the blob store", method: http.MethodPost, path: schedules, token: answersDev,
				body: scheduled.renamed(jsonOf(t, inside), "inside"), detail: "is inside the blob store it would copy"}},
			{op: "replaceBackupSchedule", when: "is not an age recipient", on: held, ask: ask{
				what: "a recipient that is no age key", method: http.MethodPut, path: schedules + "/nightly",
				token: answersDev, body: jsonOf(t, unsealed), detail: "is not one X25519"}},
			{op: "replaceBackupSchedule", when: "names the bucket of blob.target", on: held, ask: ask{
				what: "a target in the blob store", method: http.MethodPut, path: schedules + "/nightly",
				token: answersDev, body: jsonOf(t, inside), detail: "is inside the blob store it would copy"}},

			{op: "createBackup", when: "is longer than the ttl", on: held, ask: ask{what: "a ttl over the schedule's",
				method: http.MethodPost, path: backups, token: answersDev, body: jsonOf(t, longer),
				detail: `spec.ttl 721h exceeds the ttl 720h of schedule "nightly"`}},
			{op: "replaceBackup", when: "The spec differs", on: held, ask: ask{what: "another ttl", method: http.MethodPut,
				path: backups + "/b1", token: answersDev, body: jsonOf(t, changedBackup), detail: "Backup spec is immutable"}},

			{op: "createRestore", when: "to update the Namespace", on: held, ask: ask{what: "a developer's in-place Restore",
				method: http.MethodPost, path: restores, token: answersDev,
				body:   jsonOf(t, drRestore("default", "rewind", inPlace)),
				detail: `an in-place Restore needs update on Namespace "default"`}},
			{op: "createRestore", when: "another in-place Restore", on: held, ask: ask{
				what: "a second in-place Restore", method: http.MethodPost, path: restores, token: answersAdmin,
				body:   jsonOf(t, drRestore("default", "second", inPlace)),
				detail: `in-place Restore active of namespace "default" has not ended`}},
			{op: "createRestore", when: "whose load did not complete", on: held, ask: ask{
				what: "an in-place Restore while a load's hold stands", method: http.MethodPost,
				path: apis + "/namespaces/marked/restores", token: answersAdmin,
				body:   jsonOf(t, drRestore("marked", "again", inPlace)),
				detail: `is held by in-place Restore "earlier", whose load did not complete`}},
			{op: "replaceRestore", when: "The spec differs", on: held, ask: ask{what: "another member", method: http.MethodPut,
				path: restores + "/r1", token: answersDev, body: jsonOf(t, changedRestore), detail: "Restore spec is immutable"}},
			{op: "deleteRestore", when: "loads a member in place", on: held, ask: ask{
				what: "an in-place Restore that has not ended", method: http.MethodDelete, path: restores + "/active",
				token: answersDev, detail: "has not ended: delete it once it has"}},

			{op: "getHold", on: held, ask: ask{what: "the hold", method: http.MethodGet, path: apis + "/hold",
				token: answersAdmin, detail: `"held":true`}},
			{op: "getHold", when: "carries no bearer token", on: held, ask: ask{what: "no credential",
				method: http.MethodGet, path: apis + "/hold", detail: "missing bearer token"}},
			{op: "getHold", when: "only an admin's does", on: held, ask: ask{what: "a developer", method: http.MethodGet,
				path: apis + "/hold", token: answersDev, detail: "hold get denied"}},

			{op: "releaseHold", on: held, ask: ask{what: "a namespace held for its data", method: http.MethodPost,
				path: release, token: answersAdmin, body: `{"namespaces":["spare"]}`}},
			{op: "releaseHold", when: "sets both advance and namespaces", on: held, ask: ask{
				what: "an advance beside namespaces", method: http.MethodPost, path: release, token: answersAdmin,
				body: `{"namespaces":["elsewhere"],"advance":["default/files"]}`, detail: "advance applies to the platform's"}},
			{op: "releaseHold", when: "An entry of advance", on: held, ask: ask{what: "an advance that names no EventSource",
				method: http.MethodPost, path: release, token: answersAdmin, body: `{"advance":["files"]}`,
				detail: `advance "files": want`}},
			{op: "releaseHold", when: "The body names no namespace", on: free, ask: ask{what: "a platform that is not held",
				method: http.MethodPost, path: release, token: answersAdmin, body: `{}`, detail: "the platform is not held"}},
			{op: "releaseHold", when: "A namespace of the body is not held", on: held, ask: ask{
				what: "a namespace that is not held", method: http.MethodPost, path: release, token: answersAdmin,
				body: `{"namespaces":["unheld"]}`, detail: `namespace "unheld" is not held`}},
			{op: "releaseHold", when: "Pending or Running", on: held, ask: ask{what: "a namespace that a Restore holds",
				method: http.MethodPost, path: release, token: answersAdmin, body: `{"namespaces":["loading"]}`,
				detail: "is held by Restore pending"}},
			{op: "releaseHold", when: "carries no bearer token", on: free, ask: ask{what: "no credential",
				method: http.MethodPost, path: release, body: `{}`, detail: "missing bearer token"}},
			{op: "releaseHold", when: "only an admin's does", on: free, ask: ask{what: "a developer", method: http.MethodPost,
				path: release, token: answersDev, body: `{}`, detail: "hold update denied"}},
			{op: "releaseHold", when: "The query holds a parameter", on: free, ask: ask{what: "an unknown query parameter",
				method: http.MethodPost, path: release + "?force=true", token: answersAdmin, body: `{}`,
				detail: "unknown query parameter"}},
			{op: "releaseHold", when: "The body is missing or is not JSON", on: free, ask: ask{what: "a body that is not JSON",
				method: http.MethodPost, path: release, token: answersAdmin, body: "{", detail: "unexpected end of JSON input"}},
			{op: "releaseHold", when: "larger than 1 MiB", on: free, ask: ask{what: "a body over 1 MiB",
				method: http.MethodPost, path: release, token: answersAdmin, body: tooLarge(),
				detail: "request body is too large limit=1048576 bytes"}},
			{op: "releaseHold", when: "The Content-Type of the body", on: free, ask: ask{
				what: "a body that is not JSON by its Content-Type", method: http.MethodPost, path: release,
				token: answersAdmin, header: plain, body: `{}`, detail: "unknown content type"}},
			{op: "releaseHold", when: "The body breaks the schema", on: free, ask: ask{what: "a field the schema does not know",
				method: http.MethodPost, path: release, token: answersAdmin, body: `{"bogus":1}`, detail: "unexpected property"}},

			{op: "returnQuarantined", on: free, setup: func(t *testing.T, e *answerEnv) { create(t, e.c, e.api, kvStore("old", "t")) },
				ask: ask{what: "the data of a KVStore applied again", method: http.MethodPost, path: giveBack,
					token: answersAdmin, body: quarantined("old")}},
			{op: "returnQuarantined", when: "The kind is none of", on: free, ask: ask{what: "a kind the quarantine keeps none of",
				method: http.MethodPost, path: giveBack, token: answersAdmin,
				body: `{"kind":"Lake","namespace":"default","name":"lake"}`, detail: `kind "Lake"`}},
			{op: "returnQuarantined", when: "names no volume", on: free, ask: ask{what: "a volume with a KVStore",
				method: http.MethodPost, path: giveBack, token: answersAdmin,
				body:   `{"kind":"KVStore","namespace":"default","name":"old","volume":"state"}`,
				detail: `volume "state" applies to kind Function`}},
			{op: "returnQuarantined", when: "holds no entry of the object", on: free, ask: ask{what: "an object with no entry",
				method: http.MethodPost, path: giveBack, token: answersAdmin, body: quarantined("fresh"),
				detail: "no quarantined data of"}},
			{op: "returnQuarantined", when: "does not exist", on: free, ask: ask{what: "an object that does not exist",
				method: http.MethodPost, path: giveBack, token: answersAdmin, body: quarantined("lost"),
				detail: "does not exist: apply it again"}},
			{op: "returnQuarantined", when: "The object holds data", on: free,
				setup: func(t *testing.T, e *answerEnv) {
					create(t, e.c, e.api, kvStore("full", "t"))
					require.NoError(t, kv.Put(context.Background(), "default/full/t/new", []byte("written since")))
				},
				ask: ask{what: "an object that holds data", method: http.MethodPost, path: giveBack, token: answersAdmin,
					body: quarantined("full"), detail: "holds 1 keys"}},
			{op: "returnQuarantined", when: "keeps no quarantine of that kind", on: free, ask: ask{
				what: "a volume while volumes are off", method: http.MethodPost, path: giveBack, token: answersAdmin,
				body:   `{"kind":"Function","namespace":"default","name":"api","volume":"state"}`,
				detail: "no Function quarantine on this daemon"}},
			{op: "returnQuarantined", when: "carries no bearer token", on: free, ask: ask{what: "no credential",
				method: http.MethodPost, path: giveBack, body: quarantined("lost"), detail: "missing bearer token"}},
			{op: "returnQuarantined", when: "only an admin's does", on: free, ask: ask{what: "a developer",
				method: http.MethodPost, path: giveBack, token: answersDev, body: quarantined("lost"),
				detail: "hold update denied"}},
			{op: "returnQuarantined", when: "The query holds a parameter", on: free, ask: ask{
				what: "an unknown query parameter", method: http.MethodPost, path: giveBack + "?force=true",
				token: answersAdmin, body: quarantined("lost"), detail: "unknown query parameter"}},
			{op: "returnQuarantined", when: "The body is missing or is not JSON", on: free, ask: ask{
				what: "a body that is not JSON", method: http.MethodPost, path: giveBack, token: answersAdmin, body: "{",
				detail: "unexpected end of JSON input"}},
			{op: "returnQuarantined", when: "larger than 1 MiB", on: free, ask: ask{what: "a body over 1 MiB",
				method: http.MethodPost, path: giveBack, token: answersAdmin, body: tooLarge(),
				detail: "request body is too large limit=1048576 bytes"}},
			{op: "returnQuarantined", when: "The Content-Type of the body", on: free, ask: ask{
				what: "a body that is not JSON by its Content-Type", method: http.MethodPost, path: giveBack,
				token: answersAdmin, header: plain, body: quarantined("lost"), detail: "unknown content type"}},
			{op: "returnQuarantined", when: "The body breaks the schema", on: free, ask: ask{
				what: "a field the schema does not know", method: http.MethodPost, path: giveBack, token: answersAdmin,
				body: strings.Replace(quarantined("lost"), `{`, `{"bogus":1,`, 1), detail: "unexpected property"}},

			{op: "getPlatformBackup", on: held, ask: ask{what: "a platform with no platform backup", method: http.MethodGet,
				path: apis + "/platformbackup", token: answersAdmin, detail: `"enabled":false`}},
			{op: "getPlatformBackup", when: "carries no bearer token", on: held, ask: ask{what: "no credential",
				method: http.MethodGet, path: apis + "/platformbackup", detail: "missing bearer token"}},
			{op: "getPlatformBackup", when: "only an admin's does", on: held, ask: ask{what: "a developer",
				method: http.MethodGet, path: apis + "/platformbackup", token: answersDev,
				detail: "platform backup status denied"}},
			{op: "getPlatformBackup", when: "could not list backup.target", on: unlisted, ask: ask{
				what: "a target that cannot be listed", method: http.MethodGet, path: apis + "/platformbackup",
				token: answersAdmin, detail: "list the backup target: the bucket does not exist"}},
		},
	)
	runMatrix(t, rows)
}
