// Package engineargs builds the rqlited command line and auth file for a SQL database's engine (ADR-0232 Decision 4),
// shared by the container entrypoint (cmd/funcd-rqlite) and the funcdctl dev engine (ADR-0237 Decision 2) so the two
// cannot drift. It imports only the standard library, so the static entrypoint stays small.
package engineargs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// User is the engine user funcd calls the engine as; its password is the database's EnginePassword.
const User = "funcd"

// Args are an engine's addresses and paths.
type Args struct {
	NodeID      string // the database name
	HTTPAddr    string // the address rqlited binds for its HTTP API
	HTTPAdvAddr string // the advertised HTTP address; rqlited refuses an unspecified one
	RaftAddr    string // Raft stays on loopback, so a recreated engine with a new IP keeps its stored address
	AuthFile    string // the auth file, outside the durable directory
	DataDir     string // rqlited's data directory, inside the durable directory
	CDCConfig   string // the change feed's config file (ADR-0235); "" ⇒ no change feed
}

// Argv returns rqlited's arguments, without the program name.
func (a Args) Argv() []string {
	argv := []string{
		"-node-id", a.NodeID,
		"-http-addr", a.HTTPAddr,
		"-http-adv-addr", a.HTTPAdvAddr,
		"-raft-addr", a.RaftAddr,
		"-auth", a.AuthFile,
	}
	if a.CDCConfig != "" {
		// -cdc-as: the high-watermark broadcast needs a user with cdc-hwm-update under auth, else rqlite never prunes
		// its queue of sent changes.
		argv = append(argv, "-cdc-config", a.CDCConfig, "-cdc-as", User)
	}
	return append(argv, a.DataDir)
}

// The change feed's sending bounds (ADR-0235 Decision 1).
const (
	cdcMaxBatch        = 100
	cdcTransmitTimeout = 10 * time.Second
)

// cdcConfig is rqlite's CDC config file; durations are nanoseconds.
type cdcConfig struct {
	Endpoint        string `json:"endpoint"`
	ServiceID       string `json:"service_id"`
	MaxBatchSize    int    `json:"max_batch_size"`
	TransmitTimeout int64  `json:"transmit_timeout"`
}

// CDCConfig returns the change feed's config: every committed change posted to endpoint, with the engine's epoch as
// service_id (ADR-0235 Decision 1).
func CDCConfig(endpoint, epoch string) ([]byte, error) {
	if endpoint == "" || epoch == "" {
		return nil, fmt.Errorf("engineargs: the change feed needs an endpoint and an epoch")
	}
	return json.Marshal(cdcConfig{Endpoint: endpoint, ServiceID: epoch, MaxBatchSize: cdcMaxBatch, TransmitTimeout: int64(cdcTransmitTimeout)})
}

// NewEpoch returns a random 128-bit epoch, which names one engine directory's Raft indexes (ADR-0235 Decision 1).
func NewEpoch() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("engineargs: generate an epoch: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

type credential struct {
	Username string   `json:"username"`
	Password string   `json:"password,omitempty"`
	Perms    []string `json:"perms"`
}

// AuthFile returns the auth file: User with every permission, and anonymous callers with readiness only, so the
// provider runtime's probe needs no credential.
func AuthFile(password string) ([]byte, error) {
	if password == "" {
		return nil, fmt.Errorf("engineargs: empty engine password")
	}
	return json.Marshal([]credential{
		{Username: User, Password: password, Perms: []string{"all"}},
		{Username: "*", Perms: []string{"ready"}},
	})
}
