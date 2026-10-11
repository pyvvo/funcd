// Package backup is the platform backup format and target (ADR-0203): a generation's layout and manifest on an
// S3-compatible store or a directory, fencing against a second writer, and a retention ladder that needs a
// credential which only puts and lists. ADR-0205 runs it; ADR-0206 reads what it writes.
package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"golang.org/x/sys/unix"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/snapshot"
)

// Class is a generation's class: a rung of the ladder or a pin outside it (Decision 6).
type Class string

const (
	Hourly     Class = "hourly"
	Daily      Class = "daily"
	Weekly     Class = "weekly"
	PreUpgrade Class = "pre-upgrade"
	Verified   Class = "verified"
)

// Format is the platform manifest's format, a sequence apart from the KV manifest.json's (Decision 3).
const Format = 1

// Entry is one generation a listing shows. All but At come from the key; At is the manifest's ModTime, zero while
// the generation is incomplete.
type Entry struct {
	Generation uint64
	Timeline   string
	Class      Class
	Complete   bool
	At         time.Time
}

// Retention is the ladder's lifetimes: hours, days and weeks a class is kept, and the days a gen/verified/ copy is.
// Daily or Weekly 0 leaves the class unused; Verified 0 sets no gen/verified/ rule (the KV, ADR-0209); ClassFor
// ignores Verified.
type Retention struct{ Hourly, Daily, Weekly, Verified int }

// Config opens a Target. KeyPrefix names the config keys in errors ("" ⇒ "backup."); DataDir is the platform data
// directory: a directory target on its device is no independent copy, and a run spools its cut there.
type Config struct {
	Target, CredentialsFile, DataDir, KeyPrefix string
	SingleWriter                                bool
	Retention                                   Retention
	Logger                                      *slog.Logger
}

// Seal wraps dst in ADR-0204's envelope; nil stores the bytes as is.
type Seal func(dst io.Writer) (io.WriteCloser, error)

// Unseal opens a store file sealed by Seal; nil reads the bytes as is.
type Unseal func(src io.Reader) (io.Reader, error)

// Opener returns the Unseal for a manifest's recipients; ADR-0204's envelope.Opener(ids) returns one.
type Opener func(recipients []string) (Unseal, error)

// WriteOptions configures a run: the envelope and its keys (ADR-0204), the generation a restore loaded (ADR-0206),
// and a pin ("" ⇒ the ladder, PreUpgrade ⇒ ADR-0207's pin).
type WriteOptions struct {
	Seal   Seal
	Keys   Keys
	Parent *GenRef
	Pin    Class
}

// Target is a backup target.
type Target interface {
	// Ready takes a directory's lock and probes the target (Decision 4); Write calls it first.
	Ready(ctx context.Context) error
	// Conditional reports, after Ready's nil, whether puts carry IfNotExist: false only when the target refused it
	// and singleWriter is set.
	Conditional() bool
	List(ctx context.Context) ([]Entry, error)
	// Write cuts the stores and writes one generation; a second writer is fault.Conflict.
	Write(ctx context.Context, events, meta, runs snapshot.Source, opts WriteOptions) (Manifest, error)
	// Close releases the lock and the bucket.
	Close() error
}

// Open checks cfg and opens its target; a bad scheme or key is fault.Invalid naming <KeyPrefix><key>.
func Open(ctx context.Context, cfg Config) (Target, error) {
	const op = "backup.Open"
	prefix := keyPrefix(cfg)
	u, dir, err := checkConfig(op, prefix, cfg)
	if err != nil {
		return nil, err
	}
	b, err := gocloud.OpenWith(ctx, u.String(), gocloud.OpenOptions{CredentialsFile: cfg.CredentialsFile})
	if err != nil {
		keys := prefix + "target"
		if cfg.CredentialsFile != "" {
			keys += ", " + prefix + "credentialsFile"
		}
		return nil, fault.Wrapf(err, fault.Invalid, op, "%s: open the target", keys)
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	log.Info("backup target: set one lifecycle expiry per prefix (days)", "key", prefix+"target",
		"rules", LifecycleRules(cfg.Retention))
	return &target{cfg: cfg, prefix: prefix, b: b, dir: dir, log: log, clock: clock.System(), conditional: true}, nil
}

// TargetURL is the bucket URL Open opens for cfg, checked as Open checks it: a directory's carries dir_file_mode 0700.
// A sibling backup on the same target (ADR-0208, ADR-0209) opens its own bucket on it.
func TargetURL(cfg Config) (string, error) {
	u, _, err := checkConfig("backup.TargetURL", keyPrefix(cfg), cfg)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// keyPrefix names cfg's keys in errors, "backup." by default.
func keyPrefix(cfg Config) string {
	if cfg.KeyPrefix == "" {
		return "backup."
	}
	return cfg.KeyPrefix
}

// checkConfig refuses a scheme other than s3:// and file:///<absolute dir>, a credentials file beside a directory,
// and retention out of range (Decision 5). A directory's URL gains dir_file_mode 0700, which fileblob reads decimal.
func checkConfig(op, prefix string, cfg Config) (*url.URL, string, error) {
	if cfg.Target == "" {
		return nil, "", fault.Invalidf(op, "%starget is empty", prefix)
	}
	u, err := url.Parse(cfg.Target)
	if err != nil {
		return nil, "", fault.Wrapf(err, fault.Invalid, op, "%starget", prefix)
	}
	var dir string
	switch u.Scheme {
	case "s3":
	case "file":
		q := u.Query()
		switch {
		case cfg.CredentialsFile != "":
			return nil, "", fault.Invalidf(op, "%scredentialsFile applies to an s3:// target only", prefix)
		case q.Has("prefix"):
			return nil, "", fault.Invalidf(op, "%starget: a file:// target takes no prefix", prefix)
		case u.Host != "" || !filepath.IsAbs(filepath.FromSlash(u.Path)):
			return nil, "", fault.Invalidf(op, "%starget %q: want file:///<absolute dir>", prefix, cfg.Target)
		}
		dir = filepath.Clean(filepath.FromSlash(u.Path))
		q.Set("dir_file_mode", strconv.Itoa(0o700))
		u.RawQuery = q.Encode()
	default:
		return nil, "", fault.Invalidf(op, "%starget %q: the scheme is not s3 or file", prefix, cfg.Target)
	}
	r := cfg.Retention
	switch {
	case r.Hourly < 1:
		return nil, "", fault.Invalidf(op, "%sretention.hourly is %d: want at least 1", prefix, r.Hourly)
	case r.Daily < 0:
		return nil, "", fault.Invalidf(op, "%sretention.daily is %d: want 0 or more", prefix, r.Daily)
	case r.Weekly < 0:
		return nil, "", fault.Invalidf(op, "%sretention.weekly is %d: want 0 or more", prefix, r.Weekly)
	case r.Verified < 0:
		return nil, "", fault.Invalidf(op, "%sretention.verified is %d: want 0 or more", prefix, r.Verified)
	}
	return u, dir, nil
}

type target struct {
	cfg    Config
	prefix string
	b      blob.Bucket
	// dir is a file:// target's directory, "" on s3://.
	dir   string
	log   *slog.Logger
	clock clock.Clock
	// run keeps one run at a time.
	run sync.Mutex

	mu          sync.Mutex
	lock        *os.File
	ready       bool
	conditional bool
}

func (t *target) Ready(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ready {
		return nil
	}
	if t.dir != "" {
		if err := t.takeLock(); err != nil {
			return err
		}
	}
	conditional, err := t.probe(ctx)
	if err != nil {
		return err
	}
	t.ready, t.conditional = true, conditional
	return nil
}

func (t *target) Conditional() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.conditional
}

func (t *target) List(ctx context.Context) ([]Entry, error) { return List(ctx, t.b) }

func (t *target) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var errs []error
	if t.lock != nil {
		errs = append(errs, t.lock.Close())
		t.lock = nil
	}
	errs = append(errs, t.b.Close())
	return errors.Join(errs...)
}

// takeLock holds <dir>/lock for the process life, as procreg.Open does: another holder is fault.Conflict, even with
// singleWriter. A directory on the data directory's device is no independent copy (Q8), so it warns.
func (t *target) takeLock() error {
	const op = "backup.Ready"
	if t.lock != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(t.dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // the operator's target directory
	if err != nil {
		return fault.Wrapf(err, fault.Unavailable, op, "%starget: open the lock of %s", t.prefix, t.dir)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { //nolint:gosec // a file descriptor fits an int
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return fault.Conflictf(op, "%starget: another process holds the lock of %s", t.prefix, t.dir)
		}
		return fault.Wrapf(err, fault.Unavailable, op, "%starget: lock %s", t.prefix, t.dir)
	}
	t.lock = f
	var target, data unix.Stat_t
	if t.cfg.DataDir != "" && unix.Stat(t.dir, &target) == nil && unix.Stat(t.cfg.DataDir, &data) == nil && target.Dev == data.Dev {
		t.log.Warn("backup target is on the data directory's device: not an independent copy", "key", t.prefix+"target", "dir", t.dir)
	}
	return nil
}

// probe puts one random probe/<32 hex> key from two goroutines with IfNotExist: one nil and one fault.Conflict is
// a fenced target. Both nil (the condition ignored) or a refused condition is fault.Invalid naming singleWriter,
// a warning with it; Conditional turns false only when refused. Anything else is not ready, with its cause.
func (t *target) probe(ctx context.Context) (bool, error) {
	const op = "backup.Ready"
	key := probePrefix + randomHex(16)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { errs[i] = t.b.Put(ctx, key, nil, blob.PutOptions{IfNotExist: true}) })
	}
	wg.Wait()
	won := func(a, b error) bool { return a == nil && fault.KindOf(b) == fault.Conflict }
	var does string
	switch {
	case won(errs[0], errs[1]) || won(errs[1], errs[0]):
		return true, nil
	case conditionRefused(errs[0]) || conditionRefused(errs[1]):
		does = "refuses"
	case errs[0] == nil && errs[1] == nil:
		does = "ignores"
	default:
		err := errors.Join(errs...)
		return false, fault.Wrapf(err, fault.KindOf(err), op, "%starget: probe %s", t.prefix, key)
	}
	if !t.cfg.SingleWriter {
		return false, fault.Invalidf(op, "%ssingleWriter: the target %s a create-if-absent put (If-None-Match: *), so "+
			"nothing fences a second writer; set it to true only when one funcd writes here", t.prefix, does)
	}
	t.log.Warn("backup target "+does+" a create-if-absent put: writing on the operator's promise of one writer",
		"key", t.prefix+"singleWriter")
	return does != "refuses", nil
}

// conditionRefused reports a store refusing If-None-Match: S3's NotImplemented, or a 501.
func conditionRefused(err error) bool {
	var ae smithy.APIError
	if errors.As(err, &ae) && ae.ErrorCode() == "NotImplemented" {
		return true
	}
	var re *smithyhttp.ResponseError
	return errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotImplemented
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
