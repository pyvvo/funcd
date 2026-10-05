// Package gocloud is the gocloud.dev/blob driver for the blob.Bucket port
// (ADR-0007): memory / file / S3 by URL, all pure-Go (no cgo). It lives in its
// own package so go-cloud stays out of the driver-dep-free port (internal/blob).
package gocloud

import (
	"cmp"
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	neturl "net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"

	gcblob "gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	// Register the URL schemes the port supports.
	"gocloud.dev/blob/fileblob"  // file://
	_ "gocloud.dev/blob/memblob" // mem://
	"gocloud.dev/blob/s3blob"    // s3://
)

const (
	defaultExpiry = 15 * time.Minute
	// fileAttrsSuffix is the sidecar suffix fileblob reserves for its own attribute files.
	fileAttrsSuffix = ".attrs"
	// fileEscapePrefix opens fileblob's "__0x<hex>__" rune escape, which it decodes on List.
	fileEscapePrefix = "__0x"
)

// errNoV2Input reports an s3blob listing without a ListObjectsV2 request (Options.UseLegacyList), which has no
// StartAfter; s3 ListAfter then falls back to List (ADR-0184 Decision 4).
var errNoV2Input = errors.New("s3 listing has no ListObjectsV2 input")

// Open adapts a gocloud bucket to blob.Bucket.
//
//	Open(ctx, "mem://")                       → in-memory (memblob; the cgo-free in-mem driver)
//	Open(ctx, "file:///var/lib/funcd/blobs")  → local files (fileblob)
//	Open(ctx, "s3://bucket?region=us-east-1") → S3 (s3blob)
func Open(ctx context.Context, url string) (blob.Bucket, error) {
	b, err := gcblob.OpenBucket(ctx, url)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, "gocloud.Open", "open bucket %q", url)
	}
	k := &bucket{
		b:    b,
		file: strings.HasPrefix(url, fileblob.Scheme+"://"),
		s3:   strings.HasPrefix(url, s3blob.Scheme+"://"),
	}
	if k.file {
		if k.dir, err = fileDir(url); err != nil {
			_ = b.Close()
			return nil, fault.Wrapf(err, fault.Internal, "gocloud.Open", "resolve the directory of %q", url)
		}
	}
	return k, nil
}

// fileDir is the absolute directory fileblob's URL opener serves for url: its path, relative when the host is ".".
func fileDir(url string) (string, error) {
	u, err := neturl.Parse(url)
	if err != nil {
		return "", err
	}
	p := u.Path
	if u.Host == "." {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.Abs(filepath.FromSlash(p))
}

// FileURL is the file:// bucket URL for the absolute directory dir, path-escaped so URL syntax in a
// directory name ('#', '?', '%') stays part of the path (issue #331).
func FileURL(dir string) string {
	return (&neturl.URL{Scheme: fileblob.Scheme, Path: filepath.ToSlash(dir)}).String()
}

type bucket struct {
	b    *gcblob.Bucket
	file bool
	// dir is the file:// bucket's root, which the funcd-owned file walk reads (ADR-0184 Decision 5).
	dir string
	// s3 leaves MD5 nil: s3blob decodes an SSE-KMS/SSE-C ETag that is not the content's MD5 (ADR-0159).
	s3 bool
}

// checkKey rejects a key the file backend cannot keep as its own object: an empty key
// names the bucket root, not an object; fileblob reserves the ".attrs" suffix and its
// "__0x<hex>__" escape (a raw one shares the path of the key it encodes and lists
// decoded), and its filepath.Join cleans a "." segment, a trailing ".." and a leading
// "/", which would land the key on another key's file.
func (k *bucket) checkKey(op, key string) error {
	if !k.file {
		return nil
	}
	if key == "" {
		return fault.Invalidf(op, "an empty key names the bucket root, not an object, on the file backend")
	}
	if strings.HasSuffix(key, fileAttrsSuffix) {
		return fault.Invalidf(op, "%q: the %q suffix is reserved by the file backend", key, fileAttrsSuffix)
	}
	if strings.Contains(key, fileEscapePrefix) {
		return fault.Invalidf(op, "%q: the %q escape is reserved by the file backend", key, fileEscapePrefix)
	}
	segs := strings.Split(key, "/")
	for i, s := range segs {
		if s == "." || (s == ".." && i == len(segs)-1) || (s == "" && i == 0 && len(segs) > 1) {
			return fault.Invalidf(op, "%q would alias another key on the file backend", key)
		}
	}
	return nil
}

func (k *bucket) Get(ctx context.Context, key string) ([]byte, error) {
	if err := k.checkKey("blob.Get", key); err != nil {
		return nil, err
	}
	data, err := k.b.ReadAll(ctx, key)
	if err != nil {
		return nil, mapErr("blob.Get", key, err)
	}
	return data, nil
}

// Put stays on WriteAll: memblob's Upload never feeds the MD5 Attributes reports (ADR-0159). gocloud
// returns a ParseMediaType error unwrapped, so the content type is checked here first.
func (k *bucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error {
	const op = "blob.Put"
	if err := k.checkKey(op, key); err != nil {
		return err
	}
	if opts.ContentType != "" {
		if _, _, err := mime.ParseMediaType(opts.ContentType); err != nil {
			return fault.Wrapf(err, fault.Invalid, op, "content type %q of %q", opts.ContentType, key)
		}
	}
	wo := &gcblob.WriterOptions{ContentType: opts.ContentType, Metadata: opts.Metadata}
	if err := k.b.WriteAll(ctx, key, data, wo); err != nil {
		return mapErr(op, key, err)
	}
	return nil
}

func (k *bucket) Attributes(ctx context.Context, key string) (blob.Attributes, error) {
	const op = "blob.Attributes"
	if err := k.checkKey(op, key); err != nil {
		return blob.Attributes{}, err
	}
	a, err := k.b.Attributes(ctx, key)
	if err != nil {
		return blob.Attributes{}, mapErr(op, key, err)
	}
	out := blob.Attributes{
		Key:         key,
		Size:        a.Size,
		ModTime:     a.ModTime,
		ContentType: a.ContentType,
		Metadata:    a.Metadata,
	}
	if !k.s3 {
		out.MD5 = a.MD5
	}
	return out, nil
}

func (k *bucket) Delete(ctx context.Context, key string) error {
	if err := k.checkKey("blob.Delete", key); err != nil {
		return err
	}
	if err := k.b.Delete(ctx, key); err != nil {
		return mapErr("blob.Delete", key, err)
	}
	return nil
}

func (k *bucket) Exists(ctx context.Context, key string) (bool, error) {
	if err := k.checkKey("blob.Exists", key); err != nil {
		return false, err
	}
	ok, err := k.b.Exists(ctx, key)
	if err != nil {
		return false, mapErr("blob.Exists", key, err)
	}
	return ok, nil
}

func (k *bucket) List(ctx context.Context, prefix string) ([]blob.Attributes, error) {
	if k.file {
		items, _, err := k.fileWalk(ctx, prefix, "", -1)
		return items, err
	}
	iter := k.b.List(&gcblob.ListOptions{Prefix: prefix})
	var out []blob.Attributes
	for {
		obj, err := iter.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, mapErr("blob.List", prefix, err)
		}
		if obj.IsDir {
			// defensive: a flat listing (no delimiter) never sets IsDir, but a future
			// delimiter mode would — skip pseudo-directory entries.
			continue
		}
		out = append(out, k.listed(obj))
	}
	sortByKey(out)
	return out, nil
}

// listed is a mem:// or s3:// listing entry; s3 leaves MD5 nil (ADR-0159).
func (k *bucket) listed(obj *gcblob.ListObject) blob.Attributes {
	a := blob.Attributes{Key: obj.Key, Size: obj.Size, ModTime: obj.ModTime, MD5: obj.MD5}
	if k.s3 {
		a.MD5 = nil
	}
	return a
}

// ListAfter is the ranged, limited listing (ADR-0184 Decisions 3-5).
func (k *bucket) ListAfter(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error) {
	if limit < 1 {
		return nil, false, fault.Invalidf("blob.ListAfter", "limit %d is below 1", limit)
	}
	switch {
	case k.file:
		return k.fileWalk(ctx, prefix, after, limit)
	case k.s3:
		return k.s3ListAfter(ctx, prefix, after, limit)
	default:
		return k.memListAfter(ctx, prefix, after, limit)
	}
}

// memListAfter seeks with after as memblob's page token, which skips keys <= it. ListPage reads the token
// "first page" (gocloud's FirstPageToken) as the first page, and would read a next token equal to it the same
// way, so that after pages from the start with the iterator, whose driver tokens it never sees, instead.
func (k *bucket) memListAfter(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error) {
	if after == string(gcblob.FirstPageToken) {
		iter := k.b.List(&gcblob.ListOptions{Prefix: prefix})
		var out []blob.Attributes
		for {
			obj, err := iter.Next(ctx)
			if errors.Is(err, io.EOF) {
				return out, false, nil
			}
			if err != nil {
				return nil, false, mapErr("blob.ListAfter", prefix, err)
			}
			if obj.Key <= after {
				continue
			}
			if len(out) == limit {
				return out, true, nil
			}
			out = append(out, k.listed(obj))
		}
	}
	token := gcblob.FirstPageToken
	if after != "" {
		token = []byte(after)
	}
	objs, next, err := k.b.ListPage(ctx, token, limit, &gcblob.ListOptions{Prefix: prefix})
	if err != nil {
		return nil, false, mapErr("blob.ListAfter", prefix, err)
	}
	return k.listedAll(objs), next != nil, nil
}

// s3ListAfter asks for exactly limit keys from StartAfter; S3's truncation sets more.
func (k *bucket) s3ListAfter(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error) {
	opts := &gcblob.ListOptions{
		Prefix: prefix,
		BeforeList: func(as func(any) bool) error { //nolint:forbidigo // gocloud's ListOptions.BeforeList fixes this signature
			var in *awss3.ListObjectsV2Input
			if !as(&in) {
				return errNoV2Input
			}
			// ListPage re-calls the driver with a ContinuationToken to fill a short page; S3 resumes from it.
			if in.ContinuationToken == nil && after != "" {
				start := s3EscapeKey(after)
				in.StartAfter = &start
			}
			return nil
		},
	}
	objs, next, err := k.b.ListPage(ctx, gcblob.FirstPageToken, limit, opts)
	if errors.Is(err, errNoV2Input) {
		all, lerr := k.List(ctx, prefix)
		if lerr != nil {
			return nil, false, lerr
		}
		all = all[sort.Search(len(all), func(i int) bool { return all[i].Key > after }):]
		if len(all) > limit {
			return all[:limit], true, nil
		}
		return all, false, nil
	}
	if err != nil {
		return nil, false, mapErr("blob.ListAfter", prefix, err)
	}
	return k.listedAll(objs), next != nil, nil
}

func (k *bucket) listedAll(objs []*gcblob.ListObject) []blob.Attributes {
	out := make([]blob.Attributes, 0, len(objs))
	for _, obj := range objs {
		out = append(out, k.listed(obj))
	}
	return out
}

// s3EscapeKey is s3blob's unexported escapeKey (s3blob.go:718-731; gocloud.dev/internal/escape is not
// importable): a rune below 0x20 and a "/" after ".." become "__0x<hex>__", so StartAfter seeks in the order
// S3 lists the stored keys.
func s3EscapeKey(key string) string {
	if !strings.ContainsFunc(key, func(c rune) bool { return c < ' ' }) && !strings.Contains(key, "../") {
		return key
	}
	r := []rune(key)
	var b strings.Builder
	for i, c := range r {
		if c < ' ' || (i > 1 && c == '/' && r[i-1] == '.' && r[i-2] == '.') {
			b.WriteString("__0x" + strconv.FormatInt(int64(c), 16) + "__")
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

// fileUnescapeKey is fileblob's unexported unescapeKey (fileblob.go:352) on a "/" separator: it decodes each
// "__0x<hex>__" rune escape.
func fileUnescapeKey(name string) string {
	if !strings.Contains(name, fileEscapePrefix) {
		return name
	}
	var b strings.Builder
	for i := 0; i < len(name); {
		if r, n, ok := hexEscapeAt(name[i:]); ok {
			b.WriteRune(r)
			i += n
			continue
		}
		b.WriteByte(name[i])
		i++
	}
	return b.String()
}

// hexEscapeAt decodes a "__0x<hex>__" escape at the start of s into its rune and length.
func hexEscapeAt(s string) (rune, int, bool) {
	if !strings.HasPrefix(s, fileEscapePrefix) {
		return 0, 0, false
	}
	rest := s[len(fileEscapePrefix):]
	end := strings.IndexByte(rest, '_')
	if end < 0 || !strings.HasPrefix(rest[end:], "__") {
		return 0, 0, false
	}
	v, err := strconv.ParseInt(rest[:end], 16, 32)
	if err != nil {
		return 0, 0, false
	}
	return rune(v), len(fileEscapePrefix) + end + 2, true
}

// fileEntry is an object the file walk found; only returned ones have their attributes read.
type fileEntry struct {
	key string
	de  fs.DirEntry
}

// fileWalk lists file:// keys under prefix after `after` in key order; limit < 0 means no limit (List).
// It reads directories itself instead of paging fileblob's List, which re-walks per page and loses keys
// (ADR-0184 Decision 5).
func (k *bucket) fileWalk(ctx context.Context, prefix, after string, limit int) ([]blob.Attributes, bool, error) {
	start := fileWalkPrefix(prefix)
	start = start[:strings.LastIndex(start, "/")+1]
	w := fileWalker{prefix: prefix, after: after, max: limit + 1}
	if err := w.walk(ctx, filepath.Join(k.dir, filepath.FromSlash(start)), start); err != nil {
		return nil, false, mapErr("blob.List", prefix, err)
	}
	found, more := w.found, limit >= 0 && len(w.found) > limit
	if more {
		found = found[:limit]
	}
	out := make([]blob.Attributes, 0, len(found))
	for _, e := range found {
		info, err := e.de.Info()
		if err != nil {
			continue
		}
		a := blob.Attributes{Key: e.key, Size: info.Size(), ModTime: info.ModTime()}
		if attrs, err := k.b.Attributes(ctx, e.key); err == nil {
			a.MD5 = attrs.MD5
		}
		out = append(out, a)
	}
	return out, more, nil
}

// fileWalker collects keys in key order; max <= 0 collects every key.
type fileWalker struct {
	prefix, after string
	max           int
	found         []fileEntry
}

// fileChild is a directory entry with its decoded key; a directory's sort key ends in "/".
type fileChild struct {
	key, sortKey string
	de           fs.DirEntry
}

// walk visits dir, whose keys start with dirKey. Siblings sort by decoded key, a directory's with "/"
// appended and after an equal file key, so the descent emits keys in order.
func (w *fileWalker) walk(ctx context.Context, dir, dirKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil // unreadable: skipped, as fileblob does
	}
	kids := make([]fileChild, 0, len(des))
	for _, de := range des {
		if !de.IsDir() && strings.HasSuffix(de.Name(), fileAttrsSuffix) {
			continue
		}
		key := dirKey + fileUnescapeKey(de.Name())
		sortKey := key
		if de.IsDir() {
			sortKey += "/"
		}
		kids = append(kids, fileChild{key: key, sortKey: sortKey, de: de})
	}
	slices.SortFunc(kids, func(a, b fileChild) int {
		if c := strings.Compare(a.sortKey, b.sortKey); c != 0 {
			return c
		}
		return cmp.Compare(boolInt(a.de.IsDir()), boolInt(b.de.IsDir()))
	})
	for _, c := range kids {
		if w.max > 0 && len(w.found) >= w.max {
			return nil
		}
		if !c.de.IsDir() {
			if strings.HasPrefix(c.key, w.prefix) && c.key > w.after {
				w.found = append(w.found, fileEntry{key: c.key, de: c.de})
			}
			continue
		}
		d := c.sortKey
		if !strings.HasPrefix(d, w.prefix) && !strings.HasPrefix(w.prefix, d) {
			continue
		}
		if d < w.after && !strings.HasPrefix(w.after, d) {
			continue
		}
		if err := w.walk(ctx, filepath.Join(dir, c.de.Name()), d); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fileWalkPrefix cuts prefix before the first rune whose stored path differs from the key: fileblob
// stores a key under its escaped path, hex-escaping a control rune, a "/" after "/" or "..",
// and a key's trailing "/" ("a//b/c" lives at "a/__0x2f__b/c"), and filepath.Join cleans "./"
// and "../". The file walk starts at its last "/" and filters back to prefix (issue #459).
func fileWalkPrefix(prefix string) string {
	for i, r := range prefix {
		if r < ' ' || r == '/' && (i == 0 || i == len(prefix)-1 || prefix[i-1] == '/' || prefix[i-1] == '.') {
			return prefix[:i]
		}
	}
	return prefix
}

// sortByKey orders attributes by Key. List guarantees sorted output as a port contract,
// independent of the backend's listing order (gocloud's mem/file backends happen to list
// lexically, but this keeps the contract true for any backend or a future delimiter mode).
func sortByKey(items []blob.Attributes) {
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
}

func (k *bucket) SignedURL(ctx context.Context, key string, opts blob.SignOptions) (string, error) {
	switch opts.Method {
	case "", blob.SignGet, blob.SignPut, blob.SignDelete:
	default:
		return "", fault.Invalidf("blob.SignedURL", "unsupported sign method %q", opts.Method)
	}
	method := string(opts.Method)
	if method == "" {
		method = string(blob.SignGet)
	}
	expiry := opts.Expiry
	if expiry == 0 {
		expiry = defaultExpiry
	}
	u, err := k.b.SignedURL(ctx, key, &gcblob.SignedURLOptions{Method: method, Expiry: expiry})
	if err != nil {
		return "", mapErr("blob.SignedURL", key, err)
	}
	return u, nil
}

// GetRange implements the optional blob.RangeReader capability (ADR-0080) via
// gocloud's NewRangeReader: it reads only bytes [offset, offset+length) of the
// object, so DuckDB's ranged Parquet reads do not pull the whole object. A
// negative length means "to end" (the gocloud convention, identical to the port's).
func (k *bucket) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	const op = "blob.GetRange"
	if offset < 0 {
		return nil, fault.Invalidf(op, "negative offset %d for %q", offset, key)
	}
	if err := k.checkKey(op, key); err != nil {
		return nil, err
	}
	r, err := k.b.NewRangeReader(ctx, key, offset, length, nil)
	if err != nil {
		return nil, mapErr(op, key, err)
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, mapErr(op, key, err)
	}
	return data, nil
}

func (k *bucket) Close() error {
	if err := k.b.Close(); err != nil {
		return fault.Wrapf(err, fault.Internal, "gocloud.Close", "close bucket")
	}
	return nil
}

// mapErr translates gocloud error codes to api/fault kinds (ADR-0007 §3), wrapping
// the gocloud cause so errors.Is/As can still reach it (fault.Error.Unwrap).
func mapErr(op, key string, err error) error {
	switch gcerrors.Code(err) {
	case gcerrors.NotFound:
		return fault.Wrapf(err, fault.NotFound, op, "%q not found", key)
	case gcerrors.Unimplemented:
		return fault.Wrapf(err, fault.Unavailable, op, "operation not supported by this backend")
	case gcerrors.InvalidArgument:
		return fault.Wrapf(err, fault.Invalid, op, "invalid argument for %q", key)
	}
	// fileblob reports a key its OS path cannot hold as Unknown: a name past the OS limit, or
	// an object and a "key/" prefix needing one path (a rename onto a directory is EISDIR on
	// Linux, EEXIST on macOS). The key is at fault, not the backend.
	if errors.Is(err, syscall.ENAMETOOLONG) || errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.EISDIR) || errors.Is(err, syscall.EEXIST) {
		return fault.Wrapf(err, fault.Invalid, op, "%q cannot be stored by this backend", key)
	}
	return fault.Wrapf(err, fault.Internal, op, "%s failed for %q", op, key)
}
