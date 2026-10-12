// Package engineargs builds the rqlited command line and auth file for a SQL database's engine (ADR-0232 Decision 4),
// shared by the container entrypoint (cmd/funcd-rqlite) and the funcdctl dev engine (ADR-0237 Decision 2) so the two
// cannot drift. It imports only the standard library, so the static entrypoint stays small.
package engineargs

import (
	"encoding/json"
	"fmt"
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
}

// Argv returns rqlited's arguments, without the program name.
func (a Args) Argv() []string {
	return []string{
		"-node-id", a.NodeID,
		"-http-addr", a.HTTPAddr,
		"-http-adv-addr", a.HTTPAdvAddr,
		"-raft-addr", a.RaftAddr,
		"-auth", a.AuthFile,
		a.DataDir,
	}
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
