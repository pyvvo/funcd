// Package kvstore is the key/value substrate port (ADR-0019): get/put/delete/list over
// opaque string keys and []byte values. The service facade (internal/services/kv)
// supplies the tenant-prefixed key (<namespace>/<binding>/<key>); the driver is
// namespace-agnostic. V1 ships an in-memory driver; JetStream / database-layer drivers
// are deferred behind this port.
package kvstore

import "context"

// KV is the key/value port. Errors are api/fault; every method is ctx-first.
type KV interface {
	// Get returns the value for key and whether it exists. A missing key is
	// (nil, false, nil) — not an error.
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	// Put stores value under key (overwriting any existing value).
	Put(ctx context.Context, key string, value []byte) error
	// Delete removes key; deleting a missing key is a no-op (no error).
	Delete(ctx context.Context, key string) error
	// List returns the keys that start with prefix.
	List(ctx context.Context, prefix string) (keys []string, err error)
}
