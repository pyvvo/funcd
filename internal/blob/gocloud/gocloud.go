// Package gocloud is the gocloud.dev/blob driver for the blob.Bucket port
// (ADR-0007): memory / file / S3 by URL, all pure-Go (no cgo). It lives in its
// own package so go-cloud stays out of the driver-dep-free port (internal/blob).
package gocloud

import (
	"context"
	"errors"
	"io"
	neturl "net/url"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"

	gcblob "gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	// Register the URL schemes the port supports.
	"gocloud.dev/blob/fileblob"  // file://
	_ "gocloud.dev/blob/memblob" // mem://
	_ "gocloud.dev/blob/s3blob"  // s3://
)

const (
	defaultExpiry = 15 * time.Minute
	// fileAttrsSuffix is the sidecar suffix fileblob reserves for its own attribute files.
	fileAttrsSuffix = ".attrs"
	// fileEscapePrefix opens fileblob's "__0x<hex>__" rune escape, which it decodes on List.
	fileEscapePrefix = "__0x"
)

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
	return &bucket{b: b, file: strings.HasPrefix(url, fileblob.Scheme+"://")}, nil
}

// FileURL is the file:// bucket URL for the absolute directory dir, path-escaped so URL syntax in a
// directory name ('#', '?', '%') stays part of the path (issue #331).
func FileURL(dir string) string {
	return (&neturl.URL{Scheme: fileblob.Scheme, Path: filepath.ToSlash(dir)}).String()
}

type bucket struct {
	b    *gcblob.Bucket
	file bool
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

func (k *bucket) Put(ctx context.Context, key string, data []byte) error {
	if err := k.checkKey("blob.Put", key); err != nil {
		return err
	}
	if err := k.b.WriteAll(ctx, key, data, nil); err != nil {
		return mapErr("blob.Put", key, err)
	}
	return nil
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
	walk := prefix
	if k.file {
		walk = fileWalkPrefix(prefix)
	}
	iter := k.b.List(&gcblob.ListOptions{Prefix: walk})
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
		if !strings.HasPrefix(obj.Key, prefix) {
			continue
		}
		out = append(out, blob.Attributes{Key: obj.Key, Size: obj.Size, ModTime: obj.ModTime})
	}
	sortByKey(out)
	return out, nil
}

// fileWalkPrefix cuts prefix before the first rune fileblob would not walk to: it starts its
// List at filepath.Join(dir, prefix[:lastSlash]), which cleans "//", "./" and "../", while it
// stores a key under its escaped path, hex-escaping a control rune, a "/" after "/" or "..",
// and a key's trailing "/" ("a//b/c" lives at "a/__0x2f__b/c"). List filters the wider walk
// back to prefix (issue #459).
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
