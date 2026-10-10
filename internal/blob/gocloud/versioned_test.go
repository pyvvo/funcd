package gocloud_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// versionedStore opens a store with prefix st/ on a versioned stub whose clock starts an hour ago, so a restore point
// on it lies in the past.
func versionedStore(t *testing.T) (*s3stub.Stub, blob.Bucket, blob.Versioned) {
	t.Helper()
	s := s3stub.New(t, clock.NewManual(time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)))
	s.Set(func(s *s3stub.Stub) { s.Versioning = "Enabled" })
	b, err := gocloud.OpenWith(context.Background(), s.URL("prefix=st/"), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(t, "AKIDSTORE")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	v, ok := b.(blob.Versioned)
	require.True(t, ok, "an s3:// bucket is Versioned")
	return s, b, v
}

func put(t *testing.T, b blob.Bucket, key, data string) {
	t.Helper()
	require.NoError(t, b.Put(context.Background(), key, []byte(data), blob.PutOptions{ContentType: "text/plain", Metadata: map[string]string{"k": data}}))
}

// scenario: remote-restore-at — after T key a was overwritten, b created, c deleted and d overwritten with its
// version of T expired: restoring to T gives a its bytes of T and c back, deletes b and d and lists them in
// NoVersion, and removes no version; a T older than blob.versionRetention is fault.Invalid.
func TestScenarioRemoteRestoreAt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, b, v := versionedStore(t)
	put(t, b, "a", "a0")
	put(t, b, "c", "c0")
	put(t, b, "d", "d0")
	s.Clock.Advance(time.Minute)
	at := s.Clock.Now()
	s.Clock.Advance(time.Minute)
	put(t, b, "a", "a1")
	put(t, b, "b", "b1")
	require.NoError(t, b.Delete(ctx, "c"))
	put(t, b, "d", "d1")
	s.Set(func(s *s3stub.Stub) { s.ExpireNoncurrentLocked("st/d") })
	before := map[string]int{}
	for _, k := range []string{"st/a", "st/b", "st/c", "st/d"} {
		before[k] = s.VersionCount(k)
	}

	_, err := v.RestoreAt(ctx, at, time.Minute)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "a T older than blob.versionRetention")
	_, err = v.RestoreAt(ctx, at, 0)
	require.Equal(t, fault.Invalid, fault.KindOf(err), "blob.versionRetention unset")

	rep, err := v.RestoreAt(ctx, at, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, blob.RestoreReport{Copied: 2, Deleted: 2, NoVersion: []string{"b", "d"}}, rep)
	for key, want := range map[string]string{"a": "a0", "c": "c0"} {
		got, err := b.Get(ctx, key)
		require.NoError(t, err)
		require.Equal(t, want, string(got))
		a, err := b.Attributes(ctx, key)
		require.NoError(t, err)
		require.Equal(t, map[string]string{"k": want}, a.Metadata, "the copy keeps the version's metadata")
	}
	for _, key := range []string{"b", "d"} {
		_, err := b.Get(ctx, key)
		require.Equal(t, fault.NotFound, fault.KindOf(err), key)
	}
	for k, n := range before {
		require.Equal(t, n+1, s.VersionCount(k), "%s keeps every version and gains one", k)
	}

	rep, err = v.RestoreAt(ctx, at, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, blob.RestoreReport{Unchanged: 4}, rep, "a second restore to T changes nothing")
}

// A version over CopyObject's 5 GiB refuses the restore before any write.
func TestRestoreAtRefusesLargeVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, b, v := versionedStore(t)
	put(t, b, "a", "a0")
	put(t, b, "big", "big0")
	s.SetSize("st/big", 5<<30+1)
	s.Clock.Advance(time.Minute)
	at := s.Clock.Now()
	s.Clock.Advance(time.Minute)
	put(t, b, "a", "a1")
	put(t, b, "big", "big1")
	puts := len(s.PutKeys(""))

	_, err := v.RestoreAt(ctx, at, 24*time.Hour)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Contains(t, err.Error(), `"st/big"`)
	require.Len(t, s.PutKeys(""), puts, "nothing was copied")
	got, err := b.Get(ctx, "a")
	require.NoError(t, err)
	require.Equal(t, "a1", string(got))
}

// An error mid-restore keeps the keys already restored and empties nothing; a rerun completes the restore.
func TestRestoreAtErrorKeepsDone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, b, v := versionedStore(t)
	put(t, b, "a", "a0")
	put(t, b, "b", "b0")
	s.Clock.Advance(time.Minute)
	at := s.Clock.Now()
	s.Clock.Advance(time.Minute)
	put(t, b, "a", "a1")
	put(t, b, "b", "b1")
	s.Set(func(s *s3stub.Stub) { s.FailPut = func(key string) bool { return key == "st/b" } })

	rep, err := v.RestoreAt(ctx, at, 24*time.Hour)
	require.Error(t, err)
	require.Equal(t, 1, rep.Copied)
	for key, want := range map[string]string{"a": "a0", "b": "b1"} {
		got, err := b.Get(ctx, key)
		require.NoError(t, err)
		require.Equal(t, want, string(got), key)
	}

	s.Set(func(s *s3stub.Stub) { s.FailPut = nil })
	rep, err = v.RestoreAt(ctx, at, 24*time.Hour)
	require.NoError(t, err)
	require.Equal(t, blob.RestoreReport{Copied: 1, Unchanged: 1}, rep)
	got, err := b.Get(ctx, "b")
	require.NoError(t, err)
	require.Equal(t, "b0", string(got))
}

// ADR-0208 Decision 2: Versioning reads the status and the lock; a refused or unimplemented read is not Enabled, a
// missing bucket fault.NotFound, a dropped connection fault.Unavailable; a lock read error is no lock.
func TestVersioningAnswers(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		set  func(s *s3stub.Stub)
		want blob.Versioning
		kind fault.Kind
	}{
		"enabled with lock": {set: func(s *s3stub.Stub) { s.Versioning, s.ObjectLock = "Enabled", true }, want: blob.Versioning{Enabled: true, ObjectLock: true}},
		"enabled":           {set: func(s *s3stub.Stub) { s.Versioning = "Enabled" }, want: blob.Versioning{Enabled: true}},
		"lock read refused": {set: func(s *s3stub.Stub) { s.Versioning, s.ObjectLock, s.LockStatus = "Enabled", true, 403 }, want: blob.Versioning{Enabled: true}},
		"suspended":         {set: func(s *s3stub.Stub) { s.Versioning = "Suspended" }},
		"never enabled":     {set: func(*s3stub.Stub) {}},
		"refused":           {set: func(s *s3stub.Stub) { s.VersioningStatus = 403 }},
		"unimplemented":     {set: func(s *s3stub.Stub) { s.VersioningStatus = 501 }},
		"no bucket":         {set: func(s *s3stub.Stub) { s.VersioningStatus = 404 }, kind: fault.NotFound},
		"dropped":           {set: func(s *s3stub.Stub) { s.VersioningStatus = s3stub.Drop }, kind: fault.Unavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := s3stub.New(t, nil)
			s.Set(c.set)
			b, err := gocloud.OpenWith(context.Background(), s.URL(), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(t, "AKID")})
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			got, err := b.(blob.Versioned).Versioning(context.Background())
			if c.kind != "" {
				require.Equal(t, c.kind, fault.KindOf(err), "%v", err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, c.want, got)
		})
	}
	for _, url := range []string{"mem://", gocloud.FileURL(t.TempDir())} {
		b, err := gocloud.Open(context.Background(), url)
		require.NoError(t, err)
		_, ok := b.(blob.Versioned)
		require.False(t, ok, "%s is not Versioned", url)
		require.NoError(t, b.Close())
	}
}
