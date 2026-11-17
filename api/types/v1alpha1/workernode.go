package v1alpha1

import "github.com/pyvvo/funcd/api/fault"

// WorkerNode is a cluster-scoped resource naming a worker node (FEAT-0002). An admin declares it, by name only, before
// the node's machine joins; the control plane writes its status.
type WorkerNode struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Status     WorkerNodeStatus `json:"status,omitempty"`
}

// ConditionNodeReady is True while the node keeps polling the control plane.
const ConditionNodeReady = ConditionType("Ready")

// WorkerNodeStatus is what the control plane observed of the node.
type WorkerNodeStatus struct {
	Status `json:",inline"`
	// Version is the funcd release the node runs, as it reported it.
	Version string `json:"version,omitempty" doc:"The funcd release that the node runs, as it reported it."`
	// NodeAPIVersion is the version of the node API the node speaks.
	NodeAPIVersion int `json:"nodeAPIVersion,omitempty" doc:"The version of the node API that the node speaks."`
	// Platform is the OS and CPU of the node.
	Platform string `json:"platform,omitempty" doc:"The OS and CPU of the node, such as linux/arm64."`
	// LastSeen is when the node last polled the control plane.
	LastSeen Timestamp `json:"lastSeen,omitzero" doc:"When the node last polled the control plane."`
	// Certificate is the node certificate the control plane issued at the join.
	Certificate *NodeCertificate `json:"certificate,omitempty" doc:"The node certificate that the control plane issued when the node joined."`
	// PendingJoin is the one-time join token waiting to be used, as a hash.
	PendingJoin *PendingJoin `json:"pendingJoin,omitempty" doc:"The one-time join token minted for the node and not used yet, kept as a hash."`
	// Backup is the platform backup of the node's own stores, as the node reported it (F251).
	Backup *NodeBackup `json:"backup,omitempty" doc:"The backup of the node's own stores, as the node last reported it; absent while the node runs no backup."`
}

// NodeBackup is a worker node's platform backup, as the node reports it (FEAT-0002 F251).
type NodeBackup struct {
	Held            bool       `json:"held,omitempty" doc:"True while the node is held: its backups wait for the release."`
	RPORisk         bool       `json:"rpoRisk,omitempty" doc:"True when no verified generation of the node is newer than its backup.objectives.rpo."`
	LastSuccessTime *Timestamp `json:"lastSuccessTime,omitempty" doc:"When the node's newest complete generation was written."`
	NextRunTime     *Timestamp `json:"nextRunTime,omitempty" doc:"When the node's next run is due."`
	LastFailure     string     `json:"lastFailure,omitempty" doc:"Why the node's last run failed since the node started; a run that succeeds clears it."`
}

// NodeCertificate identifies the certificate a node joined with.
type NodeCertificate struct {
	Serial   string    `json:"serial" doc:"The certificate's serial number, in hexadecimal. The control plane accepts only this certificate from the node."`
	NotAfter Timestamp `json:"notAfter" doc:"When the certificate expires."`
	IssuedAt Timestamp `json:"issuedAt" doc:"When the node joined and got the certificate."`
}

// PendingJoin is a one-time join token not used yet.
type PendingJoin struct {
	TokenHash string    `json:"tokenHash" doc:"The SHA-256 of the token's secret part, in hexadecimal."`
	ExpiresAt Timestamp `json:"expiresAt" doc:"When the token stops working."`
}

// GroupVersionKind returns the constant GVK for WorkerNode.
func (w *WorkerNode) GroupVersionKind() GroupVersionKind { return KindWorkerNode.GVK() }

// Validate performs envelope validation, and checks that the name is a DNS-1123 label, as a join token and a
// Namespace's spec.node carry it.
func (w *WorkerNode) Validate() error {
	if err := validateMeta(w.TypeMeta, &w.ObjectMeta, KindWorkerNode); err != nil {
		return err
	}
	if !dnsLabel.MatchString(string(w.Name)) {
		return fault.Invalidf("WorkerNode.Validate", "metadata.name %q is not a valid DNS-1123 label", w.Name)
	}
	return nil
}

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (w *WorkerNode) GetStatus() *Status { return &w.Status.Status }
