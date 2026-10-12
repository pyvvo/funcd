// Package sql is the SQL provider (ADR-0232, FEAT-0011): the SQLDatabase reconciler, which deploys one rqlite engine
// per database through the add-on runtime with durable storage (ADR-0231), the engine password, the in-process
// registry of Ready engines, and the engine client. Imported as sqlsvc.
package sql

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"sync"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// EnginePassword derives a database's engine password from the node master (ADR-0232 Decision 5): HMAC-SHA256 over
// "funcd-sql-engine/<ns>/<name>", base64url. Nothing stores it; a master rotation changes it at the next engine start.
func EnginePassword(master []byte, ns v1.NamespaceName, name v1.ObjectName) string {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("funcd-sql-engine/" + string(ns) + "/" + string(name)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Engines is the in-process registry of Ready engines' addresses (ADR-0232 Decision 6), filled by the reconciler
// and read by the deletion probe and the access facade. Safe for concurrent use.
type Engines struct {
	mu    sync.RWMutex
	addrs map[string]string
}

// NewEngines returns an empty registry.
func NewEngines() *Engines { return &Engines{addrs: map[string]string{}} }

func engineKey(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name)
}

// Set records a Ready engine's address.
func (e *Engines) Set(ns v1.NamespaceName, name v1.ObjectName, addr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addrs[engineKey(ns, name)] = addr
}

// Delete forgets an engine that is not Ready or is deleted.
func (e *Engines) Delete(ns v1.NamespaceName, name v1.ObjectName) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.addrs, engineKey(ns, name))
}

// Address returns a Ready engine's address.
func (e *Engines) Address(ns v1.NamespaceName, name v1.ObjectName) (string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	a, ok := e.addrs[engineKey(ns, name)]
	return a, ok
}
