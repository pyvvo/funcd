package v1alpha1

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
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
	Schedule   string           `json:"schedule" doc:"The cron slots at which the schedule creates a Backup: 5 fields (minute hour day-of-month month day-of-week) or a macro such as @daily, in timeZone. Slots missed during downtime, a pause or a hold collapse into one Backup, and a slot that comes while one of the schedule's Backups is Pending or Running creates none."`
	TimeZone   string           `json:"timeZone,omitempty" doc:"The IANA time zone of schedule, such as Europe/Paris; UTC when empty."`
	TTL        Duration         `json:"ttl,omitempty" doc:"How long funcd keeps each Backup's record after its creation; 0s or unset means 720h. Only the record goes: the copy stays on the target until the target's own lifecycle rule expires it. A Backup may set a shorter spec.ttl, never a longer one."`
	Paused     bool             `json:"paused,omitempty" doc:"While true, the schedule creates no Backup and rpoRisk stays false; a Backup created by hand still runs. Once it is false again, the slots missed meanwhile collapse into one Backup."`
	Scope      BackupScope      `json:"scope" doc:"The stores each Backup copies, in one form: the lists kvStores, buckets, lakes, databases and volumes, or app, or resourceGroup. funcd resolves it at each run: a member or App it names that is missing then fails the Backup with reason MemberNotFound, and a scope of more than workloadBackup.maxCopyMembers members fails it with no copy written. In an App's spec.backupSchedules entry, an empty scope backs up that App."`
	Target     BackupTarget     `json:"target" doc:"Where each Backup writes its copy: an S3 bucket and the Secret of its write key."`
	Encryption BackupEncryption `json:"encryption,omitempty" doc:"The age recipients that seal the member files of each Backup. It is required: a schedule with fewer than 2 recipients is refused."`
}

// EffectiveTTL is spec.ttl, DefaultBackupTTL when unset.
func (s BackupScheduleSpec) EffectiveTTL() Duration {
	if s.TTL == 0 {
		return DefaultBackupTTL
	}
	return s.TTL
}

// BackupScope names the members in one form (Decision 2): names, an App, or a ResourceGroup. In each form a Bucket brings in
// the Lakes on its prefixes as their own members, since its copy holds none of their data (ADR-0312).
type BackupScope struct {
	KVStores  []ObjectName `json:"kvStores,omitempty" doc:"KVStores of the namespace to copy, by name, each once."`
	Buckets   []ObjectName `json:"buckets,omitempty" doc:"Buckets of the namespace to copy, by name, each once. A Bucket's copy holds none of the data of the Lakes located on its prefixes: each of them is copied as a member of its own."`
	Lakes     []ObjectName `json:"lakes,omitempty" doc:"Lakes of the namespace to copy, by name, each once. A Lake located on a prefix of a listed Bucket is copied without being named here. A Lake that deletes files during each of a run's copies, as a busy one may, fails the Backup with reason LakeUnstable, and so does a Bucket that brings it in."`
	Databases []ObjectName `json:"databases,omitempty" doc:"SQLDatabases of the namespace to copy, by name, each once. A database whose engine is not Ready fails the Backup with reason EngineNotReady."`
	// Volumes are local Function volumes, each "<function>/<name>" (ADR-0262 Decision 11).
	Volumes       []string          `json:"volumes,omitempty" doc:"Local volumes of the namespace's Functions to copy, each as <function>/<name>, both DNS-1123 labels, and listed once. A volume is read live, file by file, so its copy is not a single point in time."`
	App           ObjectName        `json:"app,omitempty" doc:"An App of the namespace: each Backup copies the stores of its kv, buckets, lakes and sql entries, ref entries included, and the local volumes of the Functions it declares. Set it alone, with no other scope field."`
	ResourceGroup ResourceGroupName `json:"resourceGroup,omitempty" doc:"A resource group of the namespace: each Backup copies its KVStores, Buckets, Lakes and SQLDatabases, but not its Functions' local volumes. Set it alone, with no other scope field."`
}

// BackupTarget is an s3:// URL and the Secret of its write credential (Decision 6).
type BackupTarget struct {
	URL               string     `json:"url" doc:"The s3:// URL of the bucket that receives the copies, its options as query parameters, such as s3://backups?region=eu-west-3. The bucket of blob.target is refused, and another bucket on its endpoint is accepted with reason SameProvider on the schedule's Ready condition: one failure of that provider takes both. The bucket must accept a create-if-absent put (If-None-Match: *): a bucket that refuses it fails each Backup with reason TargetUnconditional."`
	CredentialsSecret ObjectName `json:"credentialsSecret" doc:"A Secret of the namespace with AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and, optionally, AWS_SESSION_TOKEN: the key each Backup writes with. It needs only put and list on the bucket."`
}

// BackupEncryption is the age recipients every Backup is sealed to (Decision 6).
type BackupEncryption struct {
	Recipients []string `json:"recipients" doc:"The age recipients, each entry one recipient: at least 2 distinct ones, such as an owner's and a recovery one, all X25519 or all hybrid (age1pq1). Every member file of each Backup is sealed to all of them, so the identity of any one opens it. The copy's manifest.yaml, which names the members, their files' sizes and these recipients' fingerprints, is not sealed."`
}

// BackupScheduleStatus is the observed state (Decision 9): the slot last taken, the last success, the oldest stored
// Succeeded Backup, the Backup running and the RPO alert.
type BackupScheduleStatus struct {
	Status               `json:",inline"`
	LastScheduleTime     *Timestamp `json:"lastScheduleTime,omitempty" doc:"The newest slot the schedule has taken, whether it created a Backup or, finding one of its Backups still Pending or Running, created none."`
	LastSuccessTime      *Timestamp `json:"lastSuccessTime,omitempty" doc:"When the newest of the schedule's Succeeded Backups that still exist completed."`
	EarliestRestorePoint *Timestamp `json:"earliestRestorePoint,omitempty" doc:"When the oldest of the schedule's Succeeded Backups that still exist started: the oldest point a Restore can go back to."`
	Active               ObjectName `json:"active,omitempty" doc:"A Backup of the schedule that is Pending or Running, empty when none is."`
	RPORisk              bool       `json:"rpoRisk,omitempty" doc:"True when the schedule has fallen behind: the first slot after the start of its newest Succeeded Backup, or after its creation when there is none, has passed with that slot's Backup Failed or missing, or a second slot has passed. It stays false while the schedule is paused. funcd logs workload backup rpo at risk when it turns true."`
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
	Schedule ObjectName `json:"schedule" doc:"The BackupSchedule of the namespace whose scope, target and recipients the run uses, read when the run starts: a schedule gone by then fails the Backup with reason ScheduleNotFound. The schedule's status counts every Backup that names it."`
	TTL      Duration   `json:"ttl,omitempty" doc:"How long funcd keeps the record after its creation; 0s or unset means the schedule's ttl, or 720h when the schedule does not exist. A ttl longer than the schedule's is refused."`
}

// BackupStatus is the run (Decision 4): its phase, times, where it wrote, what and to whom.
type BackupStatus struct {
	Status `json:",inline"`
	// Phase shadows Status.Phase, as WorkflowRunStatus's: Pending → Running → Succeeded | Failed.
	Phase          RunPhase       `json:"phase,omitempty" doc:"Pending, then Running, then Succeeded or Failed, in one attempt: funcd does not retry a failed Backup. A Backup that was Running when funcd stopped ends at the next start, Succeeded with reason FoundOnTarget when its copy is whole on the target, else Failed with reason Interrupted."`
	StartTime      *Timestamp     `json:"startTime,omitempty" doc:"When the run began."`
	CompletionTime *Timestamp     `json:"completionTime,omitempty" doc:"When the run ended, Succeeded or Failed."`
	ExpireTime     *Timestamp     `json:"expireTime,omitempty" doc:"When funcd deletes this record, once the run has ended: the Backup's creation plus its spec.ttl, else plus its schedule's ttl. The copy on the target stays until the target's own lifecycle rule removes it."`
	Target         string         `json:"target,omitempty" doc:"The target URL the run wrote to: its schedule's spec.target.url at the time. A Restore reads the copy there."`
	Path           string         `json:"path,omitempty" doc:"The copy's directory on the target, <namespace>/<backup>-<uid>/."`
	Members        []BackupMember `json:"members,omitempty" doc:"The stores the copy holds, which a Restore names as they are listed here. A Bucket's member holds none of the data of the Lakes located on its prefixes: each of them is a member of its own."`
	// Recipients are the fingerprints of the recipients the run sealed to (ADR-0204 Decision 3).
	Recipients []string `json:"recipients,omitempty" doc:"Fingerprints of the age recipients the copy is sealed to, sorted. A Restore whose identity matches none of them fails, its message naming both sets of fingerprints. A Backup recorded with reason FoundOnTarget lists none."`
}

// BackupMember is one store a Backup copied and the bytes it stored.
type BackupMember struct {
	Kind  Kind       `json:"kind" doc:"The member's kind: KVStore, Bucket, Lake, SQLDatabase, or Function for a Function's local volume."`
	Name  ObjectName `json:"name" doc:"The member's name: the store's, or for a volume, its Function's."`
	Bytes int64      `json:"bytes,omitempty" doc:"The size of the member's sealed copy on the target, in bytes."`
	// Volume, with Kind Function, names the Function's local volume the member is (ADR-0262 Decision 11).
	Volume ObjectName `json:"volume,omitempty" doc:"With kind Function, the name of the Function's local volume that the member is."`
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
	Backup  ObjectName      `json:"backup" doc:"The Backup to load, in the Restore's namespace. The Restore waits while it is Pending or Running, and fails with reason BackupNotReady when it is missing or Failed."`
	Source  RestoreSource   `json:"source" doc:"The Secrets that read the Backup's copy and open its seal."`
	Members []RestoreMember `json:"members" doc:"The members of the Backup to load, at least one and each once, loaded in this order after funcd has checked the whole copy and every destination. A Bucket's member leaves its destination's Lake prefixes as they are: to restore those Lakes, name them as members too."`
}

// RestoreSource is the Secret of the read credential (get and list) and the Secret holding AGE_IDENTITY.
type RestoreSource struct {
	CredentialsSecret ObjectName `json:"credentialsSecret" doc:"A Secret of the namespace with AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and, optionally, AWS_SESSION_TOKEN: the key the Restore reads the copy with, at the Backup's status.target. A read-only key is enough."`
	IdentitySecret    ObjectName `json:"identitySecret,omitempty" doc:"A Secret of the namespace whose AGE_IDENTITY key holds age identities, X25519 or hybrid and not passphrase-encrypted, one of them for a recipient the Backup is sealed to. It is required: a Restore without it is refused."`
}

// RestoreMember is one member of the Backup and exactly one target: as (an empty KVStore, Bucket or SQLDatabase, or
// the empty volume of another Function), asLocation (an empty bucket prefix where no Lake is located, for a Lake) or
// inPlace. A Bucket's member acts only outside the prefixes of the Lakes on its destination, so restoring a Lake takes
// naming that Lake too (ADR-0312).
type RestoreMember struct {
	Kind       Kind          `json:"kind" doc:"The member's kind, as the Backup's status.members lists it: KVStore, Bucket, Lake, SQLDatabase, or Function for a Function's local volume."`
	Name       ObjectName    `json:"name" doc:"The member's name, as the Backup's status.members lists it: the store's, or for a volume, its Function's. A member the copy does not hold fails the Restore with reason MemberNotFound."`
	As         ObjectName    `json:"as,omitempty" doc:"The destination: the name of an existing, empty store of the member's kind in the namespace, or for a volume, of a Function whose local volume of that name is empty. A SQLDatabase is empty with no table, view, index or trigger and no Function binding it. Exactly one of as, asLocation and inPlace; a Lake takes asLocation or inPlace."`
	AsLocation *LakeLocation `json:"asLocation,omitempty" doc:"For a Lake: the bucket and prefix that receive its copy, where no Lake is located and nothing is stored yet; a Lake located there afterwards reads it. The bucket must exist and may not be one this Restore restores in place. Exactly one of as, asLocation and inPlace."`
	InPlace    bool          `json:"inPlace,omitempty" doc:"Load the member back into its own store, replacing its data; a Function's volume is loaded only when empty or named by its Function's DataMissing condition. The Restore then needs update on the Namespace, as an admin has, and another in-place Restore of the namespace is refused until it ends. It first takes the export Backup that status.export names, which needs the restored Backup's schedule, then holds the namespace while it loads: a failed load keeps the hold until funcdctl hold release --namespace. Exactly one of as, asLocation and inPlace."`
	// Volume, with Kind Function, makes the member that Function's local volume; as names another Function whose
	// empty volume of that name receives it (ADR-0262 Decision 11).
	Volume ObjectName `json:"volume,omitempty" doc:"With kind Function, which needs it, the name of the Function's local volume that the member is. Any other kind takes none."`
}

// RestoreStatus is the request's run: its phase, times and, in place, the export Backup.
type RestoreStatus struct {
	Status         `json:",inline"`
	Phase          RunPhase   `json:"phase,omitempty" doc:"Pending while it waits for its Backup and, in place, for its export; Running while it checks the whole copy and loads; then Succeeded or Failed. A Restore that was Running when funcd stopped ends Failed with reason Interrupted."`
	StartTime      *Timestamp `json:"startTime,omitempty" doc:"When the Restore turned Running, once its Backup and any export had Succeeded."`
	CompletionTime *Timestamp `json:"completionTime,omitempty" doc:"When the Restore ended, Succeeded or Failed."`
	Export         ObjectName `json:"export,omitempty" doc:"The Backup that an in-place Restore takes before it loads, <restore>-before, of the restored Backup's schedule. Empty when no member is in place."`
}

// AppBackupSchedule is a backupSchedules entry of an App (ADR-0222 Decision 2): a BackupSchedule the App declares by
// name, or only the ref of an existing one. An empty scope backs up the App itself.
type AppBackupSchedule struct {
	Name               ObjectName `json:"name,omitempty"`
	Ref                ObjectName `json:"ref,omitempty"`
	BackupScheduleSpec `json:",inline"`
}

// ConditionTargetSynced is a BackupSchedule's condition after a platform restore (ADR-0287): False with
// ReasonPlatformRestored from funcd restore run, True once a sync recorded the whole copies its target holds after the
// restored point, False with ReasonListFailed while the listing fails: the target refuses it, or what it lists is over a
// listing's limits. While it is False the schedule takes no slot.
const ConditionTargetSynced = ConditionType("TargetSynced")

// The reasons of ADR-0287: restore run's mark on a restored schedule, a sync whose listing failed, and a Backup whose
// record a listing of its whole copy on the target made or completed.
const (
	ReasonPlatformRestored = "PlatformRestored"
	ReasonListFailed       = "ListFailed"
	ReasonFoundOnTarget    = "FoundOnTarget"
)

// BackupSlotName is the Backup a schedule creates for slot: <schedule>-<slot in Unix minutes> (Decision 3).
func BackupSlotName(schedule ObjectName, slot time.Time) ObjectName {
	return ObjectName(string(schedule) + "-" + strconv.FormatInt(slot.Unix()/60, 10))
}

// ParseBackupSlotName is BackupSlotName's inverse: the slot of name when it is <schedule>-<Unix minutes>, written as
// BackupSlotName writes it (ADR-0287).
func ParseBackupSlotName(schedule, name ObjectName) (time.Time, bool) {
	rest, ok := strings.CutPrefix(string(name), string(schedule)+"-")
	if !ok {
		return time.Time{}, false
	}
	minutes, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || minutes < 0 || strconv.FormatInt(minutes, 10) != rest {
		return time.Time{}, false
	}
	return time.Unix(minutes*60, 0).UTC(), true
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
	names := len(s.KVStores) + len(s.Buckets) + len(s.Lakes) + len(s.Databases) + len(s.Volumes)
	forms := 0
	for _, set := range []bool{names > 0, s.App != "", s.ResourceGroup != ""} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return fault.Invalidf(op, "spec.scope sets %d forms: want one of kvStores, buckets, lakes, databases and volumes, app, or resourceGroup", forms)
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
	seenVol := make(map[string]bool, len(s.Volumes))
	for i, v := range s.Volumes {
		fn, name, ok := strings.Cut(v, "/")
		if !ok || !dnsLabel.MatchString(fn) || !dnsLabel.MatchString(name) {
			return fault.Invalidf(op, "spec.scope.volumes[%d] %q: want <function>/<name>, both DNS-1123 labels", i, v)
		}
		if seenVol[v] {
			return fault.Invalidf(op, "spec.scope.volumes[%d] repeats %q", i, v)
		}
		seenVol[v] = true
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
// each member a KVStore, Bucket, Lake, SQLDatabase or Function volume, once, with exactly one target of its kind.
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
	seen := make(map[restoreKey]string, len(r.Spec.Members))
	for i, m := range r.Spec.Members {
		path := fmt.Sprintf("spec.members[%d]", i)
		if err := m.validate(op, path); err != nil {
			return err
		}
		k := restoreKey{ObjectRef{Kind: m.Kind, Name: m.Name}, m.Volume}
		if prev, dup := seen[k]; dup {
			return fault.Invalidf(op, "%s repeats %s/%s of %s", path, m.Kind, m.Name, prev)
		}
		seen[k] = path
	}
	// A Lake's copy on a prefix no Lake owns would go with the Bucket's emptying, from a Succeeded Restore (ADR-0312 Decision 6).
	for i, m := range r.Spec.Members {
		if m.AsLocation == nil {
			continue
		}
		if slices.ContainsFunc(r.Spec.Members, func(b RestoreMember) bool { return b.Kind == KindBucket && b.InPlace && b.Name == m.AsLocation.Bucket }) {
			return fault.Invalidf(op, "spec.members[%d]: asLocation is on Bucket %q, which this Restore restores in place: restore them apart", i, m.AsLocation.Bucket)
		}
	}
	return nil
}

// restoreKey is a member's identity: its object, and for a Function its volume.
type restoreKey struct {
	ObjectRef
	volume ObjectName
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
	if m.Volume != "" && m.Kind != KindFunction {
		return fault.Invalidf(op, "%s.volume applies to kind Function: a %s takes no volume", path, m.Kind)
	}
	switch m.Kind {
	case KindFunction:
		if m.Volume == "" || !dnsLabel.MatchString(string(m.Volume)) {
			return fault.Invalidf(op, "%s.volume %q: a Function member names its local volume, a DNS-1123 label", path, m.Volume)
		}
		if m.AsLocation != nil {
			return fault.Invalidf(op, "%s.asLocation applies to a Lake: a Function volume takes as or inPlace", path)
		}
		if m.As != "" && !dnsLabel.MatchString(string(m.As)) {
			return fault.Invalidf(op, "%s.as %q is not a valid DNS-1123 label", path, m.As)
		}
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
		return fault.Invalidf(op, "%s.kind %q: want KVStore, Bucket, Lake, SQLDatabase or Function", path, m.Kind)
	}
	return nil
}
