// Package gocloud is the gocloud.dev/blob driver for the blob.Bucket port
// (ADR-0007): memory / file / S3 by URL, all pure-Go (no cgo). It lives in its
// own package so go-cloud stays out of the driver-dep-free port (internal/blob).
package gocloud

import (
	"context"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/blob"

	gcblob "gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	// Register the URL schemes the port supports.
	_ "gocloud.dev/blob/fileblob" // file://
	_ "gocloud.dev/blob/memblob"  // mem://
	_ "gocloud.dev/blob/s3blob"   // s3://
)

const defaultExpiry = 15 * time.Minute

// Open adapts a gocloud bucket to blob.Bucket.
//
//	Open(ctx, "mem://")                       → in-memory (memblob; the cgo-free in-mem driver)
//	Open(ctx, "file:///var/lib/funcd/blobs")  → local files (fileblob)
//	Open(ctx, "s3://bucket?region=us-east-1") → S3 (s3blob)
func Open(ctx context.Context, url string) (blob.Bucket, error) {
	b, err := gcblob.OpenBucket(ctx, url)
	if err != nil {
		return nil, fault.Internalf("gocloud.Open", "open bucket %q: %v", url, err)
	}
	return &bucket{b: b}, nil
}

type bucket struct {
	b *gcblob.Bucket
}

func (k *bucket) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := k.b.ReadAll(ctx, key)
	if err != nil {
		return nil, mapErr("blob.Get", key, err)
	}
	return data, nil
}

func (k *bucket) Put(ctx context.Context, key string, data []byte) error {
	if err := k.b.WriteAll(ctx, key, data, nil); err != nil {
		return mapErr("blob.Put", key, err)
	}
	return nil
}

func (k *bucket) Delete(ctx context.Context, key string) error {
	if err := k.b.Delete(ctx, key); err != nil {
		return mapErr("blob.Delete", key, err)
	}
	return nil
}

func (k *bucket) Exists(ctx context.Context, key string) (bool, error) {
	ok, err := k.b.Exists(ctx, key)
	if err != nil {
		return false, mapErr("blob.Exists", key, err)
	}
	return ok, nil
}

func (k *bucket) List(ctx context.Context, prefix string) ([]blob.Attributes, error) {
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
			continue
		}
		out = append(out, blob.Attributes{Key: obj.Key, Size: obj.Size, ModTime: obj.ModTime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (k *bucket) SignedURL(ctx context.Context, key string, opts blob.SignOptions) (string, error) {
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

func (k *bucket) Close() error {
	if err := k.b.Close(); err != nil {
		return fault.Internalf("gocloud.Close", "%v", err)
	}
	return nil
}

// mapErr translates gocloud error codes to api/fault kinds (ADR-0007 §3).
func mapErr(op, key string, err error) error {
	switch gcerrors.Code(err) {
	case gcerrors.NotFound:
		return fault.NotFoundf(op, "%q not found", key)
	case gcerrors.Unimplemented:
		return fault.Unavailablef(op, "operation not supported by this backend: %v", err)
	default:
		return fault.Internalf(op, "%v", err)
	}
}
