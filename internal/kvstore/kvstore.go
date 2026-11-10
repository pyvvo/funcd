// Package kvstore is the key/value substrate port (ADR-0019): get/put/delete/list over
// opaque string keys and []byte values. The service facade (internal/services/kv)
// supplies the tenant-prefixed key (<namespace>/<binding>/<key>); the driver is
// namespace-agnostic. V1 ships an in-memory driver; JetStream / database-layer drivers
// are deferred behind this port.
package kvstore

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

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

// Usager is a driver capability beyond the KV port (ADR-0292): the bytes of the keys and values under prefix, which a
// KVStore's quota measures.
type Usager interface {
	Usage(ctx context.Context, prefix string) (int64, error)
}

// Entry is a key's value with its version and its expiry.
type Entry struct {
	Value []byte
	// Version names this write of the key, an opaque hash (Version): every write gives the key another, and a restore
	// another timeline, so a version from before a restore names nothing after it. Callers compare versions for
	// equality only.
	Version string
	// ExpiresAt is the Unix second from which the key no longer exists; 0 is never.
	ExpiresAt uint64
}

// Write is what a conditional write requires and how long its key lives. With neither condition it is a Put that
// reports the new version.
type Write struct {
	// IfAbsent writes only when the key does not exist; an expired key does not.
	IfAbsent bool
	// IfVersion, when not "", writes or deletes only when the key exists at this version.
	IfVersion string
	// TTL, when not 0, makes the key expire once it has lived TTL, rounded up to the second.
	TTL time.Duration
	// ExpiresAt, when not 0, is the expiry itself, for a copy that keeps a key's own; never with TTL.
	ExpiresAt uint64
}

// Result is a conditional write's outcome. Done reports that its condition held and the write was stored. Version is
// then the key's new version ("" after a delete); otherwise it is the key's current version, "" when it does not exist.
type Result struct {
	Done    bool
	Version string
}

// Conditional is a driver capability beyond the KV port: versioned reads, writes that hold only while a condition on
// the key holds, and keys that expire. An expired key is absent to every read and condition at once; a write that
// returned Done is seen by every later read.
type Conditional interface {
	GetEntry(ctx context.Context, key string) (Entry, bool, error)
	PutIf(ctx context.Context, key string, value []byte, w Write) (Result, error)
	DeleteIf(ctx context.Context, key string, version string) (Result, error)
}

// Sweeper is a driver capability beyond the KV port: it takes up to limit keys that expired by now and that skip does
// not name, deletes each through the driver's own write path while its entry is the one that expired, and returns the
// deleted keys and whether more were due. A key renewed or rewritten since stays; a skipped one waits for a later
// sweep.
type Sweeper interface {
	Sweep(ctx context.Context, now time.Time, limit int, skip func(key string) bool) (swept []string, more bool, err error)
}

// NewTimeline mints a store's timeline: 32 random bytes as 64 lowercase hex characters, which key every version's
// hash; a store mints one when it has none, as ADR-0202 Decision 3 mints the metastore's.
func NewTimeline() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails: crypto/rand.Read always fills b
	return hex.EncodeToString(b)
}

// Version is the version of write n of key on timeline: the first 16 bytes, as 32 lowercase hex characters, of
// HMAC-SHA256 over n and key, keyed with the timeline. It shows neither n nor the timeline, and another write, key or
// timeline gives another version (ADR-0320 Decision 2).
func Version(timeline, key string, n uint64) string {
	m := hmac.New(sha256.New, []byte(timeline))
	m.Write(binary.BigEndian.AppendUint64(nil, n))
	m.Write([]byte(key))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// ExpiryOf is when a key w writes at now expires: w.ExpiresAt, else ExpiresAt(now, w.TTL).
func (w Write) ExpiryOf(now time.Time) uint64 {
	if w.ExpiresAt != 0 {
		return w.ExpiresAt
	}
	return ExpiresAt(now, w.TTL)
}

// ExpiresAt is the expiry of a key written at now that lives ttl: the next Unix second at or after now+ttl, so the
// key lives at least ttl and at most a second longer; 0 for no ttl.
func ExpiresAt(now time.Time, ttl time.Duration) uint64 {
	if ttl <= 0 {
		return 0
	}
	at := now.Add(ttl)
	sec := at.Unix()
	if at.Nanosecond() > 0 {
		sec++
	}
	return uint64(sec) //nolint:gosec // a time after the epoch
}

// Expired reports whether a key with expiry expiresAt no longer exists at now, as Badger decides it.
func Expired(expiresAt uint64, now time.Time) bool {
	return expiresAt != 0 && expiresAt <= uint64(now.Unix()) //nolint:gosec // a time after the epoch
}

// CheckWrite refuses a Write a driver cannot store: a negative TTL, a TTL with an expiry, or both conditions at once.
func CheckWrite(op string, w Write) error {
	switch {
	case w.TTL < 0:
		return fault.Invalidf(op, "ttl %s is negative", w.TTL)
	case w.TTL != 0 && w.ExpiresAt != 0:
		return fault.Invalidf(op, "a write takes a ttl or an expiry, not both")
	case w.IfAbsent && w.IfVersion != "":
		return fault.Invalidf(op, "a write is conditional on the key's absence or on its version, not both")
	}
	return nil
}

// CheckStorePrefix refuses a per-store export prefix (ADR-0209 Decision 7) that is empty, lacks the trailing "/" or
// starts with the NUL of a driver's reserved keys.
func CheckStorePrefix(op, prefix string) error {
	if prefix == "" || !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "\x00") {
		return fault.Invalidf(op, "store prefix %q: want <namespace>/<store>/", prefix)
	}
	return nil
}
