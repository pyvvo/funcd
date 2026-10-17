package router

import (
	"path"
	"strings"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// defaultBaseDomain is server.baseDomain when unset (ADR-0240 Decision 1).
const defaultBaseDomain = "localhost"

// defaultNamespace has no namespace host: its host-less entries answer on every host but reserved ones.
const defaultNamespace v1.NamespaceName = "default"

func baseOrDefault(base string) string {
	if base == "" {
		return defaultBaseDomain
	}
	return canonicalHost(base)
}

// NamespaceHost is the host a namespace's host-less entries answer on: "<ns>.<base>", or "" for default
// (ADR-0240 Decision 2). A baseDomain of "" means "localhost".
func NamespaceHost(ns v1.NamespaceName, base string) string {
	if ns == "" || ns == defaultNamespace {
		return ""
	}
	return string(ns) + "." + baseOrDefault(base)
}

// NamespaceOfHost is the namespace a host under base names (ADR-0240 Decision 6), compared in canonicalHost's
// spelling: the label just before the base domain. It is empty for the base domain itself, for a host outside it,
// for default and for a label that is not a valid NamespaceName. A baseDomain of "" means "localhost".
func NamespaceOfHost(host, base string) v1.NamespaceName {
	rest, ok := strings.CutSuffix(canonicalHost(host), "."+baseOrDefault(base))
	if !ok || rest == "" {
		return ""
	}
	ns := v1.NamespaceName(rest[strings.LastIndexByte(rest, '.')+1:])
	if ns == defaultNamespace || ns.Validate() != nil {
		return ""
	}
	return ns
}

// IsFunctionPath reports whether a cleaned path is /function or under it, the by-name invoke form.
func IsFunctionPath(p string) bool {
	p = path.Clean(p)
	return p == functionPath || strings.HasPrefix(p, functionPath+"/")
}

// onReservedHost reports whether host is one of reserved or a name under one.
func onReservedHost(host string, reserved map[string]bool) bool {
	for h := host; len(reserved) > 0; {
		if reserved[h] {
			return true
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			return false
		}
		h = h[i+1:]
	}
	return false
}
