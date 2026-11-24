// Package multinode is funcd's multi-node control plane (FEAT-0002): the node API that worker nodes dial, the cluster
// CA and the join tokens, the placement of namespaces on nodes, the nodes' liveness, and the node-call queue. The
// control plane never dials a node: a node joins with a one-time token, then long-polls the change log of the
// namespaces placed on it, writes their status back and answers the node calls, all over mTLS.
package multinode

import (
	"encoding/json"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/store"
)

// The node API's version handshake (FEAT-0002 decision 12): a node sends the version it speaks on every request, and the
// control plane serves the versions in SupportedAPIVersions.
const (
	APIVersion       = 1
	HeaderAPIVersion = "Funcd-Node-Api"
	HeaderVersion    = "Funcd-Node-Version"
	HeaderPlatform   = "Funcd-Node-Platform"
)

// SupportedAPIVersions are the node API versions this control plane serves: the current one, and the previous one once
// there is one.
var SupportedAPIVersions = []int{APIVersion} //nolint:gochecknoglobals // a constant list

// The node API's paths.
const (
	PathJoin     = "/node/v1/join"
	PathSnapshot = "/node/v1/snapshot"
	PathFeed     = "/node/v1/feed"
	PathStatus   = "/node/v1/status"
	PathObjects  = "/node/v1/objects"
	PathCalls    = "/node/v1/calls"
	PathKeys     = "/node/v1/keys"
	PathReport   = "/node/v1/report"
	PathSecrets  = "/node/v1/secrets"
	PathChildren = "/node/v1/children"
	PathRenew    = "/node/v1/renew"
)

// JoinRequest is a node's join: its one-time token and a CSR for its node certificate.
type JoinRequest struct {
	Token    string `json:"token"`
	CSR      []byte `json:"csr"`
	Version  string `json:"version,omitempty"`
	Platform string `json:"platform,omitempty"`
}

// JoinResponse carries the node certificate and the cluster CA, both PEM.
type JoinResponse struct {
	Certificate []byte `json:"certificate"`
	CA          []byte `json:"ca"`
}

// RenewRequest asks for a new certificate in place of the node's own, answered as the join is.
type RenewRequest struct {
	CSR []byte `json:"csr"`
}

// Snapshot is every object of a node's namespaces at Rev, the Namespace objects included; a node follows the feed after
// Rev. Objects a node owns (children, with ownerReferences) are left out: the node holds them already.
type Snapshot struct {
	Rev      uint64         `json:"rev"`
	Timeline string         `json:"timeline"`
	Objects  []store.Change `json:"objects"`
}

// Feed is a long poll's answer: the node's records after the asked revision, and the revision to resume after.
type Feed struct {
	Rev     uint64         `json:"rev"`
	Changes []store.Change `json:"changes,omitempty"`
}

// StatusWrite is a node's status write-back of one object it runs (FEAT-0002 decision 4), the one of that uid.
type StatusWrite struct {
	Kind      v1.Kind          `json:"kind"`
	Namespace v1.NamespaceName `json:"namespace"`
	Name      v1.ObjectName    `json:"name"`
	UID       v1.UID           `json:"uid"`
	Status    json.RawMessage  `json:"status"`
}

// ObjectWrite is a node's write of a child it owns: an object with ownerReferences, such as a Revision.
type ObjectWrite struct {
	Kind   v1.Kind         `json:"kind"`
	Object json.RawMessage `json:"object"`
}

// NodeReport is what a node reports of itself into its WorkerNode's status: the address of its blob store, and its
// platform backup (F251), absent while it runs none.
type NodeReport struct {
	BlobStore string         `json:"blobStore,omitempty"`
	Backup    *v1.NodeBackup `json:"backup,omitempty"`
}

// SecretWrite is a workload's write of one key of a Secret it binds (ADR-0327), in a namespace placed on the node; a nil
// Value removes the key.
type SecretWrite struct {
	Namespace v1.NamespaceName `json:"namespace"`
	Workload  v1.WorkloadRef   `json:"workload"`
	Secret    v1.ObjectName    `json:"secret"`
	Key       string           `json:"key"`
	Value     *string          `json:"value,omitempty"`
}

// SecretAnswer is the Secret's generation after the write, or the error it met and its kind.
type SecretAnswer struct {
	Generation int64      `json:"generation,omitempty"`
	Error      string     `json:"error,omitempty"`
	Fault      fault.Kind `json:"fault,omitempty"`
}

// The node call kinds (FEAT-0002 decision 9).
const (
	CallLogs              = "logs"
	CallRunLogs           = "runLogs"
	CallDeadLetters       = "deadLetters"
	CallDeadLetter        = "deadLetter"
	CallDiscardDeadLetter = "discardDeadLetter"
	CallReplayDeadLetter  = "replayDeadLetter"
	CallAppRetry          = "appRetry"
	CallVolumeDelete      = "volumeDelete"
	CallAdmit             = "admit"
	CallReleaseNamespaces = "releaseNamespaces"
	CallReturnQuarantined = "returnQuarantined"
	CallHoldStatus        = "holdStatus"
	CallReleaseHold       = "releaseHold"
	CallBackupStatus      = "backupStatus"
)

// OfTheNode reports whether a call of kind is about the node itself rather than a namespace it runs.
func OfTheNode(kind string) bool {
	return kind == CallHoldStatus || kind == CallReleaseHold || kind == CallBackupStatus
}

// Call is one node call the control plane queued for a node; Namespace is empty in one about the node itself.
type Call struct {
	ID        string           `json:"id"`
	Kind      string           `json:"kind"`
	Namespace v1.NamespaceName `json:"namespace,omitempty"`
	Run       v1.ObjectName    `json:"run,omitempty"`
	Name      string           `json:"name,omitempty"`   // a dead letter's ID, an App's or a Function's name
	Volume    v1.ObjectName    `json:"volume,omitempty"` // a Function's volume
	Body      json.RawMessage  `json:"body,omitempty"`   // a command's input, such as an admission's request
	Query     logread.Query    `json:"query"`
}

// Answer is a node's answer to a Call: the log lines or the body of a command, or the error it met and its kind, so
// the control plane answers as the node's own API would.
type Answer struct {
	Lines []logread.Line  `json:"lines"`
	Body  json.RawMessage `json:"body,omitempty"`
	Error string          `json:"error,omitempty"`
	Fault fault.Kind      `json:"fault,omitempty"`
}

// NamespaceKey is a namespace's master for the credentials derived on its node (FEAT-0002 decision 8), at Version.
type NamespaceKey struct {
	Namespace v1.NamespaceName `json:"namespace"`
	Version   int              `json:"version"`
	Key       []byte           `json:"key"`
}
