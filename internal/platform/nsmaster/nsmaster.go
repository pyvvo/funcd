// Package nsmaster picks the master secret a namespace's credentials derive from (ADR-0371): the platform's
// own master on an all-in-one funcd, and on a worker node the namespace's own master, which the control plane derives
// from the platform's and sends only to the node that runs the namespace.
package nsmaster

import v1 "github.com/pyvvo/funcd/api/types/v1alpha1"

// Source returns namespace ns's master, or an error when this process holds none for it.
type Source func(ns v1.NamespaceName) ([]byte, error)

// Of is ns's master: s's when s is set, else master, the platform's own.
func (s Source) Of(master []byte, ns v1.NamespaceName) ([]byte, error) {
	if s == nil {
		return master, nil
	}
	return s(ns)
}
