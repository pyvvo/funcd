package contract

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2/v2"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	"github.com/pyvvo/funcd/api/fault"
)

// Detail is one entry of a 422 or 500 body's details (ADR-0301 Decision 3).
type Detail struct {
	Path    string `json:"path"`    // RFC 6901 JSON Pointer to the failing value; "" is the root
	Message string `json:"message"` // for people; the wording is not part of the contract
}

// Option configures Compile.
type Option func(*options)

type options struct {
	matchTimeout time.Duration
	budget       time.Duration // WithCallBounds: 0 is no budget
	maxDigits    int           // WithCallBounds: 0 is no limit
}

// defaultMatchTimeout is regexp2's deadline clock period (DefaultClockPeriod), so a shorter one is not tighter.
const defaultMatchTimeout = 100 * time.Millisecond

// WithMatchTimeout bounds each pattern match (default 100 ms). A match that runs longer fails the value.
func WithMatchTimeout(d time.Duration) Option {
	return func(o *options) { o.matchTimeout = d }
}

// Validator checks JSON documents against one compiled contract side. It is safe for concurrent use.
type Validator struct {
	schema *jsonschema.Schema // nil: ADR-0058's Json form, which accepts every document
	unions map[string]union   // the side's discriminated oneOfs, by the location of the schema holding each
	bounds *bounds            // WithCallBounds; nil is ADR-0301's unbounded Validator
	all    bool               // the side is {} or true: it accepts every document
}

// union is one discriminated oneOf: its tag property, and each variant's location and the tag values it names.
type union struct {
	tag      string
	variants []variant
}

type variant struct {
	location string
	tags     []string
}

// sideURL names the one document a side is; every location the engine reports lies under it.
const sideURL = "file:///contract.json"

// Compile compiles one contract side as JSON Schema 2020-12 with funcd's rules (Decisions 1 and 2). An empty
// schemaJSON compiles to a Validator that accepts every document (ADR-0058's Json form). A side that does not
// compile returns a fault.Invalid naming its schema location.
func Compile(schemaJSON []byte, opts ...Option) (*Validator, error) {
	const op = "contract.Compile"
	o := options{matchTimeout: defaultMatchTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	if len(bytes.TrimSpace(schemaJSON)) == 0 {
		return &Validator{all: true}, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fault.Invalidf(op, "contract side is not JSON: %v", err)
	}
	if m, ok := doc.(map[string]interface{}); ok && len(m) == 0 || doc == true {
		return &Validator{all: true}, nil
	}
	if o.budget > 0 || o.maxDigits > 0 {
		first, err := compileInstance(doc, o)
		if err != nil {
			return nil, err
		}
		v := &Validator{unions: map[string]union{}, bounds: &bounds{doc: doc, opts: o, idle: make(chan *instance, maxIdle)}}
		v.collectUnions(first.schema, doc)
		v.bounds.put(first)
		return v, nil
	}
	sch, err := compileSide(doc, o, ecmaScript(o.matchTimeout))
	if err != nil {
		return nil, err
	}
	v := &Validator{schema: sch, unions: map[string]union{}}
	v.collectUnions(sch, doc)
	return v, nil
}

// compileSide compiles a decoded side with funcd's formats and engine as the pattern engine.
func compileSide(doc interface{}, o options, engine jsonschema.RegexpEngine) (*jsonschema.Schema, error) {
	const op = "contract.Compile"
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	for _, f := range profileFormats() {
		c.RegisterFormat(f)
	}
	c.UseRegexpEngine(engine)
	c.UseLoader(sideOnly{})
	if err := c.AddResource(sideURL, doc); err != nil {
		return nil, fault.Invalidf(op, "contract side: %v", err)
	}
	sch, err := c.Compile(sideURL)
	if err != nil {
		return nil, fault.Invalidf(op, "contract side does not compile: %v", err)
	}
	return sch, nil
}

// AcceptsAll reports whether the side accepts every document, as ADR-0058's Json form ({} or true) does, so a check
// need not read the document (ADR-0303).
func (v *Validator) AcceptsAll() bool { return v.all }

// Validate returns every error of doc (Decision 3), or nil when doc is valid. Numbers are compared exactly, never
// through float64. It returns a fault.Invalid when doc is not JSON. It does no I/O, and the match timeout bounds
// each pattern match (Decision 2), so it takes no context.
func (v *Validator) Validate(doc []byte) ([]Detail, error) {
	const op = "contract.Validate"
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc))
	if err != nil {
		return nil, fault.Invalidf(op, "document is not JSON: %v", err)
	}
	if b := v.bounds; b != nil {
		if b.opts.maxDigits > 0 {
			if details := outsizedNumbers(inst, nil, b.opts.maxDigits, nil); len(details) > 0 {
				return sorted(details), nil
			}
		}
		in, err := b.take()
		if err != nil {
			return nil, fault.Wrapf(err, fault.Internal, op, "compile another instance")
		}
		defer b.put(in)
		if b.opts.budget > 0 {
			in.deadline.Store(time.Now().Add(b.opts.budget).UnixNano())
		}
		details, err := v.validate(in.schema, inst)
		if err == nil && in.cut.Load() {
			details = sorted(append(details, Detail{Path: "",
				Message: fmt.Sprintf("the contract check ran out of its %v budget, so later patterns failed their values", b.opts.budget)}))
		}
		return details, err
	}
	if v.schema == nil {
		return nil, nil // the Json form: AcceptsAll
	}
	return v.validate(v.schema, inst)
}

func (v *Validator) validate(sch *jsonschema.Schema, inst interface{}) ([]Detail, error) {
	var verr *jsonschema.ValidationError
	if err := sch.Validate(inst); errors.As(err, &verr) {
		return sorted(v.leaves(verr, inst, nil)), nil
	} else if err != nil {
		return nil, fault.Internalf("contract.Validate", "validate: %v", err)
	}
	return nil, nil
}

// sorted orders details by path in byte order, then message, without duplicates (Decision 3).
func sorted(details []Detail) []Detail {
	slices.SortFunc(details, func(a, b Detail) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Message, b.Message))
	})
	return slices.Compact(details)
}

// leaves turns the engine's error tree into details: one per leaf, one per missing or extra property, and a
// discriminated union's errors through tagged.
func (v *Validator) leaves(e *jsonschema.ValidationError, inst interface{}, out []Detail) []Detail {
	if k, ok := e.ErrorKind.(*kind.OneOf); ok {
		if u, ok := v.unions[e.SchemaURL]; ok {
			return v.tagged(e, k, u, inst, out)
		}
	}
	if len(e.Causes) > 0 {
		for _, c := range e.Causes {
			out = v.leaves(c, inst, out)
		}
		return out
	}
	switch k := e.ErrorKind.(type) {
	case *kind.Required:
		for _, p := range k.Missing {
			out = append(out, detail(e, append(slices.Clone(e.InstanceLocation), p), &kind.Required{Missing: []string{p}}))
		}
		return out
	case *kind.AdditionalProperties:
		for _, p := range k.Properties {
			out = append(out, detail(e, append(slices.Clone(e.InstanceLocation), p),
				&kind.AdditionalProperties{Properties: []string{p}}))
		}
		return out
	}
	return append(out, detail(e, e.InstanceLocation, e.ErrorKind))
}

// tagged reports a discriminated oneOf: the errors of the one variant the value's tag names, else one entry at
// the union's path, so an invalid union always yields an entry.
func (v *Validator) tagged(e *jsonschema.ValidationError, k *kind.OneOf, u union, inst interface{}, out []Detail) []Detail {
	at := pointer(e.InstanceLocation)
	if k.Subschemas != nil { // more than one variant accepted the value
		return append(out, detail(e, e.InstanceLocation, k))
	}
	obj, _ := valueAt(inst, e.InstanceLocation).(map[string]interface{})
	tag, ok := obj[u.tag].(string)
	if !ok {
		return append(out, Detail{Path: at, Message: fmt.Sprintf("the union's tag property '%s' is missing or not a string", u.tag)})
	}
	var named []variant
	for _, vr := range u.variants {
		if slices.Contains(vr.tags, tag) {
			named = append(named, vr)
		}
	}
	if len(named) != 1 {
		return append(out, Detail{Path: at, Message: fmt.Sprintf("tag '%s' names %d variants of the union, not one", tag, len(named))})
	}
	n := len(out)
	for _, c := range e.Causes {
		if c.SchemaURL == named[0].location || strings.HasPrefix(c.SchemaURL, named[0].location+"/") {
			out = v.leaves(c, inst, out)
		}
	}
	if len(out) == n {
		out = append(out, detail(e, e.InstanceLocation, k))
	}
	return out
}

// detail renders one leaf with the engine's English message for its kind, without the location prefix.
func detail(e *jsonschema.ValidationError, at []string, k jsonschema.ErrorKind) Detail {
	leaf := &jsonschema.ValidationError{SchemaURL: e.SchemaURL, InstanceLocation: at, ErrorKind: k}
	return Detail{Path: pointer(at), Message: leaf.BasicOutput().Error.String()}
}

// collectUnions records each discriminated oneOf of the side. The engine keeps no discriminator, so the tag
// property comes from the side's JSON, and each variant's tag values from its compiled const or enum.
func (v *Validator) collectUnions(s *jsonschema.Schema, raw interface{}) {
	if s == nil {
		return
	}
	m, _ := raw.(map[string]interface{})
	d, _ := m["discriminator"].(map[string]interface{})
	if tag, _ := d["propertyName"].(string); len(s.OneOf) > 0 && tag != "" {
		u := union{tag: tag}
		for _, vs := range s.OneOf {
			u.variants = append(u.variants, variant{location: vs.Location, tags: tagValues(vs.Properties[tag])})
		}
		v.unions[s.Location] = u
	}
	rawOneOf, _ := m["oneOf"].([]interface{})
	for i, sub := range s.OneOf {
		var r interface{}
		if i < len(rawOneOf) {
			r = rawOneOf[i]
		}
		v.collectUnions(sub, r)
	}
	rawProps, _ := m["properties"].(map[string]interface{})
	for name, sub := range s.Properties {
		v.collectUnions(sub, rawProps[name])
	}
	if sub, ok := s.AdditionalProperties.(*jsonschema.Schema); ok {
		v.collectUnions(sub, m["additionalProperties"])
	}
	v.collectUnions(s.Items2020, m["items"])
}

// tagValues returns the string values a variant's tag property allows: its const, or its enum.
func tagValues(s *jsonschema.Schema) []string {
	if s == nil {
		return nil
	}
	var vals []interface{}
	if s.Const != nil {
		vals = append(vals, *s.Const)
	}
	if s.Enum != nil {
		vals = append(vals, s.Enum.Values...)
	}
	var tags []string
	for _, x := range vals {
		if t, ok := x.(string); ok {
			tags = append(tags, t)
		}
	}
	return tags
}

// valueAt returns the value at an instance location, or nil when the location does not resolve.
func valueAt(v interface{}, at []string) interface{} {
	for _, tok := range at {
		switch x := v.(type) {
		case map[string]interface{}:
			v = x[tok]
		case []interface{}:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(x) {
				return nil
			}
			v = x[i]
		default:
			return nil
		}
	}
	return v
}

// pointer renders an instance location as an RFC 6901 JSON Pointer.
func pointer(at []string) string {
	var sb strings.Builder
	for _, tok := range at {
		sb.WriteByte('/')
		sb.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(tok))
	}
	return sb.String()
}

// ecmaScript compiles a pattern as ECMA-262 with the u flag, with the options goja uses (Decision 2). Each match
// runs under timeout, and a match that times out is a non-match.
func ecmaScript(timeout time.Duration) jsonschema.RegexpEngine {
	return func(pattern string) (jsonschema.Regexp, error) {
		re, err := regexp2.Compile(pattern, regexp2.ECMAScript|regexp2.Unicode)
		if err != nil {
			return nil, err
		}
		re.MatchTimeout = timeout
		return ecmaPattern{re}, nil
	}
}

type ecmaPattern struct {
	re *regexp2.Regexp
}

func (p ecmaPattern) String() string { return p.re.String() }

func (p ecmaPattern) MatchString(s string) bool {
	ok, err := p.re.MatchString(s)
	return err == nil && ok
}

// sideOnly refuses every reference outside the side: a contract side is one self-contained document (ADR-0058).
type sideOnly struct{}

func (sideOnly) Load(url string) (interface{}, error) {
	return nil, fault.Invalidf("contract.Compile", "$ref %s is outside the contract side", url)
}
