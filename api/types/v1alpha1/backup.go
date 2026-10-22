package v1alpha1

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// maxBackupScheduleNameLength keeps <schedule>-<slot in Unix minutes> a DNS label: 63 less "-" and the 8 digits of
// Unix minutes up to 2160 (ADR-0222 Decision 3).
const maxBackupScheduleNameLength = 54

// maxRestoreNameLength keeps <restore>-before, an in-place Restore's export, a DNS label (ADR-0222 Decision 3).
const maxRestoreNameLength = 56

// DefaultBackupTTL is a BackupSchedule's ttl when unset (ADR-0222 Decision 1).
const DefaultBackupTTL = Duration(720 * time.Hour)

// BackupSchedule is a namespaced, status-bearing resource (ADR-0222, F111): an app owner's definition of which stores
// to copy, when, to which S3 target and sealed to whom. Its controller creates a Backup per cron slot.
type BackupSchedule struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       BackupScheduleSpec   `json:"spec"`
	Status     BackupScheduleStatus `json:"status,omitempty"`
}

// BackupScheduleSpec is the definition (Decision 1): a cron schedule in a time zone (ADR-0211), the Backups' ttl, the
// scope, the target and the recipients.
type BackupScheduleSpec struct {
	Schedule   string           `json:"schedule"`
	TimeZone   string           `json:"timeZone,omitempty"`
	TTL        Duration         `json:"ttl,omitempty"`
	Paused     bool             `json:"paused,omitempty"`
	Scope      BackupScope      `json:"scope"`
	Target     BackupTarget     `json:"target"`
	Encryption BackupEncryption `json:"encryption,omitempty"`
}

// EffectiveTTL is spec.ttl, DefaultBackupTTL when unset.
func (s BackupScheduleSpec) EffectiveTTL() Duration {
	if s.TTL == 0 {
		return DefaultBackupTTL
	}
	return s.TTL
}

// BackupScope names the members in one form (Decision 2): names, an App, or a ResourceGroup.
type BackupScope struct {
	KVStores      []ObjectName      `json:"kvStores,omitempty"`
	Buckets       []ObjectName      `json:"buckets,omitempty"`
	Lakes         []ObjectName      `json:"lakes,omitempty"`
	Databases     []ObjectName      `json:"databases,omitempty"`
	App           ObjectName        `json:"app,omitempty"`
	ResourceGroup ResourceGroupName `json:"resourceGroup,omitempty"`
}

// BackupTarget is an s3:// URL and the Secret of its write credential (Decision 6).
type BackupTarget struct {
	URL               string     `json:"url"`
	CredentialsSecret ObjectName `json:"credentialsSecret"`
}

// BackupEncryption is the age recipients every Backup is sealed to (Decision 6).
type BackupEncryption struct {
	Recipients []string `json:"recipients"`
}

// BackupScheduleStatus is the observed state (Decision 9): the slot last taken, the last success, the oldest stored
// Succeeded Backup, the Backup running and the RPO alert.
type BackupScheduleStatus struct {
	Status               `json:",inline"`
	LastScheduleTime     *Timestamp `json:"lastScheduleTime,omitempty"`
	LastSuccessTime      *Timestamp `json:"lastSuccessTime,omitempty"`
	EarliestRestorePoint *Timestamp `json:"earliestRestorePoint,omitempty"`
	Active               ObjectName `json:"active,omitempty"`
	RPORisk              bool       `json:"rpoRisk,omitempty"`
}

// Backup is a namespaced, status-bearing run record (ADR-0222): one copy of its schedule's members, made per slot, by
// hand or by a hook. Its spec is immutable; it outlives its schedule and App until its expireTime.
type Backup struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       BackupSpec   `json:"spec"`
	Status     BackupStatus `json:"status,omitempty"`
}

// BackupSpec names the schedule whose scope, target and recipients the run uses; ttl, at most the schedule's,
// overrides it.
type BackupSpec struct {
	Schedule ObjectName `json:"schedule"`
	TTL      Duration   `json:"ttl,omitempty"`
}

// BackupStatus is the run (Decision 4): its phase, times, where it wrote, what and to whom.
type BackupStatus struct {
	Status `json:",inline"`
	// Phase shadows Status.Phase, as WorkflowRunStatus's: Pending → Running → Succeeded | Failed.
	Phase          RunPhase       `json:"phase,omitempty"`
	StartTime      *Timestamp     `json:"startTime,omitempty"`
	CompletionTime *Timestamp     `json:"completionTime,omitempty"`
	ExpireTime     *Timestamp     `json:"expireTime,omitempty"`
	Target         string         `json:"target,omitempty"`
	Path           string         `json:"path,omitempty"`
	Members        []BackupMember `json:"members,omitempty"`
	// Recipients are the fingerprints of the recipients the run sealed to (ADR-0204 Decision 3).
	Recipients []string `json:"recipients,omitempty"`
}

// BackupMember is one store a Backup copied and the bytes it stored.
type BackupMember struct {
	Kind  Kind       `json:"kind"`
	Name  ObjectName `json:"name"`
	Bytes int64      `json:"bytes,omitempty"`
}

// Restore is a namespaced, status-bearing request (ADR-0222 Decision 7): load members of a Succeeded Backup into
// empty stores, or in place. Its spec is immutable.
type Restore struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RestoreSpec   `json:"spec"`
	Status     RestoreStatus `json:"status,omitempty"`
}

// RestoreSpec names the Backup, the Secrets that read and open it, and each member's target.
type RestoreSpec struct {
	Backup  ObjectName      `json:"backup"`
	Source  RestoreSource   `json:"source"`
	Members []RestoreMember `json:"members"`
}

// RestoreSource is the Secret of the read credential (get and list) and the Secret holding AGE_IDENTITY.
type RestoreSource struct {
	CredentialsSecret ObjectName `json:"credentialsSecret"`
	IdentitySecret    ObjectName `json:"identitySecret,omitempty"`
}

// RestoreMember is one member of the Backup and exactly one target: as (an empty KVStore, Bucket or SQLDatabase),
// asLocation (an empty bucket prefix) or inPlace.
type RestoreMember struct {
	Kind       Kind          `json:"kind"`
	Name       ObjectName    `json:"name"`
	As         ObjectName    `json:"as,omitempty"`
	AsLocation *LakeLocation `json:"asLocation,omitempty"`
	InPlace    bool          `json:"inPlace,omitempty"`
}

// RestoreStatus is the request's run: its phase, times and, in place, the export Backup.
type RestoreStatus struct {
	Status         `json:",inline"`
	Phase          RunPhase   `json:"phase,omitempty"`
	StartTime      *Timestamp `json:"startTime,omitempty"`
	CompletionTime *Timestamp `json:"completionTime,omitempty"`
	Export         ObjectName `json:"export,omitempty"`
}

// AppBackupSchedule is a backupSchedules entry of an App (ADR-0222 Decision 2): a BackupSchedule the App declares by
// name, or only the ref of an existing one. An empty scope backs up the App itself.
type AppBackupSchedule struct {
	Name               ObjectName `json:"name,omitempty"`
	Ref                ObjectName `json:"ref,omitempty"`
	BackupScheduleSpec `json:",inline"`
}

// BackupSlotName is the Backup a schedule creates for slot: <schedule>-<slot in Unix minutes> (Decision 3).
func BackupSlotName(schedule ObjectName, slot time.Time) ObjectName {
	return ObjectName(string(schedule) + "-" + strconv.FormatInt(slot.Unix()/60, 10))
}

// RestoreExportName is the Backup an in-place Restore runs first: <restore>-before (Decision 7).
func RestoreExportName(restore ObjectName) ObjectName { return ObjectName(string(restore) + "-before") }

// GroupVersionKind returns the constant GVK for BackupSchedule.
func (s *BackupSchedule) GroupVersionKind() GroupVersionKind { return KindBackupSchedule.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (s *BackupSchedule) GetStatus() *Status { return &s.Status.Status }

// Validate enforces the store-free rules (Decisions 1, 2, 3, 6): a name of at most 54 characters, a cron schedule and
// time zone, a ttl not negative, one scope form, an s3:// target with its Secret and at least 2 distinct recipients.
// The recipients' age parse and the target's overlap with the store are the backup-schedule admission's.
func (s *BackupSchedule) Validate() error {
	const op = "BackupSchedule.Validate"
	if err := validateMeta(s.TypeMeta, &s.ObjectMeta, KindBackupSchedule); err != nil {
		return err
	}
	if len(s.Name) > maxBackupScheduleNameLength {
		return fault.Invalidf(op, "metadata.name %q is longer than %d characters", s.Name, maxBackupScheduleNameLength)
	}
	return s.Spec.validate(op)
}

func (s BackupScheduleSpec) validate(op string) error {
	if err := CheckCron(op, "spec.schedule", s.Schedule, "spec.timeZone", s.TimeZone); err != nil {
		return err
	}
	if s.TTL < 0 {
		return fault.Invalidf(op, "spec.ttl %s is negative", s.TTL)
	}
	if err := s.Scope.validate(op); err != nil {
		return err
	}
	if err := checkS3URL(op, "spec.target.url", s.Target.URL); err != nil {
		return err
	}
	if !dnsLabel.MatchString(string(s.Target.CredentialsSecret)) {
		return fault.Invalidf(op, "spec.target.credentialsSecret %q is not a valid DNS-1123 label", s.Target.CredentialsSecret)
	}
	seen := make(map[string]bool, len(s.Encryption.Recipients))
	for i, r := range s.Encryption.Recipients {
		switch {
		case r == "":
			return fault.Invalidf(op, "spec.encryption.recipients[%d] is empty", i)
		case seen[r]:
			return fault.Invalidf(op, "spec.encryption.recipients[%d] repeats %q", i, r)
		}
		seen[r] = true
	}
	if len(seen) < 2 {
		return fault.Invalidf(op, "spec.encryption.recipients has %d recipient(s): want at least 2 (an owner and a recovery one)", len(seen))
	}
	return nil
}

// validate refuses a scope in no form or in two, and a name that is not a DNS label or repeats another.
func (s BackupScope) validate(op string) error {
	names := len(s.KVStores) + len(s.Buckets) + len(s.Lakes) + len(s.Databases)
	forms := 0
	for _, set := range []bool{names > 0, s.App != "", s.ResourceGroup != ""} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return fault.Invalidf(op, "spec.scope sets %d forms: want one of kvStores, buckets, lakes and databases, app, or resourceGroup", forms)
	}
	for _, l := range []struct {
		field string
		names []ObjectName
	}{{"kvStores", s.KVStores}, {"buckets", s.Buckets}, {"lakes", s.Lakes}, {"databases", s.Databases}} {
		seen := make(map[ObjectName]bool, len(l.names))
		for i, n := range l.names {
			if !dnsLabel.MatchString(string(n)) {
				return fault.Invalidf(op, "spec.scope.%s[%d] %q is not a valid DNS-1123 label", l.field, i, n)
			}
			if seen[n] {
				return fault.Invalidf(op, "spec.scope.%s[%d] repeats %q", l.field, i, n)
			}
			seen[n] = true
		}
	}
	if s.App != "" && !dnsLabel.MatchString(string(s.App)) {
		return fault.Invalidf(op, "spec.scope.app %q is not a valid DNS-1123 label", s.App)
	}
	if s.ResourceGroup != "" && !dnsLabel.MatchString(string(s.ResourceGroup)) {
		return fault.Invalidf(op, "spec.scope.resourceGroup %q is not a valid DNS-1123 label", s.ResourceGroup)
	}
	return nil
}

// checkS3URL refuses a target that is not an s3://<bucket> URL (Decision 6): a file:// path is outside the namespace.
func checkS3URL(op, field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "%s %q is not a URL", field, raw)
	}
	if u.Scheme != "s3" || u.Host == "" {
		return fault.Invalidf(op, "%s %q: want s3://<bucket>[?…], the only target scheme", field, raw)
	}
	if _, err := url.ParseQuery(u.RawQuery); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "%s %q has a bad query", field, raw)
	}
	return nil
}

// GroupVersionKind returns the constant GVK for Backup.
func (b *Backup) GroupVersionKind() GroupVersionKind { return KindBackup.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject. Its Phase is unused: BackupStatus.Phase
// shadows it.
func (b *Backup) GetStatus() *Status { return &b.Status.Status }

// Validate checks the schedule's name and a ttl not negative; the ttl's bound by the schedule's is admission's.
func (b *Backup) Validate() error {
	const op = "Backup.Validate"
	if err := validateMeta(b.TypeMeta, &b.ObjectMeta, KindBackup); err != nil {
		return err
	}
	if !dnsLabel.MatchString(string(b.Spec.Schedule)) {
		return fault.Invalidf(op, "spec.schedule %q is not a valid DNS-1123 label", b.Spec.Schedule)
	}
	if b.Spec.TTL < 0 {
		return fault.Invalidf(op, "spec.ttl %s is negative", b.Spec.TTL)
	}
	return nil
}

// GroupVersionKind returns the constant GVK for Restore.
func (r *Restore) GroupVersionKind() GroupVersionKind { return KindRestore.GVK() }

// GetStatus returns the shared Status pointer, implementing StatusObject. Its Phase is unused: RestoreStatus.Phase
// shadows it.
func (r *Restore) GetStatus() *Status { return &r.Status.Status }

// InPlace reports a member restored in place, which needs update on the Namespace (Decision 10).
func (r *Restore) InPlace() bool {
	for _, m := range r.Spec.Members {
		if m.InPlace {
			return true
		}
	}
	return false
}

// Ended reports a Restore Succeeded or Failed (Interrupted included); one not ended is Pending or Running.
func (r *Restore) Ended() bool { return r.Status.Phase == RunSucceeded || r.Status.Phase == RunFailed }

// Validate enforces Decision 7's shape: a name of at most 56 characters, the Backup and the two Secrets named, and
// each member a KVStore, Bucket or Lake, once, with exactly one target of its kind.
func (r *Restore) Validate() error {
	const op = "Restore.Validate"
	if err := validateMeta(r.TypeMeta, &r.ObjectMeta, KindRestore); err != nil {
		return err
	}
	if len(r.Name) > maxRestoreNameLength {
		return fault.Invalidf(op, "metadata.name %q is longer than %d characters", r.Name, maxRestoreNameLength)
	}
	for _, f := range []struct {
		field string
		name  ObjectName
	}{{"spec.backup", r.Spec.Backup}, {"spec.source.credentialsSecret", r.Spec.Source.CredentialsSecret},
		{"spec.source.identitySecret", r.Spec.Source.IdentitySecret}} {
		if !dnsLabel.MatchString(string(f.name)) {
			return fault.Invalidf(op, "%s %q is not a valid DNS-1123 label", f.field, f.name)
		}
	}
	if len(r.Spec.Members) == 0 {
		return fault.Invalidf(op, "spec.members is empty: name at least one member of the Backup")
	}
	seen := make(map[ObjectRef]string, len(r.Spec.Members))
	for i, m := range r.Spec.Members {
		path := fmt.Sprintf("spec.members[%d]", i)
		if err := m.validate(op, path); err != nil {
			return err
		}
		k := ObjectRef{Kind: m.Kind, Name: m.Name}
		if prev, dup := seen[k]; dup {
			return fault.Invalidf(op, "%s repeats %s/%s of %s", path, m.Kind, m.Name, prev)
		}
		seen[k] = path
	}
	return nil
}

func (m RestoreMember) validate(op, path string) error {
	if !dnsLabel.MatchString(string(m.Name)) {
		return fault.Invalidf(op, "%s.name %q is not a valid DNS-1123 label", path, m.Name)
	}
	targets := 0
	for _, set := range []bool{m.As != "", m.AsLocation != nil, m.InPlace} {
		if set {
			targets++
		}
	}
	if targets != 1 {
		return fault.Invalidf(op, "%s sets %d targets: want exactly one of as, asLocation and inPlace", path, targets)
	}
	switch m.Kind {
	case KindKVStore, KindBucket, KindSQLDatabase:
		if m.AsLocation != nil {
			return fault.Invalidf(op, "%s.asLocation applies to a Lake: a %s takes as or inPlace", path, m.Kind)
		}
		if m.As != "" && !dnsLabel.MatchString(string(m.As)) {
			return fault.Invalidf(op, "%s.as %q is not a valid DNS-1123 label", path, m.As)
		}
	case KindLake:
		if m.As != "" {
			return fault.Invalidf(op, "%s.as applies to a KVStore, Bucket or SQLDatabase: a Lake takes asLocation or inPlace", path)
		}
		if c := m.AsLocation; c != nil && (!dnsLabel.MatchString(string(c.Bucket)) || !dnsLabel.MatchString(c.Prefix)) {
			return fault.Invalidf(op, "%s.asLocation %q/%q: bucket and prefix must be DNS-1123 labels", path, c.Bucket, c.Prefix)
		}
	default:
		return fault.Invalidf(op, "%s.kind %q: want KVStore, Bucket, Lake or SQLDatabase", path, m.Kind)
	}
	return nil
}
