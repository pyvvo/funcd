package expr

import (
	"encoding/json"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
)

// fakeResolver is a schema-backed test Resolver: roots are the exposed root
// documents, and fields maps "<root>|<dotted path>" to a Field.
type fakeResolver struct {
	roots  []string
	fields map[string]Field
}

func (f fakeResolver) Roots() []string { return f.roots }

func (f fakeResolver) Resolve(root string, path []string) (Field, error) {
	key := root + "|" + join(path)
	fld, ok := f.fields[key]
	if !ok {
		return Field{}, fault.NotFoundf("test.resolve", "no field %q", key)
	}
	return fld, nil
}

func join(path []string) string {
	out := ""
	for i, p := range path {
		if i > 0 {
			out += "."
		}
		out += p
	}
	return out
}

func req(t string) Field  { return Field{Type: t, Required: true} }
func arr(it string) Field { return Field{Type: "array", Items: it, Required: true} }
func withDefault(t, def string) Field {
	return Field{Type: t, HasDefault: true, Default: json.RawMessage(def)}
}
func optional(t string) Field { return Field{Type: t} }

func docs(pairs ...string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = json.RawMessage(pairs[i+1])
	}
	return m
}

func mustCheck(t *testing.T, src string, mode Mode, r Resolver) *Expr {
	t.Helper()
	e, err := Parse(src, mode)
	if err != nil {
		t.Fatalf("Parse(%q): %v", src, err)
	}
	if err := e.Check(r); err != nil {
		t.Fatalf("Check(%q): %v", src, err)
	}
	return e
}

func mustFailCheck(t *testing.T, src string, mode Mode, r Resolver) {
	t.Helper()
	e, err := Parse(src, mode)
	if err != nil {
		return // a parse error is a valid rejection
	}
	if err := e.Check(r); err == nil {
		t.Fatalf("expected Check(%q) to fail", src)
	}
}

// scenario: select-projects-path
func TestSelectProjectsPath(t *testing.T) {
	r := fakeResolver{roots: []string{"event"}, fields: map[string]Field{"event|data.order.lines": arr("object")}}
	e := mustCheck(t, "${{ event.data.order.lines }}", Select, r)
	got, err := e.Eval(docs("event", `{"data":{"order":{"lines":[1,2,3]}}}`))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if string(got) != "[1,2,3]" {
		t.Fatalf("got %s, want [1,2,3]", got)
	}
}

// scenario: condition-evaluates
func TestConditionEvaluates(t *testing.T) {
	r := fakeResolver{roots: []string{"step.stats.output"}, fields: map[string]Field{"step.stats.output|rows": req("integer")}}
	e := mustCheck(t, "${{ step.stats.output.rows > 0 }}", Condition, r)
	for _, tc := range []struct {
		doc  string
		want bool
	}{{`{"rows":3}`, true}, {`{"rows":0}`, false}} {
		got, err := e.EvalBool(docs("step.stats.output", tc.doc))
		if err != nil {
			t.Fatalf("EvalBool(%s): %v", tc.doc, err)
		}
		if got != tc.want {
			t.Fatalf("rows %s: got %v want %v", tc.doc, got, tc.want)
		}
	}
}

// scenario: bare-reference-rejected-in-condition
func TestBareReferenceRejectedInCondition(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|publish": req("boolean")}}
	mustFailCheck(t, "${{ input.publish }}", Condition, r)
}

// scenario: static-type-mismatch-rejected
func TestStaticTypeMismatchRejected(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|name": req("string")}}
	mustFailCheck(t, "${{ input.name > 0 }}", Condition, r)
}

// scenario: unknown-path-rejected
func TestUnknownPathRejected(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|publish": req("boolean")}}
	e, err := Parse("${{ input.pubish === true }}", Condition)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := e.Check(r); err == nil || fault.KindOf(err) != fault.Invalid {
		t.Fatalf("expected an Invalid error for an unknown path, got %v", err)
	}
}

// scenario: default-substituted
func TestDefaultSubstituted(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|rows": withDefault("integer", "0")}}
	e := mustCheck(t, "${{ input.rows > -1 }}", Condition, r)
	got, err := e.EvalBool(docs("input", `{}`)) // rows absent → default 0
	if err != nil {
		t.Fatalf("EvalBool: %v", err)
	}
	if !got {
		t.Fatal("expected default 0 > -1 to be true")
	}
}

// scenario: optional-without-default-rejected
func TestOptionalWithoutDefaultRejected(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|maybe": optional("integer")}}
	mustFailCheck(t, "${{ input.maybe > 0 }}", Condition, r)
}

// scenario: guard-allows-optional
func TestGuardAllowsOptional(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|a.b": optional("string")}}
	e := mustCheck(t, `${{ input.a.b !== undefined && input.a.b === "x" }}`, Condition, r)
	got, err := e.EvalBool(docs("input", `{"a":{}}`)) // b absent → short-circuit → false
	if err != nil {
		t.Fatalf("EvalBool(absent): %v", err)
	}
	if got {
		t.Fatal("expected false when guarded field is absent")
	}
	got, err = e.EvalBool(docs("input", `{"a":{"b":"x"}}`))
	if err != nil {
		t.Fatalf("EvalBool(present): %v", err)
	}
	if !got {
		t.Fatal("expected true when b == x")
	}
}

// scenario: operators-compose
func TestOperatorsCompose(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|a": req("integer"), "input|b": req("boolean")}}
	e := mustCheck(t, "${{ input.a > 0 && input.b === true }}", Condition, r)
	got, err := e.EvalBool(docs("input", `{"a":5,"b":true}`))
	if err != nil || !got {
		t.Fatalf("expected true, got %v err %v", got, err)
	}
	got, _ = e.EvalBool(docs("input", `{"a":5,"b":false}`))
	if got {
		t.Fatal("expected false when b is false")
	}
	// truthiness rejected: a non-boolean operand of &&.
	mustFailCheck(t, "${{ input.a && input.b === true }}", Condition, r)
}

// scenario: membership-typed
func TestMembershipTyped(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|name": req("string"), "input|tier": req("string")}}
	mustFailCheck(t, "${{ [1,2].includes(input.name) }}", Condition, r)
	// well-typed membership passes and evaluates.
	e := mustCheck(t, `${{ ["silver","bronze"].includes(input.tier) }}`, Condition, r)
	got, err := e.EvalBool(docs("input", `{"name":"x","tier":"silver"}`))
	if err != nil || !got {
		t.Fatalf("expected silver in set, got %v err %v", got, err)
	}
}

// scenario: select-object-literal-constructs (ADR-0096) — a Select expression may construct an object
// from typed fields (the `pass` transform); each value is checked, and the result marshals to JSON.
func TestSelectObjectLiteral(t *testing.T) {
	r := fakeResolver{roots: []string{"input", "step.a.output"}, fields: map[string]Field{
		"input|day": req("string"), "step.a.output|rows": req("integer"),
	}}
	e := mustCheck(t, "${{ {count: step.a.output.rows, day: input.day} }}", Select, r)
	got, err := e.Eval(docs("input", `{"day":"mon"}`, "step.a.output", `{"rows":7}`))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	var out struct {
		Count int    `json:"count"`
		Day   string `json:"day"`
	}
	if err := json.Unmarshal(got, &out); err != nil || out.Count != 7 || out.Day != "mon" {
		t.Fatalf("object literal = %s (%+v), want {count:7 day:mon}", got, out)
	}
	// a value referencing an unknown field is still rejected by the checker.
	mustFailCheck(t, "${{ {x: input.nope} }}", Select, r)
	// object literals are Select-only: Condition mode rejects them (not a boolean / unsupported).
	mustFailCheck(t, "${{ {count: step.a.output.rows} }}", Condition, r)
	// an empty object literal is rejected.
	mustFailCheck(t, "${{ {} }}", Select, r)
}

// scenario: compute-arithmetic
func TestComputeArithmetic(t *testing.T) {
	r := fakeResolver{roots: []string{"step.stats.output", "step.audit.output"}, fields: map[string]Field{
		"step.stats.output|rows": req("integer"), "step.audit.output|rows": req("integer"),
	}}
	e := mustCheck(t, "${{ step.stats.output.rows + step.audit.output.rows > 100 }}", Condition, r)
	got, err := e.EvalBool(docs("step.stats.output", `{"rows":60}`, "step.audit.output", `{"rows":50}`))
	if err != nil || !got {
		t.Fatalf("expected 60+50>100, got %v err %v", got, err)
	}
}

// scenario: compute-string
func TestComputeString(t *testing.T) {
	r := fakeResolver{roots: []string{"event"}, fields: map[string]Field{"event|time": req("string")}}
	e := mustCheck(t, "${{ event.time.slice(0,10) }}", Select, r)
	got, err := e.Eval(docs("event", `{"time":"2026-07-05T09:14:02Z"}`))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if string(got) != `"2026-07-05"` {
		t.Fatalf("got %s want \"2026-07-05\"", got)
	}
	rn := fakeResolver{roots: []string{"event"}, fields: map[string]Field{"event|n": req("integer")}}
	mustFailCheck(t, "${{ event.n.slice(0,2) }}", Select, rn) // .slice on a number
}

// scenario: compute-array-sum
func TestComputeArraySum(t *testing.T) {
	r := fakeResolver{roots: []string{"step.items.output"}, fields: map[string]Field{"step.items.output|prices": arr("number")}}
	e := mustCheck(t, "${{ sum(step.items.output.prices) }}", Select, r)
	got, err := e.Eval(docs("step.items.output", `{"prices":[1.5,2.5,6]}`))
	if err != nil {
		t.Fatalf("Eval: %v", err)
	}
	if string(got) != "10" {
		t.Fatalf("got %s want 10", got)
	}
	bad := fakeResolver{roots: []string{"step.items.output"}, fields: map[string]Field{"step.items.output|names": arr("string")}}
	mustFailCheck(t, "${{ sum(step.items.output.names) }}", Select, bad)
}

// scenario: reference-operands-typed
func TestReferenceOperandsTyped(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|name": req("string"), "input|count": req("integer")}}
	mustFailCheck(t, "${{ input.name === input.count }}", Condition, r)
}

// scenario: divide-by-zero
func TestDivideByZero(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|n": req("number"), "input|d": req("number")}}
	mustFailCheck(t, "${{ input.n / 0 }}", Select, r) // literal zero rejected at Check
	e := mustCheck(t, "${{ input.n / input.d }}", Select, r)
	if _, err := e.Eval(docs("input", `{"n":10,"d":0}`)); err == nil {
		t.Fatal("expected a fault for a non-finite (division-by-zero) Select result")
	}
	got, err := e.Eval(docs("input", `{"n":10,"d":2}`))
	if err != nil || string(got) != "5" {
		t.Fatalf("expected 5, got %s err %v", got, err)
	}
}

// scenario: grammar-error-rejected
func TestGrammarErrorRejected(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|x": req("integer")}}
	for _, src := range []string{
		"${{ a..b }}",           // parse error
		"${{ input.x == 0 }}",   // coercing == rejected at Check
		"plain text",            // not a template
		"${{ }}",                // empty
		"${{ input.x = 0 }}",    // assignment
		"${{ (() => true)() }}", // arrow function
		"${{ new Date() }}",     // new
	} {
		mustFailCheck(t, src, Condition, r)
	}
}

// scenario: roots-are-context-scoped
func TestRootsAreContextScoped(t *testing.T) {
	r := fakeResolver{roots: []string{"step.stats.output", "input"}, fields: map[string]Field{
		"step.stats.output|rows": req("integer"), "input|flag": req("boolean"),
	}}
	e := mustCheck(t, "${{ step.stats.output.rows > 0 }}", Condition, r)
	if got := e.Roots(); len(got) != 1 || got[0] != "step.stats.output" {
		t.Fatalf("Roots() = %v, want [step.stats.output]", got)
	}
	mustFailCheck(t, "${{ event.data.x !== undefined }}", Condition, r) // event not a root
}

// FuzzParse asserts Parse+Check never panic on arbitrary input.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"${{ input.x > 0 }}", "${{ event.a.b }}", "${{ a..b }}", "${{ }}",
		"not a template", "${{ input.x == 0 }}", `${{ ["a"].includes(input.x) }}`,
		"${{ input.a !== undefined && input.a > 0 }}",
	} {
		f.Add(s)
	}
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|x": req("integer")}}
	f.Fuzz(func(t *testing.T, src string) {
		e, err := Parse(src, Condition)
		if err != nil {
			return
		}
		_ = e.Check(r)
	})
}
