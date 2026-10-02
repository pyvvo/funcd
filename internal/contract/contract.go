// Package contract enforces the funcd contract type profile (ADR-0058): the bounded,
// language-agnostic subset of JSON Schema 2020-12 that a function's I/O contract may use —
// "the def" (the discipline JTD and WIT apply). A contract is GENERATED from the author's
// code type (typia / pydantic) and then run through Check before it is shipped or embedded,
// so a contract can never advertise a shape a peer runtime (or the registry) can't honor.
//
// Supported: scalars (string + format date-time/uuid/email/uri, pattern, length; integer +
// format int32/int64; number; boolean), string
// enums, CLOSED objects (additionalProperties:false), arrays, string-keyed maps
// (additionalProperties:<type>, no properties), DISCRIMINATED unions (oneOf + discriminator),
// optional/nullable, and the explicit `Json` form (the empty schema {} — arbitrary JSON).
//
// Forbidden (Check returns a fault.Invalid naming the construct): OPEN records
// (additionalProperties true/absent on a record), NON-DISCRIMINATED oneOf/anyOf/allOf, not,
// if/then/else, external/recursive $ref, and recursive types (which require $ref).
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// Check reports whether a JSON Schema (a generated function I/O contract) lies within the
// funcd profile. It returns nil when every construct is supported, or a fault.Invalid whose
// message names the first unsupported construct and its JSON-pointer-ish path. A nil/empty
// input is treated as the `Json` (any) form — valid.
func Check(schemaJSON []byte) error {
	const op = "contract.Check"
	if len(bytes.TrimSpace(schemaJSON)) == 0 {
		return nil // no contract declared ⇒ nothing to gate
	}
	var s schema
	if err := json.Unmarshal(schemaJSON, &s); err != nil {
		return fault.Invalidf(op, "contract is not valid JSON Schema JSON: %v", err)
	}
	if err := walk(&s, "(root)"); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "contract type is outside the funcd profile")
	}
	return nil
}

// walk validates one schema node against the profile, recursing into children. path is a
// human-readable location used in the error so the author can find the offending construct.
func walk(s *schema, path string) error {
	if err := checkFormat(s, path); err != nil {
		return err
	}
	switch {
	// The empty schema {} (and the boolean schema `true`) is the explicit `Json` form: any value.
	case s.isEmpty():
		return nil
	case !s.Type.inProfile():
		return unsupported(path, "type "+strings.Join(s.Type.values, "|"),
			`use one supported type, or one type plus "null" for a nullable value`)
	case s.Ref != "":
		return unsupported(path, "$ref", "external or recursive references are not supported; inline the type (recursive types are forbidden in v1.1)")
	case len(s.AnyOf) > 0:
		return unsupported(path, "anyOf", "use a discriminated `oneOf` (a tagged union) instead")
	case len(s.AllOf) > 0:
		return unsupported(path, "allOf", "flatten into a single closed object")
	case s.Not != nil:
		return unsupported(path, "not", "negation has no portable mapping")
	case s.If != nil || s.Then != nil || s.Else != nil:
		return unsupported(path, "if/then/else", "conditional schemas have no portable mapping")
	case len(s.OneOf) > 0:
		return walkUnion(s, path)
	case len(s.Enum) > 0:
		return nil // enum of literals (typically strings) — supported
	}

	switch s.primaryType() {
	case "object", "": // a bare {properties:…} with no "type" is still an object
		return walkObject(s, path)
	case "array":
		if s.Items == nil {
			return unsupported(path, "array without items", "declare the element type")
		}
		return walk(s.Items, path+"[]")
	}
	return nil // scalars (incl. profile formats/ranges/patterns) and explicit null are supported
}

// walkObject enforces the closed-record / typed-map rule — the heart of "no open records".
func walkObject(s *schema, path string) error {
	hasProps := len(s.Properties) > 0
	ap := s.AdditionalProperties

	switch {
	case hasProps && ap.isFalse():
		// closed record — the default object form. Recurse each field.
		for name, field := range s.Properties {
			if err := walk(field, path+"."+name); err != nil {
				return err
			}
		}
		return nil
	case !hasProps && ap.isSchema():
		// typed map (string keys → a known value type). Recurse the value type.
		return walk(ap.Schema, path+"{}")
	case hasProps && ap.isSchema():
		return unsupported(path, "object with both properties and a typed additionalProperties",
			"use a closed record (additionalProperties:false) OR a typed map (no fixed properties), not both")
	default:
		// properties with additionalProperties true/absent, or an untyped open object.
		return unsupported(path, "open record (additionalProperties is not false)",
			"close it with additionalProperties:false, use a typed map (Record<string,T> / dict[str,T]), or the `Json` type for arbitrary data")
	}
}

// walkUnion requires a discriminated union (oneOf + discriminator) and recurses each variant.
func walkUnion(s *schema, path string) error {
	if s.Discriminator == nil || s.Discriminator.PropertyName == "" {
		return unsupported(path, "non-discriminated oneOf",
			"add a discriminator (a shared literal tag field) so the union is a tagged union")
	}
	for i, v := range s.OneOf {
		if err := walk(v, fmt.Sprintf("%s|%d", path, i)); err != nil {
			return err
		}
	}
	return nil
}

// checkFormat admits only the profile's `format` values (ADR-0058). Every runtime must compile them; any
// other format would pass push and fail the worker's validator compile instead.
func checkFormat(s *schema, path string) error {
	switch s.primaryType() {
	case "string":
		if slices.Contains([]string{"", "date-time", "uuid", "email", "uri"}, s.Format) {
			return nil
		}
	case "integer":
		if slices.Contains([]string{"", "int32", "int64"}, s.Format) {
			return nil
		}
	default:
		if s.Format == "" {
			return nil
		}
	}
	return unsupported(path, "format "+s.Format,
		"the profile allows only date-time, uuid, email or uri on a string and int32 or int64 on an integer")
}

func unsupported(path, construct, fix string) error {
	return fault.Invalidf("contract.walk", "%s: unsupported construct %q — %s", path, construct, fix)
}
