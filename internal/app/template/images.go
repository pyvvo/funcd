package template

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
	"oras.land/oras-go/v2/registry"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/expr"
)

const imageOp = "app.image"

// Image is an images entry parsed (ADR-0218 Decision 1): a repository and either an exact tag or a range.
type Image struct {
	Repo  string              // passes Reference.ValidateRepository
	Exact string              // the tag when the spec is a version; "" for a range
	Range *semver.Constraints // nil when Exact is set
}

// vBeforeVersion finds a v before a version in a range, which the constraint grammar accepts (^v1.0.0) but a
// strict tag never holds; a v inside a pre-release identifier (1.0.0-rc.v2) follows a letter, digit, . or -.
var vBeforeVersion = regexp.MustCompile(`(^|[\s^~<>=!,|])v[0-9xX*]`)

// ParseImage parses an images entry <repo>:<spec>, split at the first ":" (a repo carries no host): the spec is an
// exact version when StrictNewVersion parses it, else a range when NewConstraint parses it. A v before a version,
// build metadata and a digest are refused.
func ParseImage(entry string) (Image, error) {
	repo, spec, ok := strings.Cut(entry, ":")
	if !ok || repo == "" || spec == "" {
		return Image{}, fault.Invalidf(imageOp, "%q is not <repo>:<version or range>", entry)
	}
	if err := (registry.Reference{Repository: repo}).ValidateRepository(); err != nil {
		return Image{}, fault.Invalidf(imageOp, "%q: %q is not a repository (a repo carries no host): %v", entry, repo, err)
	}
	switch {
	case strings.Contains(spec, "@"):
		return Image{}, fault.Invalidf(imageOp, "%q holds a digest: app.lock records the digest of each image", entry)
	case strings.Contains(spec, "+"):
		return Image{}, fault.Invalidf(imageOp, "%q holds build metadata: an OCI tag cannot hold a +", entry)
	case vBeforeVersion.MatchString(spec):
		return Image{}, fault.Invalidf(imageOp, "%q has a v before a version: a tag is the version without one", entry)
	}
	if _, err := semver.StrictNewVersion(spec); err == nil {
		return Image{Repo: repo, Exact: spec}, nil
	}
	c, err := semver.NewConstraint(spec)
	if err != nil {
		return Image{}, fault.Invalidf(imageOp, "%q: %q is neither a version nor a range: %v", entry, spec, err)
	}
	return Image{Repo: repo, Range: c}, nil
}

// parsed is the entry of image name, parsed.
func (t *Template) parsed(name string) (Image, error) {
	if img, ok := t.Parsed[name]; ok {
		return img, nil
	}
	return ParseImage(t.Images[name])
}

// ImageRef is the value an images entry renders to: <registry>/<repo>:<version>@<digest>.
func ImageRef(registry, repo string, l LockedImage) (string, error) {
	if l.Version == "" || !digestForm.MatchString(l.Digest) {
		return "", fault.Invalidf(imageOp, "the lock of %s records version %q and digest %q: run funcdctl app lock", repo, l.Version, l.Digest)
	}
	return registry + "/" + repo + ":" + l.Version + "@" + l.Digest, nil
}

// EvalRegistry merges and validates the values and evaluates registry over them (ADR-0218 Decision 4). With lock,
// only the values registry reads are validated, against their subschemas, so app lock needs no other value.
func EvalRegistry(t *Template, values []json.RawMessage, lock bool) (string, error) {
	merged, err := mergeValues(values)
	if err != nil {
		return "", err
	}
	return evalRegistry(t, merged, lock)
}

func evalRegistry(t *Template, values json.RawMessage, lock bool) (string, error) {
	if !lock {
		if err := validateValues(t.ValuesSchema, values); err != nil {
			return "", err
		}
	}
	if err := checkForm("registry", t.Registry, expr.Select, valuesRoot); err != nil {
		return "", fault.Invalidf(renderOp, "app.yaml: %v", err)
	}
	registry := t.Registry
	if isExpression(registry) {
		e, err := expr.Parse(registry, expr.Select)
		if err != nil {
			return "", fault.Invalidf(renderOp, "app.yaml: registry: %v", err)
		}
		reads := &readKeys{Resolver: expr.NewSchemaResolver(map[string]json.RawMessage{valuesRoot: t.ValuesSchema}, nil, expr.StrictTypes())}
		if err := e.Check(reads); err != nil {
			return "", fault.Invalidf(renderOp, "app.yaml: registry: %v", err)
		}
		if lock {
			if err := validateKeys(t.ValuesSchema, values, reads.keys); err != nil {
				return "", err
			}
		}
		out, err := e.Eval(map[string]json.RawMessage{valuesRoot: values})
		if err != nil {
			return "", fault.Invalidf(renderOp, "app.yaml: registry: %v", err)
		}
		if json.Unmarshal(out, &registry) != nil {
			return "", fault.Invalidf(renderOp, "app.yaml: registry gives %s, not a string", out)
		}
		if isExpression(registry) || interpolates(registry) {
			return "", fault.Invalidf(renderOp, "app.yaml: registry gives %q: render never writes an expression", registry)
		}
	}
	if t.Registry == "" {
		return "", nil
	}
	if msg := badRegistry(registry); msg != "" {
		return "", fault.Invalidf(renderOp, "app.yaml: registry gives %q: %s", registry, msg)
	}
	return registry, nil
}

// badRegistry says why a registry value is refused, "" when it is not: it is a non-empty string without a trailing
// /, a scheme other than oci-layout://, a digest or a tag. A tag is a ":" after the last / of an oci-layout:// value
// (parseLocalRef's rule) or after the first / of any other, as a host's ":" is a port.
func badRegistry(registry string) string {
	const layout = "oci-layout://"
	switch {
	case registry == "" || strings.HasSuffix(registry, "/"):
		return "it must be a non-empty string without a trailing /"
	case strings.Contains(registry, "://") && !strings.HasPrefix(registry, layout):
		return "a registry is a host[:port][/path] or oci-layout://<dir>, no other scheme"
	case strings.Contains(registry, "@"):
		return "a registry holds no digest: app.lock records the digest of each image"
	}
	if dir, ok := strings.CutPrefix(registry, layout); ok {
		if strings.Contains(dir[strings.LastIndex(dir, "/")+1:], ":") {
			return "a registry holds no tag: each image's tag is its version"
		}
		return ""
	}
	if _, path, ok := strings.Cut(registry, "/"); ok && strings.Contains(path, ":") {
		return "a registry holds no tag: each image's tag is its version"
	}
	return ""
}

// readKeys records the top-level values keys an expression reads while it is checked.
type readKeys struct {
	expr.Resolver
	keys []string
}

func (r *readKeys) Resolve(root string, path []string) (expr.Field, error) {
	if root == valuesRoot && len(path) > 0 && !slices.Contains(r.keys, path[0]) {
		r.keys = append(r.keys, path[0])
	}
	return r.Resolver.Resolve(root, path)
}
