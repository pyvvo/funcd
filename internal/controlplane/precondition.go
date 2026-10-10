package controlplane

import (
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

const preconditionOp = "controlplane.precondition"

// IfMatchParams is embedded in every PUT and DELETE input (ADR-0210 Decision 1). It is exported because huma skips
// unexported embedded fields.
type IfMatchParams struct {
	IfMatch string `header:"If-Match" doc:"one strong entity-tag \"<resourceVersion>\" or *; the write is conditional on it (ADR-0210)"`
	rv      string
}

// Resolve reads every If-Match line, as huma's own header binding keeps only the first: rv is "" when the header is
// absent or "*", else the one strong entity-tag unquoted. The version itself is never parsed (ADR-0202).
func (p *IfMatchParams) Resolve(ctx huma.Context) []error {
	var lines []string
	ctx.EachHeader(func(name, value string) {
		if strings.EqualFold(name, "If-Match") {
			lines = append(lines, value)
		}
	})
	rv, err := parseIfMatch(lines)
	if err != nil {
		return []error{wrapFaultError(err)}
	}
	p.rv = rv
	return nil
}

func parseIfMatch(lines []string) (string, error) {
	switch len(lines) {
	case 0:
		return "", nil
	case 1:
	default:
		return "", fault.Invalidf(preconditionOp, "If-Match is sent on %d lines; send one entity-tag", len(lines))
	}
	v := strings.Trim(lines[0], " \t")
	switch {
	case v == "":
		return "", fault.Invalidf(preconditionOp, "If-Match is empty; send one entity-tag \"<resourceVersion>\" or *")
	case v == "*":
		return "", nil
	case strings.HasPrefix(v, "W/"):
		return "", fault.Invalidf(preconditionOp, "If-Match %s is a weak entity-tag; send a strong one", v)
	}
	tag, ok := strings.CutPrefix(v, `"`)
	if ok {
		tag, ok = strings.CutSuffix(tag, `"`)
	}
	if !ok || tag == "" || strings.ContainsFunc(tag, func(r rune) bool { return r < 0x21 || r == '"' || r == 0x7f }) {
		return "", fault.Invalidf(preconditionOp, "If-Match %s is not one quoted entity-tag \"<resourceVersion>\"", v)
	}
	return tag, nil
}

// replaceVersion is a PUT's precondition: the If-Match version or the body's metadata.resourceVersion, or both when
// they are equal; two different versions are ambiguous.
func replaceVersion(header, body string) (string, error) {
	if header != "" && body != "" && header != body {
		return "", fault.Invalidf(preconditionOp,
			"If-Match %q and metadata.resourceVersion %q differ; send one version", header, body)
	}
	if header != "" {
		return header, nil
	}
	return body, nil
}

// withReplaceVersion sets meta's resourceVersion to the PUT's precondition.
func withReplaceVersion(p IfMatchParams, meta *v1.ObjectMeta) error {
	rv, err := replaceVersion(p.rv, meta.ResourceVersion)
	if err != nil {
		return err
	}
	meta.ResourceVersion = rv
	return nil
}

// staleVersion is the answer to a write whose precondition is not the stored version (ADR-0210 Decision 2).
func staleVersion(kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName, want string, cur v1.Object) error {
	if want == "" || want == cur.GetObjectMeta().ResourceVersion {
		return nil
	}
	ref := string(name)
	if ns != "" {
		ref = string(ns) + "/" + ref
	}
	return fault.Conflictf(preconditionOp,
		"%s %s has changed since resourceVersion %q; re-read it and apply the change again", kind, ref, want)
}
