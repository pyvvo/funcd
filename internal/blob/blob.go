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
	Put(ctx context.Context, key string, data []byte, opts PutOptions) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix string) ([]Attributes, error)
	// Attributes reads one object's attributes without its content; fault.NotFound if absent.
	Attributes(ctx context.Context, key string) (Attributes, error)
	SignedURL(ctx context.Context, key string, opts SignOptions) (string, error)
	Close() error
}

// Attributes describes one object. List fills Key, Size, ModTime and MD5; Bucket.Attributes fills all.
type Attributes struct {
	Key     string
	Size    int64
	ModTime time.Time
	// MD5 is the MD5 of the content; empty when the driver has no digest for the object (ADR-0159).
	MD5         []byte
	ContentType string
	Metadata    map[string]string // user metadata, lowercase keys
}

// PutOptions configures a Put. The zero value stores no metadata and lets the driver pick the content type.
type PutOptions struct {
	ContentType string
	Metadata    map[string]string
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
