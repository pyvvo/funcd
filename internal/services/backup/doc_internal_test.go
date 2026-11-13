package backup

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The descriptions of BackupSchedule, Backup and Restore name these reasons and Secret keys, so a renamed one fails
// here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	keys := []string{keyAccessKeyID, keySecretAccessKey, keySessionToken}
	doctag.Names(t, reflect.TypeFor[v1.BackupScheduleSpec](), "Scope", ReasonMemberNotFound)
	doctag.Names(t, reflect.TypeFor[v1.BackupScope](), "Databases", ReasonEngineNotReady)
	doctag.Names(t, reflect.TypeFor[v1.BackupTarget](), "URL", ReasonSameProvider, ReasonTargetUnconditional)
	doctag.Names(t, reflect.TypeFor[v1.BackupScope](), "Lakes", ReasonLakeUnstable)
	doctag.Names(t, reflect.TypeFor[v1.BackupTarget](), "CredentialsSecret", keys...)
	doctag.Names(t, reflect.TypeFor[v1.BackupSpec](), "Schedule", ReasonScheduleNotFound)
	doctag.Names(t, reflect.TypeFor[v1.BackupStatus](), "Phase", ReasonInterrupted)
	doctag.Names(t, reflect.TypeFor[v1.RestoreStatus](), "Phase", ReasonInterrupted)
	doctag.Names(t, reflect.TypeFor[v1.RestoreSpec](), "Backup", reasonBackupNotReady)
	doctag.Names(t, reflect.TypeFor[v1.RestoreMember](), "Name", ReasonMemberNotFound)
	doctag.Names(t, reflect.TypeFor[v1.RestoreSource](), "CredentialsSecret", keys...)
	doctag.Names(t, reflect.TypeFor[v1.RestoreSource](), "IdentitySecret", keyAgeIdentity)
}
