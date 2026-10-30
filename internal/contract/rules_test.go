package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/contract"
)

func compileSide(t *testing.T, side string) *contract.Validator {
	t.Helper()
	v, err := contract.Compile([]byte(side))
	if err != nil {
		t.Fatalf("Compile(%s): %v", side, err)
	}
	return v
}

// wantPaths asserts that doc yields exactly the details at paths, in order, each with a message; no paths means valid.
func wantPaths(t *testing.T, v *contract.Validator, doc string, paths ...string) {
	t.Helper()
	d, err := v.Validate([]byte(doc))
	if err != nil {
		t.Fatalf("Validate(%s): %v", doc, err)
	}
	got := make([]string, 0, len(d))
	for _, x := range d {
		if x.Message == "" {
			t.Errorf("Validate(%s): detail at %q has no message", doc, x.Path)
		}
		got = append(got, x.Path)
	}
	if !slices.Equal(got, paths) {
		t.Errorf("Validate(%s) paths = %q, want %q (details %+v)", doc, got, paths, d)
	}
}

// scenario: date-time-follows-rfc3339
func TestScenarioDateTimeFollowsRFC3339(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "string", "format": "date-time"}`)
	wantPaths(t, v, `"2026-10-30T10:00:00Z"`)
	wantPaths(t, v, `"2026-10-30T10:00:00.5+02:00"`)
	wantPaths(t, v, `"2026-02-30T10:00:00Z"`, "")
	wantPaths(t, v, `"2026-10-30T25:00:00Z"`, "")
	wantPaths(t, v, `"2026-10-30T10:00:00"`, "")
}

// scenario: uuid-is-the-string-form
func TestScenarioUUIDIsTheStringForm(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "string", "format": "uuid"}`)
	wantPaths(t, v, `"550E8400-E29B-41D4-A716-446655440000"`)
	wantPaths(t, v, `"urn:uuid:550e8400-e29b-41d4-a716-446655440000"`, "")
}

// scenario: email-is-an-rfc5321-mailbox
func TestScenarioEmailIsAnRFC5321Mailbox(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "string", "format": "email"}`)
	wantPaths(t, v, `"ops@localhost"`)
	wantPaths(t, v, `"a b@example.com"`, "")
}

// scenario: uri-has-a-scheme-and-is-ascii
func TestScenarioURIHasASchemeAndIsASCII(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "string", "format": "uri"}`)
	wantPaths(t, v, `"https://example.com/a?b=c"`)
	wantPaths(t, v, `"https://example.com/a#b"`)
	wantPaths(t, v, `"http://例え.テスト/"`, "")
	wantPaths(t, v, `"http://example.com/a b"`, "")
	wantPaths(t, v, `"/relative/path"`, "")
}

// scenario: integer-formats-keep-their-ranges
func TestScenarioIntegerFormatsKeepTheirRanges(t *testing.T) {
	t.Parallel()
	i32 := compileSide(t, `{"type": "integer", "format": "int32"}`)
	wantPaths(t, i32, `2147483647`)
	wantPaths(t, i32, `-2147483648`)
	wantPaths(t, i32, `2147483648`, "")
	wantPaths(t, i32, `-2147483649`, "")
	i64 := compileSide(t, `{"type": "integer", "format": "int64"}`)
	wantPaths(t, i64, `9007199254740991`)
	wantPaths(t, i64, `-9007199254740991`)
	wantPaths(t, i64, `9007199254740992`, "")
	wantPaths(t, i64, `-9007199254740992`, "")
}

// The integer formats read a number's digits, so an integer written with an exponent or a trailing fraction of
// zeros keeps its range, and no exponent is expanded.
func TestIntegerFormatsReadTheDigits(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "integer", "format": "int32"}`)
	wantPaths(t, v, `2.147483647e9`)
	wantPaths(t, v, `2147483647.000`)
	wantPaths(t, v, `0.0`)
	wantPaths(t, v, `2.147483648e9`, "")
	start := time.Now()
	wantPaths(t, v, `1e999999999`, "")
	if took := time.Since(start); took > time.Second {
		t.Errorf("a huge exponent took %v", took)
	}
}

// scenario: pattern-follows-ecma262
func TestScenarioPatternFollowsECMA262(t *testing.T) {
	t.Parallel()
	digits := compileSide(t, `{"type": "string", "pattern": "^\\d+$"}`)
	wantPaths(t, digits, `"123"`)
	wantPaths(t, digits, `"١٢٣"`, "")
	wantPaths(t, digits, `"123\n"`, "")
	word := compileSide(t, `{"type": "string", "pattern": "^\\w+$"}`)
	wantPaths(t, word, `"café"`, "")
	look := compileSide(t, `{"type": "string", "pattern": "^(?!admin$)[a-z]+$"}`)
	wantPaths(t, look, `"alice"`)
	wantPaths(t, look, `"admin"`, "")
}

// scenario: a-runaway-pattern-match-fails-the-value
func TestScenarioARunawayPatternMatchFailsTheValue(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "string", "pattern": "^(a+)+$"}`)
	start := time.Now()
	wantPaths(t, v, `"`+strings.Repeat("a", 40)+`!"`, "")
	if took := time.Since(start); took > time.Second {
		t.Errorf("Validate took %v, want under 1 s", took)
	}
}

// scenario: every-error-is-listed
func TestScenarioEveryErrorIsListed(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "object", "properties": {"a": {"type": "string"}, "b": {"type": "integer"}},
		"required": ["a", "b"], "additionalProperties": false}`)
	wantPaths(t, v, `{"a": 1, "b": "x"}`, "/a", "/b")
	wantPaths(t, v, `{"a": "x"}`, "/b")
	wantPaths(t, v, `{"a": "x", "b": 1, "c": true}`, "/c")
}

// scenario: a-union-reports-its-tagged-variant
func TestScenarioAUnionReportsItsTaggedVariant(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"oneOf": [
		{"type": "object", "properties": {"kind": {"const": "a"}, "x": {"type": "integer"}}, "required": ["kind", "x"],
		 "additionalProperties": false},
		{"type": "object", "properties": {"kind": {"const": "b"}, "y": {"type": "string"}}, "required": ["kind", "y"],
		 "additionalProperties": false}
	], "discriminator": {"propertyName": "kind"}}`)
	wantPaths(t, v, `{"kind": "a", "x": 1}`)
	wantPaths(t, v, `{"kind": "a", "x": "1"}`, "/x")
	wantPaths(t, v, `{"kind": "c"}`, "")
	wantPaths(t, v, `{"x": 1}`, "")
	both := compileSide(t, `{"oneOf": [
		{"type": "object", "properties": {"kind": {"const": "a"}}, "additionalProperties": false},
		{"type": "object", "properties": {"kind": {"enum": ["a", "b"]}}, "additionalProperties": false}
	], "discriminator": {"propertyName": "kind"}}`)
	wantPaths(t, both, `{"kind": "a"}`, "")
	wantPaths(t, both, `{"kind": "b"}`)
}

// scenario: push-gates-patterns
func TestScenarioPushGatesPatterns(t *testing.T) {
	t.Parallel()
	bad := `{"type": "object", "properties": {"a": {"type": "string", "pattern": "[z-a]"}}, "additionalProperties": false}`
	err := contract.Check([]byte(bad))
	if fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "properties/a") {
		t.Fatalf("Check(%s) = %v, want Invalid naming properties/a", bad, err)
	}
	good := `{"type": "object", "properties": {"a": {"type": "string", "pattern": "^(?!admin$)[a-z]+$"}},
		"additionalProperties": false}`
	if err := contract.Check([]byte(good)); err != nil {
		t.Fatalf("Check(%s) = %v, want nil", good, err)
	}
}

// TestFormatTable pins each format to the JSON-Schema-Test-Suite's draft 2020-12 optional/format cases (Decision 1),
// vendored under testdata/jsts with their license.
func TestFormatTable(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob(filepath.Join("testdata", "jsts", "*.json"))
	if err != nil || len(files) != 4 {
		t.Fatalf("suite files = %v, %v; want date-time, email, uri and uuid", files, err)
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var groups []struct {
			Schema json.RawMessage `json:"schema"`
			Tests  []struct {
				Description string          `json:"description"`
				Data        json.RawMessage `json:"data"`
				Valid       bool            `json:"valid"`
			} `json:"tests"`
		}
		if err := json.Unmarshal(raw, &groups); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, g := range groups {
			v := compileSide(t, string(g.Schema))
			for _, c := range g.Tests {
				d, err := v.Validate(c.Data)
				if err != nil {
					t.Fatalf("%s: %s: %v", f, c.Description, err)
				}
				if got := len(d) == 0; got != c.Valid {
					t.Errorf("%s: %s: %s valid = %v, want %v", filepath.Base(f), c.Description, c.Data, got, c.Valid)
				}
			}
		}
	}
}

// One Validator serves concurrent calls with the same answers as serial ones.
func TestValidateConcurrent(t *testing.T) {
	t.Parallel()
	v := compileSide(t, `{"type": "object", "properties": {
		"id": {"type": "string", "format": "uuid"}, "mail": {"type": "string", "format": "email"},
		"name": {"type": "string", "pattern": "^[a-z]+$"}}, "required": ["id", "name"], "additionalProperties": false}`)
	docs := []string{
		`{"id": "550e8400-e29b-41d4-a716-446655440000", "name": "alice", "mail": "ops@localhost"}`,
		`{"id": "x", "name": "Alice", "extra": 1}`,
		`{"name": "bob", "mail": "a b@example.com"}`,
		`{"id": "550e8400-e29b-41d4-a716-446655440000", "name": "carol"}`,
	}
	want := make([][]contract.Detail, len(docs))
	for i, doc := range docs {
		d, err := v.Validate([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		want[i] = d
	}
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for n := range 100 {
				i := (g + n) % len(docs)
				d, err := v.Validate([]byte(docs[i]))
				if err != nil || !slices.Equal(d, want[i]) {
					t.Errorf("Validate(%s) = %+v, %v; want %+v", docs[i], d, err, want[i])
					return
				}
			}
		})
	}
	wg.Wait()
}

// A document that is not JSON is Invalid, for a contract side and for the Json form alike.
func TestValidateRefusesNonJSON(t *testing.T) {
	t.Parallel()
	for _, side := range []string{`{"type": "string"}`, ``} {
		v := compileSide(t, side)
		for _, doc := range []string{`{"a":`, `1 2`, ``} {
			if _, err := v.Validate([]byte(doc)); fault.KindOf(err) != fault.Invalid {
				t.Errorf("side %q: Validate(%q) = %v, want Invalid", side, doc, err)
			}
		}
	}
}
