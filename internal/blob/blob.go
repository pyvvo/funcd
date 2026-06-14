// Package blob is the storage-layer port (ADR-0007): opaque byte objects behind
// the blob.Bucket port. It is the bytes substrate, sibling to the store (records)
// — deliberately named "blob", not "storage", to stay distinct from "store".
//
// This package imports no driver library; the gocloud.dev/blob adapter (memory /
// file / S3, all pure-Go) lives in internal/blob/gocloud.
package blob

import (
	"context"
	"time"
)

// Bucket is the storage-layer port: opaque byte objects keyed by a path string.
// Errors are api/fault kinds; every method is ctx-first.
type Bucket interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix string) ([]Attributes, error)
	SignedURL(ctx context.Context, key string, opts SignOptions) (string, error)
	Close() error
}

// Attributes is the listing metadata for one object.
type Attributes struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// SignMethod is the typed HTTP method a SignedURL grants (ADR-0002: typed over magic strings).
type SignMethod string

const (
	SignGet    SignMethod = "GET"
	SignPut    SignMethod = "PUT"
	SignDelete SignMethod = "DELETE"
)

// SignOptions configures a SignedURL request. A zero Method means SignGet; a zero
// Expiry means the driver's default (15m).
type SignOptions struct {
	Method SignMethod
	Expiry time.Duration
}
