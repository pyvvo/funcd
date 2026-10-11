package blobmirror_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/blobmirror"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

// start is the first hour every mirror test's clocks begin at.
var start = time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // a test fixture

// fixture is a local store, a box-mode S3 target and a mirror between them, on one manual clock.
type fixture struct {
	t      *testing.T
	dir    string
	store  blob.Bucket
	stub   *s3stub.Stub
	target blob.Bucket
	clock  *clock.Manual
	mirror blobmirror.Mirror
	cfg    blobmirror.Config
	logs   *syncBuffer
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newFixture(t *testing.T, mod func(*blobmirror.Config)) *fixture {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	f := &fixture{t: t, dir: filepath.Join(base, "blob"), clock: clock.NewManual(start), logs: &syncBuffer{}}
	require.NoError(t, os.MkdirAll(f.dir, 0o700))
	var err error
	f.store, err = gocloud.Open(ctx, gocloud.FileURL(f.dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.store.Close() })
	f.stub = s3stub.New(t, f.clock)
	f.stub.Set(func(s *s3stub.Stub) { s.Box = true })
	f.target, err = gocloud.OpenWith(ctx, f.stub.URL(), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(t, "AKIDBOX")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.target.Close() })
	f.cfg = blobmirror.Config{Dir: f.dir, Target: f.target, Interval: time.Hour, Rebaseline: 720 * time.Hour,
		Retention: 720 * time.Hour, RetryInterval: 5 * time.Minute, Logger: slog.New(slog.NewTextHandler(f.logs, nil))}
	if mod != nil {
		mod(&f.cfg)
	}
	f.mirror, err = blobmirror.New(f.cfg)
	require.NoError(t, err)
	blobmirror.Set(f.mirror, f.clock, nil, nil, nil)
	return f
}

func (f *fixture) put(key, data string) {
	f.t.Helper()
	require.NoError(f.t, f.store.Put(context.Background(), key, []byte(data),
		blob.PutOptions{ContentType: "text/plain", Metadata: map[string]string{"of": key}}))
}

func (f *fixture) del(key string) {
	f.t.Helper()
	require.NoError(f.t, f.store.Delete(context.Background(), key))
}

func (f *fixture) run() blobmirror.Manifest {
	f.t.Helper()
	m, err := f.mirror.Run(context.Background())
	require.NoError(f.t, err)
	require.NoDirExists(f.t, f.dir+"-frozen", "the image goes after a run")
	return m
}

// reader opens the target with a credential that reads, as the operator's restore does, and lifts the box policy.
func (f *fixture) reader() blob.Bucket {
	f.t.Helper()
	f.stub.Set(func(s *s3stub.Stub) { s.Box = false })
	b, err := gocloud.OpenWith(context.Background(), f.stub.URL(), gocloud.OpenOptions{CredentialsFile: s3stub.CredentialsFile(f.t, "AKIDREAD")})
	require.NoError(f.t, err)
	f.t.Cleanup(func() { _ = b.Close() })
	return b
}

// restored restores generation n into a new memory store and returns its objects' bytes by key.
func (f *fixture) restored(n uint64, open backup.Opener) map[string]string {
	f.t.Helper()
	ctx := context.Background()
	dst, err := gocloud.Open(ctx, "mem://")
	require.NoError(f.t, err)
	f.t.Cleanup(func() { _ = dst.Close() })
	if open == nil {
		open = envelope.Opener(nil)
	}
	_, err = blobmirror.Restore(ctx, f.reader(), n, dst, open)
	require.NoError(f.t, err)
	return contents(f.t, dst)
}

func contents(t *testing.T, b blob.Bucket) map[string]string {
	t.Helper()
	ctx := context.Background()
	items, err := b.List(ctx, "")
	require.NoError(t, err)
	out := map[string]string{}
	for _, a := range items {
		data, err := b.Get(ctx, a.Key)
		require.NoError(t, err)
		out[a.Key] = string(data)
	}
	return out
}

// requireBoxOnly checks the mirror sent the target only puts and listings.
func (f *fixture) requireBoxOnly() {
	f.t.Helper()
	requests, _ := f.stub.Seen()
	for _, r := range requests {
		require.True(f.t, strings.HasPrefix(r, "PUT ") || r == "GET ", "the mirror sent %q", r)
	}
}

// sealer is ADR-0204's envelope to two fresh recipients, with an identity that opens it.
func sealer(t *testing.T) (*envelope.Sealer, []age.Identity) {
	t.Helper()
	var rs bytes.Buffer
	var ids []age.Identity
	for range 2 {
		id, err := age.GenerateX25519Identity()
		require.NoError(t, err)
		rs.WriteString(id.Recipient().String() + "\n")
		ids = append(ids, id)
	}
	path := filepath.Join(t.TempDir(), "recipients.txt")
	require.NoError(t, os.WriteFile(path, rs.Bytes(), 0o600))
	s, err := envelope.New(envelope.Config{Recipients: []string{path}, NoSecrets: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	return s, ids[:1]
}
