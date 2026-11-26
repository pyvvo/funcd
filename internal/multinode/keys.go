package multinode

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"strconv"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/store"
)

// NamespaceKeyVersion is the version of the namespace keys this control plane derives; a new version rotates every
// namespace's key at once.
const NamespaceKeyVersion = 1

// NamespaceKeys derives each namespace's master from the control plane's master secret and the namespace's UID
// (FEAT-0002 decision 8). A node gets only the keys of the namespaces placed on it, so a leaked node exposes no other,
// and a namespace keeps its key when it moves to another node.
type NamespaceKeys struct {
	st     store.Store
	master []byte
}

// NewNamespaceKeys derives from master.
func NewNamespaceKeys(st store.Store, master []byte) *NamespaceKeys {
	return &NamespaceKeys{st: st, master: master}
}

// Of is namespace ns's key at NamespaceKeyVersion.
func (k *NamespaceKeys) Of(ctx context.Context, ns v1.NamespaceName) (nodeapi.NamespaceKey, error) {
	obj, err := k.st.Get(ctx, v1.KindNamespace.GVK(), "", v1.ObjectName(ns))
	if err != nil {
		return nodeapi.NamespaceKey{}, err
	}
	uid := obj.GetObjectMeta().UID
	if uid == "" {
		return nodeapi.NamespaceKey{}, fault.Internalf("multinode.NamespaceKeys", "Namespace %s has no UID", ns)
	}
	return nodeapi.NamespaceKey{Namespace: ns, Version: NamespaceKeyVersion, Key: deriveNamespaceKey(k.master, uid, NamespaceKeyVersion)}, nil
}

func deriveNamespaceKey(master []byte, uid v1.UID, version int) []byte {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte("funcd-namespace-key/v" + strconv.Itoa(version) + "/" + string(uid)))
	return mac.Sum(nil)
}
