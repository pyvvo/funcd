// Package blob is the storage-layer port (ADR-0007): opaque byte objects behind
// the blob.Bucket port. It is the bytes substrate, sibling to the store (records)
// — deliberately named "blob", not "storage", to stay distinct from "store".
//
// This package imports no driver library; the gocloud.dev/blob adapter (memory /
// file / S3, all pure-Go) lives in internal/blob/gocloud.
package blob

import (
	"context"
	"errors"
	"time"
)

// ErrCorrupt reports an object whose bytes do not match the MD5 its attributes record, or whose attributes do not
// decode; a driver wraps it in fault.Internal and serves none of the object (ADR-0278).
var ErrCorrupt = errors.New("the object does not match its attributes")

// Bucket is the storage-layer port: opaque byte objects keyed by a path string.
// Errors are api/fault kinds; every method is ctx-first.
type Bucket interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte, opts PutOptions) error
	Delete(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
	List(ctx context.Context, prefix string) ([]Attributes, error)
	// ListAfter returns at most limit objects under prefix whose key sorts strictly after `after`, sorted by key,
	// with the fields List fills; more is true exactly when a further key under prefix exists. limit < 1 is
	// fault.Invalid (ADR-0184).
	ListAfter(ctx context.Context, prefix, after string, limit int) (items []Attributes, more bool, err error)
	// Attributes reads one object's attributes without its content; fault.NotFound if absent.
	Attributes(ctx context.Context, key string) (Attributes, error)
	SignedURL(ctx context.Context, key string, opts SignOptions) (string, error)
	Close() error
}

// PrefixMover is an optional capability of a Bucket (ADR-0288 Decision 2): MovePrefix moves each object under src to
// dst plus its suffix, both prefixes ending in "/". A file store does it in one rename of the directory, both parents
// synced; another store copies each object server side, then deletes it, so a cut move run again moves what src still
// holds. A src with no object is no error.
type PrefixMover interface {
	MovePrefix(ctx context.Context, src, dst string) error
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
	// IfNotExist creates the object only when its key is absent; a present key is fault.Conflict and the object
	// stays unchanged (ADR-0203).
	IfNotExist bool
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

// Versioned is an OPTIONAL capability, found by type assertion as RangeReader is: a store whose bucket keeps every
// version of an object (ADR-0208). A Bucket without it cannot be protected or restored to a time; the caller answers
// fault.Invalid.
type Versioned interface {
	// Versioning reads the bucket's versioning status and Object Lock configuration (ADR-0208 Decision 2).
	Versioning(ctx context.Context) (Versioning, error)
	// RestoreAt makes every key under the store's prefix hold its version at at (Decision 6): keep is
	// blob.versionRetention, and 0 or an at older than keep is fault.Invalid. No version is removed.
	RestoreAt(ctx context.Context, at time.Time, keep time.Duration) (RestoreReport, error)
}

// Versioning is a bucket's protection: versioning Enabled and Object Lock enabled.
type Versioning struct{ Enabled, ObjectLock bool }

// RestoreReport counts a RestoreAt's keys: copied from an older version, deleted (a new delete marker) and left as
// they were; NoVersion lists the deleted keys that had no version at the time.
type RestoreReport struct {
	Copied, Deleted, Unchanged int
	NoVersion                  []string
}
