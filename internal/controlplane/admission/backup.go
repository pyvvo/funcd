package admission

import (
	"context"
	"reflect"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// --- backup-schedule (ADR-0222 Decision 6): Create and Update on BackupSchedule ---------------------------------

type backupSchedule struct{ storeURL string }

// NewBackupScheduleAdmission returns the Validating admission of a BackupSchedule's recipients and target: the
// recipients follow ADR-0204 Decision 2's rules (envelope.Parse), and a target inside the blob store, storeURL
// (blob.target or the file:// URL of blob.dir), is refused naming spec.target.url. A target on the store's provider is
// stored; the schedule's Ready reason says SameProvider.
func NewBackupScheduleAdmission(storeURL string) Admission { return backupSchedule{storeURL: storeURL} }

func (backupSchedule) Name() string { return "backup-schedule" }
func (backupSchedule) Phase() Phase { return Validating }

func (backupSchedule) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindBackupSchedule.GVK() && (op == Create || op == Update)
}

func (a backupSchedule) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.backup-schedule"
	s, ok := req.Object.(*v1.BackupSchedule)
	if !ok {
		return req.Object, nil
	}
	if _, err := envelope.Parse(s.Spec.Encryption.Recipients); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "spec.encryption.recipients")
	}
	overlap, err := targetOverlap(a.storeURL, s.Spec.Target.URL)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, op, "spec.target.url")
	}
	if overlap == gocloud.OverlapStore {
		return nil, fault.Invalidf(op, "spec.target.url %q is inside the blob store it would copy: a store failure "+
			"would take the backup with it; name another bucket", s.Spec.Target.URL)
	}
	return req.Object, nil
}

// targetOverlap compares a backup target with the blob store, storeURL; an empty storeURL overlaps nothing.
func targetOverlap(storeURL, target string) (gocloud.Overlap, error) {
	if storeURL == "" {
		return gocloud.OverlapNone, nil
	}
	return gocloud.CompareDomains(storeURL, target)
}

// --- backup-spec-immutable (ADR-0222 Decision 1): Update on Backup and Restore ---------------------------------

type backupSpecImmutable struct{}

// NewBackupSpecImmutableAdmission returns the Validating admission that refuses a change to a Backup's or a Restore's
// spec: a run record and a request are fixed at creation, as a Site's prefix is.
func NewBackupSpecImmutableAdmission() Admission { return backupSpecImmutable{} }

func (backupSpecImmutable) Name() string { return "backup-spec-immutable" }
func (backupSpecImmutable) Phase() Phase { return Validating }

func (backupSpecImmutable) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return (gvk == v1.KindBackup.GVK() || gvk == v1.KindRestore.GVK()) && op == Update
}

func (backupSpecImmutable) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.backup-spec-immutable"
	same := true
	switch o := req.Object.(type) {
	case *v1.Backup:
		if old, ok := req.Old.(*v1.Backup); ok {
			same = reflect.DeepEqual(o.Spec, old.Spec)
		}
	case *v1.Restore:
		if old, ok := req.Old.(*v1.Restore); ok {
			same = reflect.DeepEqual(o.Spec, old.Spec)
		}
	}
	if !same {
		return nil, fault.Invalidf(op, "%s spec is immutable: create another one", req.GVK.Kind)
	}
	return req.Object, nil
}

// --- backup-ttl (ADR-0222 Decision 3): Create on Backup ---------------------------------------------------------

type backupTTL struct{ r StoreReader }

// NewBackupTTLAdmission returns the Validating admission that refuses a Backup whose spec.ttl exceeds its schedule's
// ttl. Without the schedule it admits: the run fails ScheduleNotFound (no link-validity admission, ADR-0121).
func NewBackupTTLAdmission(r StoreReader) Admission { return backupTTL{r: r} }

func (backupTTL) Name() string { return "backup-ttl" }
func (backupTTL) Phase() Phase { return Validating }

func (backupTTL) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindBackup.GVK() && op == Create
}

func (a backupTTL) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.backup-ttl"
	b, ok := req.Object.(*v1.Backup)
	if !ok || b.Spec.TTL == 0 {
		return req.Object, nil
	}
	objs, err := a.r.List(ctx, v1.KindBackupSchedule.GVK(), b.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the schedules of %q", b.Namespace)
	}
	for _, o := range objs {
		s, ok := o.(*v1.BackupSchedule)
		if ok && s.Name == b.Spec.Schedule && b.Spec.TTL > s.Spec.EffectiveTTL() {
			return nil, fault.Invalidf(op, "spec.ttl %s exceeds the ttl %s of schedule %q", b.Spec.TTL, s.Spec.EffectiveTTL(), s.Name)
		}
	}
	return req.Object, nil
}

// --- restore-in-place (ADR-0222 Decisions 7, 10): Create and Delete on Restore ----------------------------------

type restoreInPlace struct {
	authz auth.Authorizer
	r     StoreReader
}

// NewRestoreInPlaceAdmission returns the Validating admission of a Restore with an inPlace member: its create needs
// update on the Namespace (the built-in RBAC gives it to an admin only) and is refused while another in-place Restore
// of the namespace has not ended, since they would share the namespace's restore hold; its delete is refused until it
// has ended, so the hold keeps the Restore that names it. It reads Request.Old on a delete.
func NewRestoreInPlaceAdmission(authz auth.Authorizer, r StoreReader) Admission {
	return restoreInPlace{authz: authz, r: r}
}

func (restoreInPlace) Name() string { return "restore-in-place" }
func (restoreInPlace) Phase() Phase { return Validating }

// ReadsNamespace: the one-active check reads the namespace's Restores (ADR-0147).
func (restoreInPlace) ReadsNamespace() bool { return true }

func (restoreInPlace) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindRestore.GVK() && (op == Create || op == Delete)
}

func (a restoreInPlace) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.restore-in-place"
	if req.Operation == Delete {
		if old, ok := req.Old.(*v1.Restore); ok && old.InPlace() && !old.Ended() {
			return nil, fault.Conflictf(op, "in-place Restore %s of namespace %q has not ended: delete it once it has",
				old.Name, old.Namespace)
		}
		return req.Old, nil
	}
	r, ok := req.Object.(*v1.Restore)
	if !ok || !r.InPlace() {
		return req.Object, nil
	}
	dec, err := a.authz.Authorize(ctx, auth.Request{Identity: req.Identity, Verb: auth.VerbUpdate, Kind: v1.KindNamespace})
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "authorize update on Namespace %q", r.Namespace)
	}
	if !dec.Allowed {
		return nil, fault.Forbiddenf(op, "an in-place Restore needs update on Namespace %q: %s", r.Namespace, dec.Reason)
	}
	objs, err := a.r.List(ctx, v1.KindRestore.GVK(), r.Namespace)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "list the Restores of %q", r.Namespace)
	}
	for _, o := range objs {
		if other, ok := o.(*v1.Restore); ok && other.Name != r.Name && other.InPlace() && !other.Ended() {
			return nil, fault.Conflictf(op, "in-place Restore %s of namespace %q has not ended: create this one once it has",
				other.Name, r.Namespace)
		}
	}
	return req.Object, nil
}
