package cedar

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"
	cedartypes "github.com/cedar-policy/cedar-go/types"
	"github.com/cedar-policy/cedar-go/x/exp/ast"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// FunctionGrants lists, per Function of namespace ns that a statement of Policy name references anywhere (its scope or
// its conditions), one grant per such statement: "policy/<ns>/<name>#<j>@<sha256 of the statement>", where
// "<ns>/<name>#<j>" is the ID the PDP compiles that statement under. A grant names what it grants, never its
// principal, so Functions referenced by one statement share it.
func FunctionGrants(ns v1.NamespaceName, name v1.ObjectName, text string) (map[v1.ObjectName][]string, error) {
	list, err := cedar.NewPolicyListFromBytes(string(name), []byte(text))
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, "cedar.FunctionGrants", "parse Policy %q/%q", ns, name)
	}
	out := map[v1.ObjectName][]string{}
	for j, pol := range list {
		refs := map[v1.ObjectName]bool{}
		walkPolicy((*ast.Policy)(pol.AST()), func(uid cedartypes.EntityUID) {
			if fns, fn, ok := functionOf(uid); ok && fns == ns {
				refs[fn] = true
			}
		})
		if len(refs) == 0 {
			continue
		}
		sum := sha256.Sum256(pol.MarshalCedar())
		grant := fmt.Sprintf("policy/%s/%s#%d@%s", ns, name, j, hex.EncodeToString(sum[:]))
		for fn := range refs {
			out[fn] = append(out[fn], grant)
		}
	}
	return out, nil
}

// functionOf splits a Function entity UID into its namespace and name.
func functionOf(uid cedartypes.EntityUID) (v1.NamespaceName, v1.ObjectName, bool) {
	if uid.Type != entityTypeFunction {
		return "", "", false
	}
	ns, name, ok := strings.Cut(string(uid.ID), "/")
	return v1.NamespaceName(ns), v1.ObjectName(name), ok
}

// walkPolicy calls visit on every entity UID in p's scope and conditions.
func walkPolicy(p *ast.Policy, visit func(cedartypes.EntityUID)) {
	visitScope(p.Principal, visit)
	visitScope(p.Action, visit)
	visitScope(p.Resource, visit)
	for _, c := range p.Conditions {
		ast.Inspect(ast.NewNode(c.Body), func(n ast.IsNode) bool {
			if v, ok := n.(ast.NodeValue); ok {
				visitValue(v.Value, visit)
			}
			return true
		})
	}
}

// visitScope calls visit on the entity UIDs a scope names.
func visitScope(scope ast.IsScopeNode, visit func(cedartypes.EntityUID)) {
	switch s := scope.(type) {
	case ast.ScopeTypeEq:
		visit(s.Entity)
	case ast.ScopeTypeIn:
		visit(s.Entity)
	case ast.ScopeTypeIsIn:
		visit(s.Entity)
	case ast.ScopeTypeInSet:
		for _, e := range s.Entities {
			visit(e)
		}
	}
}

// visitValue calls visit on every entity UID in v, inside sets and records too.
func visitValue(v cedartypes.Value, visit func(cedartypes.EntityUID)) {
	switch t := v.(type) {
	case cedartypes.EntityUID:
		visit(t)
	case cedartypes.Set:
		for e := range t.All() {
			visitValue(e, visit)
		}
	case cedartypes.Record:
		for _, e := range t.All() {
			visitValue(e, visit)
		}
	}
}
