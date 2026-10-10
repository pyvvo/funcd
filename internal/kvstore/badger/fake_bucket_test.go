package badger

import (
	"cmp"
	"context"
	"crypto/sha256"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

func TestMain(m *testing.M) { s3stub.Main(m) }

// fakeBucket is an in-memory blob.Bucket stamped by a clock: it honors IfNotExist (fault.Conflict) unless
// ignoreCond, records every put, counts reads and deletes, and can fail a put or run a hook after one.
type fakeBucket struct {
	mu         sync.Mutex
	objs       map[string]fakeObj
	clock      clock.Clock
	puts       []string
	reads      int
	deletes    int
	ignoreCond bool
	failPut    func(key string) error
	onPut      func(key string)
}

type fakeObj struct {
	data []byte
	mod  time.Time
}

func newFakeBucket() *fakeBucket {
	return &fakeBucket{objs: map[string]fakeObj{}, clock: clock.System()}
}

func (f *fakeBucket) Put(_ context.Context, key string, data []byte, opts blob.PutOptions) error {
	f.mu.Lock()
	fail, hook := f.failPut, f.onPut
	f.mu.Unlock()
	if fail != nil {
		if err := fail(key); err != nil {
			return err
		}
	}
	f.mu.Lock()
	if _, ok := f.objs[key]; ok && opts.IfNotExist && !f.ignoreCond {
		f.mu.Unlock()
		return fault.Conflictf("fakeBucket.Put", "%q exists", key)
	}
	f.objs[key] = fakeObj{data: slices.Clone(data), mod: f.clock.Now()}
	f.puts = append(f.puts, key)
	f.mu.Unlock()
	if hook != nil {
		hook(key)
	}
	return nil
}

func (f *fakeBucket) Get(_ context.Context, key string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	o, ok := f.objs[key]
	if !ok {
		return nil, fault.NotFoundf("fakeBucket.Get", "%q not found", key)
	}
	return slices.Clone(o.data), nil
}

func (f *fakeBucket) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	delete(f.objs, key)
	return nil
}

func (f *fakeBucket) Exists(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	_, ok := f.objs[key]
	return ok, nil
}

func (f *fakeBucket) Attributes(_ context.Context, key string) (blob.Attributes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	o, ok := f.objs[key]
	if !ok {
		return blob.Attributes{}, fault.NotFoundf("fakeBucket.Attributes", "%q not found", key)
	}
	return blob.Attributes{Key: key, Size: int64(len(o.data)), ModTime: o.mod}, nil
}

func (f *fakeBucket) List(_ context.Context, prefix string) ([]blob.Attributes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []blob.Attributes
	for k, o := range f.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, blob.Attributes{Key: k, Size: int64(len(o.data)), ModTime: o.mod})
		}
	}
	slices.SortFunc(out, func(a, b blob.Attributes) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}

func (f *fakeBucket) ListAfter(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error) {
	all, err := f.List(ctx, prefix)
	if err != nil {
		return nil, false, err
	}
	all = slices.DeleteFunc(all, func(a blob.Attributes) bool { return a.Key <= after })
	if len(all) > limit {
		return all[:limit], true, nil
	}
	return all, false, nil
}

func (f *fakeBucket) SignedURL(context.Context, string, blob.SignOptions) (string, error) {
	return "", fault.Invalidf("fakeBucket.SignedURL", "unsupported")
}

func (f *fakeBucket) Close() error { return nil }

// keys returns the keys under prefix, sorted.
func (f *fakeBucket) keys(prefix string) []string {
	attrs, _ := f.List(context.Background(), prefix)
	out := make([]string, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, a.Key)
	}
	return out
}

// bytesUnder sums the size of every object whose key starts with prefix.
func (f *fakeBucket) bytesUnder(prefix string) int {
	attrs, _ := f.List(context.Background(), prefix)
	total := 0
	for _, a := range attrs {
		total += int(a.Size)
	}
	return total
}

// requireBoxOnly checks the writer only put and listed: no read, no delete, no key put twice.
func (f *fakeBucket) requireBoxOnly(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Zero(t, f.reads, "the writer read the target")
	require.Zero(t, f.deletes, "the writer deleted on the target")
	seen := map[string]bool{}
	for _, k := range f.puts {
		require.False(t, seen[k], "%s was put twice", k)
		seen[k] = true
	}
}

// chains returns the chain numbers of the complete bases, sorted.
func (f *fakeBucket) chains(t *testing.T) []uint64 {
	t.Helper()
	l, err := listBases(context.Background(), f)
	require.NoError(t, err)
	var out []uint64
	for n := range l.complete {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// unsealed opens an unsealed chain.
func unsealed([]string) (backup.Unseal, error) { return nil, nil }

// newestPoint is the newest restorable point of bucket.
func newestPoint(t *testing.T, bucket blob.Bucket) Point {
	t.Helper()
	ps, err := ListPoints(context.Background(), bucket)
	require.NoError(t, err)
	var best *Point
	for i := range ps {
		if ps[i].Restorable {
			best = &ps[i]
		}
	}
	require.NotNil(t, best, "no restorable point in %v", ps)
	return *best
}

// baseDirOf is the directory of chain n's complete base.
func baseDirOf(t *testing.T, bucket blob.Bucket, n uint64) string {
	t.Helper()
	l, err := listBases(context.Background(), bucket)
	require.NoError(t, err)
	for _, e := range l.entries {
		if e.Generation == n {
			return baseDir(e.Class, n)
		}
	}
	require.FailNow(t, "no complete base", "chain %d", n)
	return ""
}

// readManifest reads the manifest under dir.
func readManifest(t *testing.T, bucket blob.Bucket, dir string) ChainManifest {
	t.Helper()
	data, err := bucket.Get(context.Background(), dir+manifestName)
	require.NoError(t, err)
	var m ChainManifest
	require.NoError(t, yaml.Unmarshal(data, &m))
	return m
}

// loadStored loads the stored file under dir of an unsealed chain into a fresh instance as is: no record pass, no
// cursor.
func loadStored(t *testing.T, bucket blob.Bucket, dir string) *badger.DB {
	t.Helper()
	db := openRawDB(t, t.TempDir())
	parts := readManifest(t, bucket, dir).Store.Parts
	r := &partReader{ctx: context.Background(), bucket: bucket, prefix: dir, parts: parts, h: sha256.New()}
	require.NoError(t, db.Load(r, 16))
	return db
}

// state reads the cursor record of db.
func state(t *testing.T, db *badger.DB) (chainState, bool) {
	t.Helper()
	st, full, err := (&exporter{db: db}).readState(context.Background())
	require.NoError(t, err)
	return st, full
}
