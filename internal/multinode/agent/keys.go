package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/platform/durable"
)

// keysDir holds the master of each namespace the node runs, one file each (ADR-0371), so the node derives
// their credentials while the control plane is away.
const keysDir = "keys"

// NamespaceMaster is namespace ns's master on this node, the one the control plane sent for it (ADR-0371):
// the namespace's S3 pairs, lake tokens and SQL engine passwords derive from it, never from the node's own master.
func (a *Agent) NamespaceMaster(ns v1.NamespaceName) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	k, ok := a.keys[ns]
	if !ok {
		return nil, fault.Unavailablef("agent.NamespaceMaster", "worker node %s holds no master for namespace %s yet", a.id.Load().name, ns)
	}
	return k.Key, nil
}

// loadKeys reads back the masters the node kept.
func loadKeys(dir string) (map[v1.NamespaceName]nodeapi.NamespaceKey, error) {
	const op = "agent.loadKeys"
	out := map[v1.NamespaceName]nodeapi.NamespaceKey{}
	entries, err := os.ReadDir(filepath.Join(dir, keysDir))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "read %s", keysDir)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, keysDir, e.Name())) //nolint:gosec // the node's own cluster directory
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "read %s/%s", keysDir, e.Name())
		}
		var k nodeapi.NamespaceKey
		if err := json.Unmarshal(b, &k); err != nil || k.Namespace == "" || len(k.Key) == 0 {
			return nil, fault.Invalidf(op, "%s/%s holds no namespace master", keysDir, e.Name())
		}
		out[k.Namespace] = k
	}
	return out, nil
}

// fetchKey takes namespace ns's master from the control plane and keeps it.
func (a *Agent) fetchKey(ctx context.Context, ns v1.NamespaceName) error {
	const op = "agent.fetchKey"
	k, err := a.client().Key(ctx, ns)
	if err != nil {
		return err
	}
	if k.Namespace != ns || len(k.Key) == 0 {
		return fault.Internalf(op, "the control plane answered no master for namespace %s", ns)
	}
	b, err := json.Marshal(k)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "encode the master of %s", ns)
	}
	dir := filepath.Join(a.cfg.Dir, keysDir)
	if err := (durable.Disk{}).MkdirAll(dir, 0o700); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "create %s", keysDir)
	}
	if err := (durable.Disk{}).WriteFile(dir, string(ns)+".json", b, 0o600); err != nil {
		return fault.Wrapf(err, fault.Internal, op, "keep the master of %s", ns)
	}
	a.mu.Lock()
	a.keys[ns] = k
	a.mu.Unlock()
	return nil
}

// fetchMissingKeys retries the master of each namespace the node runs without one.
func (a *Agent) fetchMissingKeys(ctx context.Context) {
	a.mu.Lock()
	var missing []v1.NamespaceName
	for ns := range a.mine {
		if _, ok := a.keys[ns]; !ok {
			missing = append(missing, ns)
		}
	}
	a.mu.Unlock()
	for _, ns := range missing {
		if err := a.fetchKey(ctx, ns); err != nil {
			a.log.WarnContext(ctx, "node agent: fetch a namespace's master", "namespace", ns, "error", err)
		}
	}
}

// dropKey forgets the master of a namespace that left the node: its file first, so a master gone from memory is gone
// from the disk too.
func (a *Agent) dropKey(ctx context.Context, ns v1.NamespaceName) {
	if err := os.Remove(filepath.Join(a.cfg.Dir, keysDir, string(ns)+".json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		a.log.WarnContext(ctx, "node agent: remove a namespace's master", "namespace", ns, "error", err)
	}
	a.mu.Lock()
	delete(a.keys, ns)
	a.mu.Unlock()
}

// dropKeysOutside drops the master of every namespace that keep does not hold.
func (a *Agent) dropKeysOutside(ctx context.Context, keep map[v1.NamespaceName]bool) {
	a.mu.Lock()
	var stale []v1.NamespaceName
	for ns := range a.keys {
		if !keep[ns] {
			stale = append(stale, ns)
		}
	}
	a.mu.Unlock()
	for _, ns := range stale {
		a.dropKey(ctx, ns)
	}
}
