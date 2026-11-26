// Package nodeapi is the node API port (FEAT-0002): the link between a control plane and its worker nodes. A node opens
// every connection, and the control plane never dials one. Over it the node takes a snapshot of the namespaces placed on
// it, follows the change log after that snapshot, writes back the status and the children it makes, and answers the calls
// the control plane sends it.
//
// The rules of each request live once, in the control plane's Handler; a driver only carries them, with their errors'
// api/fault kinds. The port imports no transport library. Two drivers implement it: the HTTPS long poll of
// internal/nodeapi/longpoll (FEAT-0002 decisions 3 and 9), the default, and core NATS on an embedded server, with no
// JetStream (internal/nodeapi/nats). Each is the production and the test driver alike, as the gateway's embedded driver
// is (ADR-0012), and both run the same contract suite (nodeapicontract).
package nodeapi

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/store"
)

// APIVersion is the node API version this release speaks (FEAT-0002 decision 12): a node sends it with every request.
const APIVersion = 1

// SupportedAPIVersions are the node API versions a control plane serves: the current one, and the previous one once
// there is one.
var SupportedAPIVersions = []int{APIVersion} //nolint:gochecknoglobals // a constant list

// Supported reports whether a control plane serves node API version v.
func Supported(v int) bool { return slices.Contains(SupportedAPIVersions, v) }

// The wire's bounds, the same on every driver: a long poll's wait, the bodies of the requests, and the time a node gets
// to answer a call.
const (
	DefaultWait   = 25 * time.Second
	MaxWait       = 60 * time.Second
	MaxJoinBody   = 64 << 10
	MaxStatusBody = 1 << 20
	MaxObjectBody = 4 << 20
	CallTimeout   = 10 * time.Second
)

// Wait is a long poll's wait: wait itself when it is above zero and at most MaxWait, else DefaultWait.
func Wait(wait time.Duration) time.Duration {
	if wait <= 0 || wait > MaxWait {
		return DefaultWait
	}
	return wait
}

// WaitForChanges is h's Changes for node after since as soon as there is one, or none once wait passes: the feed's long
// poll, which every driver serves the same way.
func WaitForChanges(ctx context.Context, h Handler, node v1.ObjectName, since uint64, timeline string, wait time.Duration) (Feed, error) {
	deadline := time.Now().Add(Wait(wait))
	for {
		changed := h.Changed(node)
		f, err := h.Changes(ctx, node, since, timeline)
		if err != nil || len(f.Changes) > 0 || !time.Now().Before(deadline) {
			return f, err
		}
		since = f.Rev
		t := time.NewTimer(time.Until(deadline))
		select {
		case <-changed:
			t.Stop()
		case <-t.C:
			return f, nil
		case <-ctx.Done():
			t.Stop()
			return Feed{}, ctx.Err()
		}
	}
}

// AnswerError is the error of a node's answer a, with its kind (fault.Unavailable when it names none), as the control
// plane's caller gets it; nil when a holds none.
func AnswerError(op string, node v1.ObjectName, a Answer) error {
	if a.Error == "" {
		return nil
	}
	return fault.Wrapf(errors.New(a.Error), cmp.Or(a.Fault, fault.Unavailable), op, "worker node %s answered: %s", node, a.Error)
}

// PinnedTLS is the TLS a node joins with, before it holds a certificate: it trusts the control plane at host only when
// the chain the control plane presents holds a CA that trusted accepts, and verifies that chain against that CA for host,
// so a join token never reaches an impostor. ca is that CA's DER once a handshake found it.
func PinnedTLS(host string, trusted func(caDER []byte) bool) (cfg *tls.Config, ca func() []byte) {
	var (
		mu    sync.Mutex
		found []byte
	)
	cfg = &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, //nolint:gosec // VerifyPeerCertificate pins the CA that trusted accepts, below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			for _, der := range raw {
				if trusted(der) {
					if err := verifyChain(raw, der, host); err != nil {
						return err
					}
					mu.Lock()
					found = der
					mu.Unlock()
					return nil
				}
			}
			return fmt.Errorf("the control plane at %s does not present the CA the join token names", host)
		},
	}
	return cfg, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return found
	}
}

// verifyChain checks the presented leaf against the pinned CA, for host.
func verifyChain(raw [][]byte, caDER []byte, host string) error {
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(raw[0])
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: host, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// Schemes is a Connector that hands each join and each client to the driver its control plane's URL names by its scheme,
// such as https for the HTTPS long poll and nats for NATS.
type Schemes map[string]Connector

func (s Schemes) Join(ctx context.Context, server string, trusted func(caDER []byte) bool, in JoinRequest) (JoinResponse, []byte, error) {
	c, err := s.of(server)
	if err != nil {
		return JoinResponse{}, nil, err
	}
	return c.Join(ctx, server, trusted, in)
}

func (s Schemes) Dial(id Identity) (Client, error) {
	c, err := s.of(id.Server)
	if err != nil {
		return nil, err
	}
	return c.Dial(id)
}

func (s Schemes) of(server string) (Connector, error) {
	u, err := url.Parse(server)
	if err == nil {
		if c, ok := s[u.Scheme]; ok {
			return c, nil
		}
	}
	schemes := make([]string, 0, len(s))
	for k := range s {
		schemes = append(schemes, k+"://")
	}
	sort.Strings(schemes)
	return nil, fault.Invalidf("nodeapi.Schemes", "the control plane's node API is an %s URL, not %q", strings.Join(schemes, " or "), server)
}

// Handler is the control plane's side of each request: the rules of the node API. A driver calls Admit for every request
// of a joined node, then the request's own method as the node Admit let in. Errors are api/fault kinds, which reach the
// node as they are.
type Handler interface {
	// Join swaps a one-time join token for a node certificate (decision 2): the one request of a node that holds none.
	Join(ctx context.Context, in JoinRequest) (JoinResponse, error)
	// Admit lets p's request in: fault.Unauthorized for a node that is not declared or a certificate that is not its
	// current one, fault.Conflict for a release newer than the control plane's (decision 12). It records the contact.
	Admit(ctx context.Context, p Peer) error
	// Renew signs a new certificate for node's key; the node's certificate stays its own until a request under the new one.
	Renew(ctx context.Context, node v1.ObjectName, in RenewRequest) (JoinResponse, error)
	// Snapshot is every object of the namespaces placed on node, or of namespace ns alone, at the revision its feed
	// follows after.
	Snapshot(ctx context.Context, node v1.ObjectName, ns v1.NamespaceName) (Snapshot, error)
	// Changes is node's records after since, of the change log's timeline, and none while there is none: a since older
	// than the change log, or of another timeline, is fault.PreconditionFailed, after which the node takes a snapshot.
	Changes(ctx context.Context, node v1.ObjectName, since uint64, timeline string) (Feed, error)
	// Changed is closed at the next record of the change log that concerns node, which a driver waits for to ask Changes
	// again; a record that concerns another node leaves it open.
	Changed(node v1.ObjectName) <-chan struct{}
	// Children are the children node made in its namespaces, as the control plane holds them.
	Children(ctx context.Context, node v1.ObjectName) ([]store.Change, error)
	// Status writes node's status of the object in.UID names.
	Status(ctx context.Context, node v1.ObjectName, in StatusWrite) error
	// PutObject writes a child node owns, or an object it made with no owner.
	PutObject(ctx context.Context, node v1.ObjectName, in ObjectWrite) error
	// DeleteObject deletes an object node deleted, the one of in.UID.
	DeleteObject(ctx context.Context, node v1.ObjectName, in ObjectRef) error
	// Report writes what node reports of itself into its WorkerNode's status.
	Report(ctx context.Context, node v1.ObjectName, in NodeReport) error
	// Key is the master of namespace ns, which must be placed on node (decision 8).
	Key(ctx context.Context, node v1.ObjectName, ns v1.NamespaceName) (NamespaceKey, error)
	// Secret makes a workload's write of one key of a Secret, as the control plane's own workloads' are.
	Secret(ctx context.Context, node v1.ObjectName, in SecretWrite) (SecretAnswer, error)
}

// Server is a driver's control-plane side: it carries the nodes' requests to a Handler, and the control plane's calls to
// the nodes.
type Server interface {
	// Serve hands each request of the nodes to h until ctx ends.
	Serve(ctx context.Context, h Handler) error
	// Call sends c to node and waits for the node's answer, a bounded time: fault.Unavailable at once for a node that is
	// offline, and an answer's error with its kind.
	Call(ctx context.Context, node v1.ObjectName, c Call) (Answer, error)
	// Addr is where the nodes reach the control plane.
	Addr() string
	// Close stops serving.
	Close() error
}

// Client is a driver's node side, under the node's certificate: a method per request of the Handler, the call queue, and
// the use of a renewed certificate. Errors are the Handler's, with their kinds.
type Client interface {
	Snapshot(ctx context.Context, ns v1.NamespaceName) (Snapshot, error)
	// Feed is Changes once a record comes, or none once wait passes.
	Feed(ctx context.Context, since uint64, timeline string, wait time.Duration) (Feed, error)
	Children(ctx context.Context) ([]store.Change, error)
	Status(ctx context.Context, in StatusWrite) error
	PutObject(ctx context.Context, in ObjectWrite) error
	DeleteObject(ctx context.Context, in ObjectRef) error
	Report(ctx context.Context, in NodeReport) error
	Key(ctx context.Context, ns v1.NamespaceName) (NamespaceKey, error)
	Secret(ctx context.Context, in SecretWrite) (SecretAnswer, error)
	Renew(ctx context.Context, in RenewRequest) (JoinResponse, error)
	// Renewed is a node's first request under its renewed certificate, which makes it the node's certificate.
	Renewed(ctx context.Context) error
	// Calls are the calls the control plane sent the node, once one comes, or none once wait passes.
	Calls(ctx context.Context, wait time.Duration) ([]Call, error)
	// Answer answers the call id.
	Answer(ctx context.Context, id string, a Answer) error
	// Close lets the client's connections go; requests under way end on them.
	Close() error
}

// Connector is a driver's node side before a client: the join, then a client under the certificate it brought.
type Connector interface {
	// Join sends in to the control plane at server. Before it sends the token, it finds in the chain the control plane
	// presents the CA that trusted accepts, and verifies the chain against it, so a token never reaches an impostor. It
	// returns the answer and that CA's DER.
	Join(ctx context.Context, server string, trusted func(caDER []byte) bool, in JoinRequest) (JoinResponse, []byte, error)
	// Dial makes the client of id.
	Dial(id Identity) (Client, error)
}

// Peer is who sent a request: the node its certificate names, by the certificate's serial in hex, and what the node
// reports of itself.
type Peer struct {
	Node       v1.ObjectName
	Serial     string
	APIVersion int
	Version    string
	Platform   string
}

// Identity is a joined node's: the control plane it reaches, its certificate with its key, the cluster CA it trusts, and
// the release and platform it reports.
type Identity struct {
	Server      string
	Certificate tls.Certificate
	Roots       *x509.CertPool
	Version     string
	Platform    string
}

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

// Feed is the node's records after the asked revision, and the revision to resume after.
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

// ObjectRef names the object a node deleted, by its uid, so a delete never takes another object of its name.
type ObjectRef struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	UID       v1.UID
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

// Call is one node call the control plane sent a node; Namespace is empty in one about the node itself.
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
