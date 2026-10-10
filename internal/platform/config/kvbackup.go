package config

import (
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// kvChunkBytesMax is the default and the maximum of kvstore.backup.chunkBytes: ADR-0203's part size (ADR-0209
// Decision 2).
const kvChunkBytesMax = 8 << 20

// The KV export's times when a key is empty (ADR-0067, ADR-0195).
const (
	defaultKVBackupInterval   = 30 * time.Second
	defaultKVRebaseline       = 24 * time.Hour
	defaultKVRebaselineRetry  = time.Hour
	kvBackupKeyPrefix         = "kvstore.backup."
	platformBackupKeyPrefix   = "backup."
	kvRecipientsKey           = kvBackupKeyPrefix + "encryption.recipients"
	kvRebaselineKey           = kvBackupKeyPrefix + "rebaseline"
	kvRetentionHourlyKey      = kvBackupKeyPrefix + "retention.hourly"
	platformEncryptionNoneKey = platformBackupKeyPrefix + "encryption.none"
	platformRecipientsKey     = platformBackupKeyPrefix + "encryption.recipients"
)

// KVBackup is kvstore.backup after ADR-0209 Decision 1: the target group (Target, CredentialsFile, SingleWriter) and
// the encryption group (None, Recipients). Shared is true when the target group is backup:'s.
type KVBackup struct {
	Target, CredentialsFile    string
	SingleWriter, Shared, None bool
	Recipients                 []string
}

// ResolveKVBackup takes each group whole from kvstore.backup when its lead key is set there, else from backup:. A
// credentialsFile or singleWriter without the KV's own target, a KV target equal to backup.target, no target in
// either block, or no recipients and no none in either block is fault.Invalid naming the key.
func (c Config) ResolveKVBackup() (KVBackup, error) {
	kb, refusal := c.resolveKVBackup()
	if refusal != nil {
		return KVBackup{}, fault.Invalidf("config.ResolveKVBackup", "%s", refusal.Message)
	}
	return kb, nil
}

// KVEncryptionKeyPrefix is the block the KV's encryption group comes from, which names its keys in errors:
// "kvstore.backup." when encryption.recipients or encryption.none is set there, else "backup.".
func (c Config) KVEncryptionKeyPrefix() string {
	if e := c.Kvstore.Backup.Encryption; len(e.Recipients) > 0 || e.None {
		return kvBackupKeyPrefix
	}
	return platformBackupKeyPrefix
}

func (c Config) resolveKVBackup() (KVBackup, *Finding) {
	k, b := c.Kvstore.Backup, c.Backup
	refuse := func(key, msg string) (KVBackup, *Finding) {
		return KVBackup{}, &Finding{Key: key, Message: msg, Error: true}
	}
	var out KVBackup
	switch {
	case k.Target == "" && k.CredentialsFile != "":
		return refuse(kvBackupKeyPrefix+"credentialsFile", "kvstore.backup.credentialsFile is set without "+
			"kvstore.backup.target: the target group comes whole from one block")
	case k.Target == "" && k.SingleWriter:
		return refuse(kvBackupKeyPrefix+"singleWriter", "kvstore.backup.singleWriter is set without "+
			"kvstore.backup.target: the target group comes whole from one block")
	case k.Target != "" && k.Target == b.Target:
		return refuse(kvBackupKeyPrefix+"target", "kvstore.backup.target equals backup.target: leave it unset to "+
			"share the platform target")
	case k.Target != "":
		out.Target, out.CredentialsFile, out.SingleWriter = k.Target, k.CredentialsFile, k.SingleWriter
	case b.Target != "":
		out.Target, out.CredentialsFile, out.SingleWriter, out.Shared = b.Target, b.CredentialsFile, b.SingleWriter, true
	default:
		return refuse(kvBackupKeyPrefix+"target", "kvstore.backup.enabled but kvstore.backup.target and "+
			"backup.target are empty")
	}
	if c.KVEncryptionKeyPrefix() == kvBackupKeyPrefix {
		out.None, out.Recipients = k.Encryption.None, k.Encryption.Recipients
	} else {
		out.None, out.Recipients = b.Encryption.None, b.Encryption.Recipients
	}
	if !out.None && len(out.Recipients) == 0 {
		return refuse(kvRecipientsKey, "kvstore.backup.encryption.recipients and backup.encryption.recipients are "+
			"empty: set at least 2 age recipients in either block, or encryption.none")
	}
	return out, nil
}

// checkKVBackup applies ADR-0209's kvstore.backup rows while the KV export is on outside memory mode:
// ResolveKVBackup's refusals, the times, chunkBytes above 8 MiB, the retention ranges, and a rebaseline that reaches
// the hourly term (Decision 5).
func (c Config) checkKVBackup(f *findings) {
	k := c.Kvstore.Backup
	if !k.Enabled || c.Storage.Mode == "memory" {
		return
	}
	if _, refusal := c.resolveKVBackup(); refusal != nil {
		f.errs = append(f.errs, *refusal)
	}
	var interval, rebaseline, retry time.Duration
	rebaselineOK := true
	for _, t := range []backupTimeKey{
		{kvBackupKeyPrefix + "interval", k.Interval, defaultKVBackupInterval, &interval},
		{kvRebaselineKey, k.Rebaseline, defaultKVRebaseline, &rebaseline},
		{kvBackupKeyPrefix + "rebaselineRetry", k.RebaselineRetry, defaultKVRebaselineRetry, &retry},
	} {
		if err := t.parse(); err != nil {
			f.refuse(t.key, "%v", err)
			rebaselineOK = rebaselineOK && t.key != kvRebaselineKey
		}
	}
	if k.ChunkBytes > kvChunkBytesMax {
		f.refuse(kvBackupKeyPrefix+"chunkBytes", "kvstore.backup.chunkBytes is %d: want at most %d (8 MiB)",
			k.ChunkBytes, kvChunkBytesMax)
	}
	r := k.Retention
	for _, rk := range []struct {
		key      string
		v, least int
	}{
		{kvRetentionHourlyKey, r.Hourly, 1},
		{kvBackupKeyPrefix + "retention.daily", r.Daily, 0},
		{kvBackupKeyPrefix + "retention.weekly", r.Weekly, 0},
	} {
		if rk.v < rk.least {
			f.refuse(rk.key, "%s is %d: want at least %d", rk.key, rk.v, rk.least)
		}
	}
	if days := (r.Hourly + 23) / 24; rebaselineOK && r.Hourly >= 1 && rebaseline >= time.Duration(days)*24*time.Hour {
		f.refuse(kvRebaselineKey, "%s %s reaches %s %d (%d days): a 30 s point would expire before the next base; "+
			"want a rebaseline below %d days", kvRebaselineKey, k.Rebaseline, kvRetentionHourlyKey, r.Hourly, days, days)
	}
}

// kvInheritsEncryption reports the KV export on and taking backup.encryption.* (Decision 1), so those keys are not
// ignored without backup.target.
func (c Config) kvInheritsEncryption() bool {
	return c.Kvstore.Backup.Enabled && c.Storage.Mode != "memory" && c.KVEncryptionKeyPrefix() == platformBackupKeyPrefix
}
