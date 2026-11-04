package backup

import (
	"context"
	"errors"
	"sync"
	"testing"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/workload"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/kvstore/memory"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// testStore is a member kind that no code of the backup names: a member's file at test/<name>/ holds one record, its
// name, and each destination notes its prepares, releases, loads and clears in log; fail and prepare are the errors of
// a named destination's load and Prepare.
type testStore struct {
	log           *[]string
	mu            *sync.Mutex
	fail, prepare map[v1.ObjectName]error
}

const kindTestStore v1.Kind = "TestStore"

func (testStore) Kind() v1.Kind { return kindTestStore }

func (testStore) Write(ctx context.Context, w workload.FileWriter, _ v1.NamespaceName, m workload.Member) (workload.MemberFile, error) {
	f, err := w.File(ctx, "test/"+string(m.Name), func(emit func(snapshot.Record) error) error {
		return emit(snapshot.Record{Key: []byte("name"), Value: []byte(m.Name)})
	})
	return workload.MemberFile{Kind: m.Kind, Name: m.Name, Files: []backup.StoreFile{f}}, err
}

func (testStore) Parse(seg []string) (v1.BackupMember, int, bool) {
	if len(seg) < 3 || seg[0] != "test" {
		return v1.BackupMember{}, 0, false
	}
	return v1.BackupMember{Kind: kindTestStore, Name: v1.ObjectName(seg[1])}, 0, true
}

func (k testStore) Destination(_ context.Context, _ v1.NamespaceName, rm v1.RestoreMember) (workload.Destination, error) {
	return testDestination{k, rm.As}, nil
}

type testDestination struct {
	testStore
	name v1.ObjectName
}

func (d testDestination) note(what string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	*d.log = append(*d.log, what)
}

func (d testDestination) Prepare(context.Context) (func(), error) {
	if err := d.prepare[d.name]; err != nil {
		return nil, err
	}
	d.note("prepare " + string(d.name))
	return func() { d.note("release " + string(d.name)) }, nil
}

func (testDestination) IsEmpty(context.Context) (bool, error) { return true, nil }

func (d testDestination) Load(ctx context.Context, f workload.MemberFile, open workload.RecordOpener) error {
	rec, err := open(ctx, f.Files[0])()
	if err != nil {
		return err
	}
	d.note("load " + string(rec.Value) + " into " + string(d.name))
	return d.fail[d.name]
}

func (d testDestination) Clear(context.Context) error {
	d.note("clear " + string(d.name))
	return nil
}

// keySecrets resolves any Secret to the target's credential and the owner's identity.
type keySecrets struct{ identity string }

func (s keySecrets) ResolveEnv(context.Context, auth.Identity, v1.NamespaceName, []string) (map[string]string, error) {
	return map[string]string{keyAccessKeyID: "AKIDOWNER", keySecretAccessKey: "s", keyAgeIdentity: s.identity}, nil
}

// noteHold is a namespace hold that notes each namespace it holds.
type noteHold struct {
	hold.Gate
	hold.NamespaceGate
	mu   sync.Mutex
	held []v1.NamespaceName
}

func (h *noteHold) HoldNamespace(ns v1.NamespaceName, _ hold.Marker) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.held = append(h.held, ns)
	return nil
}

func (*noteHold) ReleaseNamespaceMarker(v1.NamespaceName, hold.Marker) error { return nil }

// kindsCopy writes ms with kinds, sealed to a new owner and recovery key, as Backup b1 of orders on an S3 stub, and
// returns that Backup and the Secrets a Restore of it reads.
func kindsCopy(t *testing.T, ms []workload.Member, kinds ...workload.MemberKind) (*v1.Backup, keySecrets) {
	t.Helper()
	ctx := context.Background()
	owner, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	recovery, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	sealer, err := envelope.Parse([]string{owner.Recipient().String(), recovery.Recipient().String()})
	require.NoError(t, err)
	s := s3stub.New(t, nil)
	target, err := gocloud.OpenWith(ctx, s.URL(), gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	const path = "orders/b1-0123456789abcdef0123456789abcdef/"
	_, err = workload.Write(ctx, "orders", ms, kinds, workload.Options{Target: target, Path: path, Seal: sealer.Seal(),
		Recipients: sealer.Keys().Recipients})
	require.NoError(t, err)
	b := &v1.Backup{ObjectMeta: v1.ObjectMeta{Namespace: "orders", Name: "b1"},
		Status: v1.BackupStatus{Phase: v1.RunSucceeded, Target: s.URL(), Path: path}}
	return b, keySecrets{identity: owner.String()}
}

func restoreOf(ms ...v1.RestoreMember) *v1.Restore {
	return &v1.Restore{ObjectMeta: v1.ObjectMeta{Namespace: "orders", Name: "r1"}, Spec: v1.RestoreSpec{Backup: "b1",
		Source: v1.RestoreSource{CredentialsSecret: "backup-reader", IdentitySecret: "owner-key"}, Members: ms}}
}

// scenario: a-new-kind-is-one-type (ADR-0311) — a test-only kind, the service's one kind: a copy of two of its members
// is written and listed, and a Restore built in memory (the API admits only the five kinds) loads the first, fails
// on the second and clears both, Failed; a second load refused with ErrNotEmpty fails NotEmpty, and only the first is
// cleared.
func TestKinds_ATestKindNeedsNoServiceCode(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var log []string
	kind := testStore{log: &log, mu: &sync.Mutex{}, fail: map[v1.ObjectName]error{}}
	b, secrets := kindsCopy(t, []workload.Member{{Kind: kindTestStore, Name: "a"}, {Kind: kindTestStore, Name: "b"}}, kind)
	src, err := gocloud.OpenWith(ctx, b.Status.Target, gocloud.OpenOptions{Static: &gocloud.StaticKey{ID: "AKIDOWNER", Secret: "s"}})
	require.NoError(t, err)
	c, err := workload.ListCopy(ctx, src, b.Status.Path, []workload.MemberKind{kind}, workload.DefaultLimits())
	require.NoError(t, err)
	require.True(t, c.Whole)
	require.Equal(t, []v1.ObjectName{"a", "b"}, []v1.ObjectName{c.Members[0].Name, c.Members[1].Name})
	require.Equal(t, []v1.Kind{kindTestStore, kindTestStore}, []v1.Kind{c.Members[0].Kind, c.Members[1].Kind})

	rs, err := New(Deps{Store: store.New(storemem.New()), Secrets: secrets, Kinds: []workload.MemberKind{kind}})
	require.NoError(t, err)
	restore := restoreOf(v1.RestoreMember{Kind: kindTestStore, Name: "a", As: "a2"}, v1.RestoreMember{Kind: kindTestStore, Name: "b", As: "b2"})
	for name, c := range map[string]struct {
		err    error
		reason string
		log    []string
	}{
		"failed": {fault.Unavailablef("test", "b2 is down"), reasonFailed, []string{"prepare a2", "prepare b2", "load a into a2",
			"load b into b2", "clear a2", "clear b2", "release a2", "release b2"}},
		"refused": {errors.Join(workload.ErrNotEmpty, fault.Conflictf("test", "b2 holds data")), ReasonNotEmpty, []string{"prepare a2",
			"prepare b2", "load a into a2", "load b into b2", "clear a2", "release a2", "release b2"}},
	} {
		log = nil
		kind.fail["b2"] = c.err
		reason, err := rs.Restores.load(ctx, restore, b)
		require.Error(t, err, name)
		require.Equal(t, c.reason, reason, name)
		require.Equal(t, c.log, log, name)
	}
}

// ADR-0311 Decision 3: every Prepare runs before the first load, and one that fails releases those before it and clears
// nothing: the Restore fails Failed, with no load.
func TestRestore_AFailedPrepareReleasesTheEarlierOnes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var log []string
	kind := testStore{log: &log, mu: &sync.Mutex{}, prepare: map[v1.ObjectName]error{"b2": fault.Unavailablef("test", "b2 stops no worker")}}
	b, secrets := kindsCopy(t, []workload.Member{{Kind: kindTestStore, Name: "a"}, {Kind: kindTestStore, Name: "b"}}, kind)
	rs, err := New(Deps{Store: store.New(storemem.New()), Secrets: secrets, Kinds: []workload.MemberKind{kind}})
	require.NoError(t, err)

	reason, err := rs.Restores.load(ctx, restoreOf(v1.RestoreMember{Kind: kindTestStore, Name: "a", As: "a2"},
		v1.RestoreMember{Kind: kindTestStore, Name: "b", As: "b2"}), b)
	require.ErrorContains(t, err, "b2 stops no worker")
	require.Equal(t, reasonFailed, reason)
	require.Equal(t, []string{"prepare a2", "release a2"}, log)
}

// scenario: an-unserved-kind-fails-by-name (ADR-0311 Decision 6) — a copy that holds a KVStore restored in place by a
// service whose one kind is a test kind fails Failed naming KVStore, before the hold and before any load.
func TestRestore_AnUnservedKindFailsByName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var log []string
	kind := testStore{log: &log, mu: &sync.Mutex{}}
	kv := memory.New()
	require.NoError(t, kv.Put(ctx, "orders/orders-state/t/1", []byte("one")))
	export := kv.(interface{ SnapshotPrefix(string) snapshot.Source }).SnapshotPrefix
	b, secrets := kindsCopy(t, []workload.Member{{Kind: kindTestStore, Name: "a"}, {Kind: v1.KindKVStore, Name: "orders-state"}},
		kind, workload.KVStores(nil, export, nil))
	h := &noteHold{Gate: hold.Never, NamespaceGate: hold.NeverNamespace}
	rs, err := New(Deps{Store: store.New(storemem.New()), Secrets: secrets, Kinds: []workload.MemberKind{kind}, Hold: h})
	require.NoError(t, err)

	reason, err := rs.Restores.load(ctx, restoreOf(v1.RestoreMember{Kind: kindTestStore, Name: "a", InPlace: true},
		v1.RestoreMember{Kind: v1.KindKVStore, Name: "orders-state", InPlace: true}), b)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
	require.ErrorContains(t, err, "no member kind serves KVStore")
	require.Equal(t, reasonFailed, reason)
	require.Empty(t, h.held, "nothing is held")
	require.Empty(t, log, "nothing is loaded")
}

// ADR-0311 Decision 6: New refuses a kind that two of its kinds serve, and a nil kind, fault.Invalid naming it.
func TestNew_RefusesAKindServedTwice(t *testing.T) {
	t.Parallel()
	kind := testStore{log: &[]string{}, mu: &sync.Mutex{}}
	for want, kinds := range map[string][]workload.MemberKind{
		"two member kinds serve TestStore": {kind, kind},
		"member kind 1 is nil":             {kind, nil},
	} {
		_, err := New(Deps{Store: store.New(storemem.New()), Secrets: noSecrets{}, Kinds: kinds})
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
		require.ErrorContains(t, err, want)
	}
}
