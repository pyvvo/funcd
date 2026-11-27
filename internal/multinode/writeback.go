package multinode

import (
	"slices"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// childKinds are the kinds a node writes back whole as children it owns (ADR-0366 Decision 1): what its reconcilers
// make with an ownerReference. They are Revisions, AppRevisions and the Invocations of App hooks, the parts of an App
// (its stores, Functions, Jobs, Workflows, EventSources, Sensors, Routes, Sites, Lakes, add-on objects, ConfigMaps and
// BackupSchedules), the Functions of Workflow steps, the stores a Function, a Job or a Workflow owns, the Routes of an
// ingress, and the Secret of an Identity's credential or of an OIDCProvider's client.
func childKinds() []v1.Kind {
	return []v1.Kind{v1.KindRevision, v1.KindAppRevision, v1.KindInvocation, v1.KindFunction, v1.KindJob, v1.KindWorkflow,
		v1.KindEventSource, v1.KindSensor, v1.KindRoute, v1.KindSite, v1.KindLake, v1.KindKVStore, v1.KindBucket,
		v1.KindSQLDatabase, v1.KindAddonObject, v1.KindConfigMap, v1.KindBackupSchedule, v1.KindSecret}
}

// madeKinds are the kinds a node makes with no owner: the Backup records a backup sync finds and the export a Restore
// takes, the WorkflowRuns and the Invocations of a Sensor, and the JobRuns a Function starts. A node writes a new one
// back whole, then its status alone, as it does of one that a person made.
func madeKinds() []v1.Kind {
	return []v1.Kind{v1.KindBackup, v1.KindWorkflowRun, v1.KindInvocation, v1.KindJobRun}
}

// WrittenBack reports whether a node writes an object of kind back whole, owned or not, rather than its status alone.
// No other kind comes from a node, so a node cannot write a Policy, a Role, a RolesAssignment or an Identity.
func WrittenBack(kind v1.Kind, owned bool) bool {
	if owned {
		return slices.Contains(childKinds(), kind)
	}
	return slices.Contains(madeKinds(), kind)
}
