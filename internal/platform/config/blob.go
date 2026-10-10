package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The local blob store mirror's times when a key is empty (ADR-0208 Decision 1).
const (
	defaultBlobBackupInterval = time.Hour
	defaultBlobRebaseline     = 720 * time.Hour
	defaultBlobRetention      = 720 * time.Hour
)

// BlobTimes are blob.backup.interval, .rebaseline, .retention and blob.versionRetention, parsed; VersionRetention 0
// is unset (ADR-0208 Decision 1).
type BlobTimes struct{ Interval, Rebaseline, Retention, VersionRetention time.Duration }

// BlobTimes parses the blob times with ADR-0194's grammar within [1ms, MaxDuration]: an empty key is its default, a
// malformed or out-of-bounds one fault.Invalid naming it.
func (c Config) BlobTimes() (BlobTimes, error) {
	var t BlobTimes
	for _, k := range c.blobTimeKeys(&t) {
		if err := k.parse(); err != nil {
			return BlobTimes{}, err
		}
	}
	return t, nil
}

func (c Config) blobTimeKeys(t *BlobTimes) []backupTimeKey {
	b := c.Blob
	return []backupTimeKey{
		{"blob.backup.interval", b.Backup.Interval, defaultBlobBackupInterval, &t.Interval},
		{"blob.backup.rebaseline", b.Backup.Rebaseline, defaultBlobRebaseline, &t.Rebaseline},
		{"blob.backup.retention", b.Backup.Retention, defaultBlobRetention, &t.Retention},
		{"blob.versionRetention", b.VersionRetention, 0, &t.VersionRetention},
	}
}

// CheckBlob applies ADR-0208 Decision 1's rows, errors first, reading only the config: blob.target is s3:// and
// takes no blob.dir, blob.credentialsFile needs it, blob.dir lies below storage.dataDir, the times parse and a
// generation outlives the interval. Under storage.mode memory every blob key is ignored with one warning.
func (c Config) CheckBlob() []Finding {
	var f findings
	b := c.Blob
	if c.Storage.Mode == "memory" {
		if keys := c.blobKeysOffDefault(); len(keys) > 0 {
			f.warn(keys[0], "storage.mode is memory: %s ignored, the blob store is in memory", strings.Join(keys, ", "))
		}
		return f.warns
	}
	if b.Target != "" {
		if u, err := url.Parse(b.Target); err != nil || u.Scheme != "s3" {
			f.refuse("blob.target", "blob.target %q: want s3://<bucket>?region=…&endpoint=…", b.Target)
		}
		if b.Dir != "" {
			f.refuse("blob.dir", "blob.dir is set beside blob.target: a remote store has no local directory")
		}
	}
	if b.CredentialsFile != "" && b.Target == "" {
		f.refuse("blob.credentialsFile", "blob.credentialsFile applies to an s3:// blob.target only")
	}
	if b.Dir != "" && c.Storage.DataDir != "" && atOrBelow(filepath.Clean(c.Storage.DataDir), filepath.Clean(b.Dir)) {
		f.refuse("blob.dir", "blob.dir %s is at or above storage.dataDir %s: want a directory below it or elsewhere",
			b.Dir, c.Storage.DataDir)
	}
	var t BlobTimes
	timesOK := true
	for _, k := range c.blobTimeKeys(&t) {
		if err := k.parse(); err != nil {
			f.refuse(k.key, "%v", err)
			timesOK = false
		}
	}
	if timesOK {
		for _, k := range []struct {
			key string
			d   time.Duration
		}{{"blob.backup.rebaseline", t.Rebaseline}, {"blob.backup.retention", t.Retention}} {
			if k.d < t.Interval {
				f.refuse(k.key, "%s is below blob.backup.interval: a generation would expire or a new epoch begin "+
					"before the next run", k.key)
			}
		}
	}
	return append(f.errs, f.warns...)
}

// atOrBelow reports dir at or below root.
func atOrBelow(dir, root string) bool {
	return dir == root || strings.HasPrefix(dir, strings.TrimSuffix(root, string(os.PathSeparator))+string(os.PathSeparator))
}

// blobKeysOffDefault names the blob.* keys set off their defaults; an empty duration is its default.
func (c Config) blobKeysOffDefault() []string {
	b, d := c.Blob, defaults().Blob
	var keys []string
	for _, k := range []struct {
		key string
		off bool
	}{
		{"blob.target", b.Target != d.Target},
		{"blob.credentialsFile", b.CredentialsFile != d.CredentialsFile},
		{"blob.dir", b.Dir != d.Dir},
		{"blob.allowUnversioned", b.AllowUnversioned != d.AllowUnversioned},
		{"blob.versionRetention", b.VersionRetention != ""},
		{"blob.backup.interval", offDuration(b.Backup.Interval, defaultBlobBackupInterval)},
		{"blob.backup.rebaseline", offDuration(b.Backup.Rebaseline, defaultBlobRebaseline)},
		{"blob.backup.retention", offDuration(b.Backup.Retention, defaultBlobRetention)},
	} {
		if k.off {
			keys = append(keys, k.key)
		}
	}
	return keys
}
