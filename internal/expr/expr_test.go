package expr

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
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

// A document-backed Resolver reports a missing field as the absent Field: the guard probes it and its
// && operand goes unchecked, while any other read of it is still an unknown field.
func TestIssue308_AbsentFieldOnlyProbed(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|x": {}}}
	e := mustCheck(t, `${{ input.x !== undefined && input.x > 1 }}`, Condition, r)
	if got, err := e.EvalBool(docs("input", `{}`)); err != nil || got {
		t.Fatalf("EvalBool = %v, %v; want false, nil", got, err)
	}
	mustFailCheck(t, "${{ input.x > 1 }}", Condition, r)
	mustFailCheck(t, `${{ input.x === undefined && input.x > 1 }}`, Condition, r)
	mustFailCheck(t, `${{ input.y !== undefined && input.y > 1 }}`, Condition, r)
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

// Chained replaceAll multiplies a string's length (each .replaceAll("", t) by about len(t)+1), a
// replacement's $` and $' insert the text around every match, and concatenated or length-preserving
// calls add up: evaluation fails once the strings the calls of one evaluation build pass the limit,
// before they are allocated, and keeps JavaScript semantics below it.
func TestReplaceAllResultLengthIsBounded(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|s": req("string")}}
	in := docs("input", `{"s":"`+strings.Repeat("z", 2000)+`"}`)
	chain := `"xxxxxxxxxx"` + strings.Repeat(`.replaceAll("", "yyyyyyyyyy")`, 5)
	for _, src := range []string{
		"${{ " + chain + ".length > 0 }}",
		`${{ input.s.replaceAll("", "$'").length > 0 }}`,
		`${{ input.s.replaceAll("z", "$'").length > 0 }}`,
		"${{ " + strings.Repeat(`input.s.replaceAll("z", "zzzzzzzzzz") + `, 60) + `"" !== "" }}`,
		`${{ input.s.replaceAll("z", "zzzzzzzzzz")` + strings.Repeat(`.replaceAll("x", "x")`, 60) + ` !== "" }}`,
	} {
		x := mustCheck(t, src, Condition, r)
		x.timeout = time.Minute // under load the deadline could fire first; this test checks the budget alone
		ok, err := x.EvalBool(in)
		if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "characters") {
			t.Errorf("%.70s: got ok=%v err=%v, want a fault.Invalid over the replaceAll length limit", src, ok, err)
		}
	}
	ok, err := mustCheck(t, `${{ "a-b".replaceAll("-", "[$&$$]") === "a[-$]b" && "abc".replaceAll("b", "[$`+"`"+`$']") === "a[ac]c" && "ab".replaceAll("", "-") === "-a-b-" && input.s.replaceAll("z", "") === "" }}`, Condition, r).EvalBool(in)
	if err != nil || !ok {
		t.Fatalf("replaceAll within the limit: ok=%v err=%v, want true", ok, err)
	}
}

// String '+' copies its operands, so many concatenations of a document cost time and memory that grow
// with the expression's length: evaluation is interrupted once it passes its deadline.
func TestEvaluationIsInterruptedAtTheDeadline(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|s": req("string")}}
	x := mustCheck(t, "${{ "+strings.Repeat("input.s + ", 400)+`"" !== "" }}`, Condition, r)
	x.timeout = time.Millisecond
	ok, err := x.EvalBool(docs("input", `{"s":"`+strings.Repeat("z", 2000)+`"}`))
	if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("got ok=%v err=%v, want a fault.Invalid at the time limit", ok, err)
	}
}

// An array or object literal can repeat a large document reference any number of times at almost no
// evaluation cost: Eval fails once the result passes its documents' size plus a margin, before it is
// exported and marshalled, and returns a result within that bound unchanged. An array literal's strings
// are charged to the string length before it runs, so it fails there first.
func TestSelectResultSizeIsBounded(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|": req("object"), "input|s": req("string")}}
	in := docs("input", `{"s":"`+strings.Repeat("z", 64<<10)+`"}`)
	members := make([]string, 1000)
	for i := range members {
		members[i] = fmt.Sprintf("k%d: input", i)
	}
	for _, c := range []struct{ src, want string }{
		{"${{ [" + strings.Repeat("input.s, ", 1000) + `""] }}`, "characters"},
		{"${{ {" + strings.Join(members, ", ") + "} }}", "larger than their documents"},
	} {
		x := mustCheck(t, c.src, Select, r)
		x.timeout = time.Minute
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		out, err := x.Eval(in)
		runtime.ReadMemStats(&after)
		if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%.40s: got %d bytes err=%v, want a fault.Invalid (%s)", c.src, len(out), err, c.want)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
			t.Errorf("%.40s: allocated %d MiB, want the result rejected before it is built", c.src, n>>20)
		}
	}
	out, err := mustCheck(t, "${{ [input.s, input.s] }}", Select, r).Eval(in)
	if err != nil || len(out) != 2*(64<<10+2)+3 {
		t.Fatalf("two references: got %d bytes err=%v, want the result", len(out), err)
	}
	big := `{"s":"` + strings.Repeat("z", 3<<20) + `"}`
	one := mustCheck(t, "${{ input.s }}", Select, r)
	one.timeout = time.Minute // binding the document is charged to the deadline; this case checks the margin
	out, err = one.Eval(docs("input", big))
	if err != nil || len(out) != 3<<20+2 {
		t.Fatalf("a document larger than the margin: got %d bytes err=%v, want the result", len(out), err)
	}
}

// Evaluations within one Budget share its replaceAll length, its running time and its result margin,
// so expressions that each fit alone fail once together they pass the bound; a document's size is
// allowed for once, however many evaluations reference it.
func TestBudgetIsSharedAcrossEvaluations(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|s": req("string"), "input|t": req("string")}}
	in := docs("input", `{"s":"`+strings.Repeat("z", 256<<10)+`","t":"abc"}`)
	for _, c := range []struct {
		src, want string
		n         int
	}{
		{"${{ input.t" + strings.Repeat(`.replaceAll("", "yyyyyyyyyy")`, 5) + " }}", "characters", 2},
		{"${{ input.s }}", "larger than their documents", 8},
	} {
		x := mustCheck(t, c.src, Select, r)
		x.timeout = time.Minute
		for i := 0; i < c.n; i++ {
			if _, err := x.Eval(in); err != nil {
				t.Fatalf("%.40s: evaluation %d alone: %v", c.src, i, err)
			}
		}
		b := NewBudget()
		b.time = time.Minute
		var err error
		for i := 0; i < c.n && err == nil; i++ {
			_, err = x.EvalWithin(b, in)
		}
		if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%.40s: %d evaluations within one budget: err=%v, want a fault.Invalid (%s)", c.src, c.n, err, c.want)
		}
	}
	slow := mustCheck(t, "${{ ["+strings.Repeat(`input.s.includes("y"), `, 400)+"true] }}", Select, r)
	b := NewBudget()
	b.time = time.Millisecond
	_, err := slow.EvalWithin(b, in)
	if err == nil || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("slow evaluation: err=%v, want the time limit", err)
	}
	_, err = mustCheck(t, "${{ input.t }}", Select, r).EvalWithin(b, in)
	if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("an evaluation after the budget's time is spent: err=%v, want a fault.Invalid at the time limit", err)
	}
}

// goja's own search on a non-ASCII string compares the whole pattern at every position, and the
// deadline cannot stop a native call: includes and replaceAll search in time linear in their operands,
// and keep their JavaScript results, also for a pattern that overlaps itself after a partial match.
func TestStringSearchIsLinear(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{
		"input|s": req("string"), "input|p": req("string"), "input|r": req("string"),
	}}
	in := docs("input", `{"s":"é`+strings.Repeat("a", 160000)+`","p":"`+strings.Repeat("a", 80000)+`b","r":""}`)
	for _, src := range []string{
		"${{ !input.s.includes(input.p) }}",
		"${{ input.s.replaceAll(input.p, input.r) === input.s }}",
	} {
		x := mustCheck(t, src, Condition, r)
		x.timeout = time.Minute
		start := time.Now()
		ok, err := x.EvalBool(in)
		if d := time.Since(start); err != nil || !ok || d > 250*time.Millisecond {
			t.Errorf("%s over a non-ASCII document: ok=%v err=%v in %s, want true well within the deadline", src, ok, err, d)
		}
	}
	includes := mustCheck(t, "${{ input.s.includes(input.p) }}", Select, r)
	replaceAll := mustCheck(t, "${{ input.s.replaceAll(input.p, input.r) }}", Select, r)
	vm := goja.New()
	for _, c := range [][3]string{
		{"aaa", "aa", "b"},
		{"ab", "", "-$&-"},
		{"é😀é😀", "😀", "[$`|$'|$&|$$|$1|$<x>|$0$]"},
		{"abcabc", "bc", "$'"},
		{"", "", "$&x$"},
		{"xyz", "q", "$`"},
		{"aXbX", "X", "$$$&$"},
		{"abaabababaabab", "abab", "<$&>"},
		{"aaab", "aab", "<$&>"},
		{"abababc", "ababc", "$`"},
		{"aabaaabaaaa", "aabaaaa", "$'"},
		{"😀😀😀a", "😀😀a", "[$&]"},
	} {
		vals := map[string]string{"s": c[0], "p": c[1], "r": c[2]}
		doc, err := json.Marshal(vals)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range vals {
			if err := vm.Set(k, v); err != nil {
				t.Fatal(err)
			}
		}
		for x, js := range map[*Expr]string{includes: "s.includes(p)", replaceAll: "s.replaceAll(p, r)"} {
			want, err := vm.RunString(js)
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, _ := json.Marshal(want.Export())
			got, err := x.Eval(docs("input", string(doc)))
			if err != nil || string(got) != string(wantJSON) {
				t.Errorf("%s with %q: got %s err=%v, want %s", js, c, got, err, wantJSON)
			}
		}
	}
}

// A '+' result is never longer than its two operands together: a concatenation that repeats a large
// document is refused against the Budget's string length before it runs, and one within it keeps its
// result.
func TestConcatenationIsBoundedBeforeItRuns(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{
		"input|s": req("string"), "input|t": req("string"), "input|u": req("string"),
	}}
	in := docs("input", `{"s":"`+strings.Repeat("z", 64<<10)+`","t":"abc","u":"x1-ΐ"}`)
	tree := "input.s"
	for range 6 {
		tree = "(" + tree + " + " + tree + ")"
	}
	x := mustCheck(t, "${{ "+tree+".length > 0 }}", Condition, r)
	x.timeout = time.Minute
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ok, err := x.EvalBool(in)
	runtime.ReadMemStats(&after)
	if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "characters") {
		t.Errorf("a concatenation of 64 references: ok=%v err=%v, want a fault.Invalid over the string length limit", ok, err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
		t.Errorf("allocated %d MiB, want the concatenation refused before it runs", n>>20)
	}
	out, err := mustCheck(t, `${{ input.t + "-" + input.t.toUpperCase() }}`, Select, r).Eval(in)
	if err != nil || string(out) != `"abc-ABC"` {
		t.Fatalf("a concatenation within the limit: got %s err=%v, want \"abc-ABC\"", out, err)
	}
	// A case change can return more code units than its receiver has UTF-8 bytes: "x1-ΐ" is 5 bytes,
	// and upper case it is 6 code units.
	up := mustCheck(t, `${{ input.u.toUpperCase() + "" }}`, Select, r)
	b := NewBudget()
	b.strings = 5
	if out, err := up.EvalWithin(b, in); err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "characters") {
		t.Errorf("a 6-unit case change within a 5-unit budget: got %s err=%v, want a fault.Invalid over the string length limit", out, err)
	}
	if out, err := up.Eval(in); err != nil || string(out) != `"X1-`+"\u0399\u0308\u0301"+`"` {
		t.Errorf("a case change within the limit: got %s err=%v, want its upper case", out, err)
	}
}

// An array or object literal keeps every string it holds until the evaluation ends, and goja copies a
// non-ASCII string that it slices or case-changes, or that Array.prototype.includes compares, which the
// deadline cannot stop: the string elements of a literal are charged to the Budget's string length
// before it runs. An array or object a literal reads is the bound document, and is not charged.
func TestLiteralStringsAreBoundedBeforeTheyRun(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{
		"input|s": req("string"), "input|t": req("string"), "input|list": arr("string"),
	}}
	in := docs("input", `{"s":"`+strings.Repeat("é", 32<<10)+`"}`)
	keys := make([]string, 512)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%d: input.s.toLowerCase()", i)
	}
	for _, c := range []struct {
		src  string
		mode Mode
	}{
		{"${{ [" + strings.Repeat("input.s.slice(0, 30000), ", 512) + `""].length > 0 }}`, Condition},
		{"${{ [" + strings.Repeat("input.s, ", 512) + `""].includes("é") }}`, Condition},
		{"${{ {" + strings.Join(keys, ", ") + "} }}", Select},
		{"${{ [" + strings.Repeat(`input.s === "" ? "" : input.s.slice(0, 30000), `, 512) + `""].length > 0 }}`, Condition},
	} {
		x := mustCheck(t, c.src, c.mode, r)
		x.timeout = time.Minute
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		var err error
		if c.mode == Condition {
			_, err = x.EvalBool(in)
		} else {
			_, err = x.Eval(in)
		}
		runtime.ReadMemStats(&after)
		if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "characters") {
			t.Errorf("%.40s: err=%v, want a fault.Invalid over the string length limit", c.src, err)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 {
			t.Errorf("%.40s: allocated %d MiB, want the literal refused before it runs", c.src, n>>20)
		}
	}
	list := strings.TrimSuffix(strings.Repeat(`"x",`, 40000), ",")
	big := docs("input", `{"s":"é","t":"abc","list":[`+list+`]}`)
	out, err := mustCheck(t, `${{ {up: input.t.toUpperCase(), parts: [input.t, input.t.slice(0, 1), input.s], all: input.list} }}`, Select, r).Eval(big)
	if want := `{"all":[` + list + `],"parts":["abc","a","é"],"up":"ABC"}`; err != nil || string(out) != want {
		t.Fatalf("a literal within the limit that reads a large array: got %.60s err=%v, want %.60s", out, err, want)
	}
	out, err = mustCheck(t, `${{ {all: input.t === "abc" ? input.list : input.list} }}`, Select, r).Eval(big)
	if want := `{"all":[` + list + `]}`; err != nil || string(out) != want {
		t.Fatalf("a literal whose ternary reads a large array: got %.60s err=%v, want %.60s", out, err, want)
	}
}

// Every evaluation binds its documents again, which takes time that grows with them: the Budget's time
// runs from the binding, so evaluations of a short expression over a large document within one Budget
// fail once together they pass it.
func TestBudgetTimeCoversDocumentBinding(t *testing.T) {
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|t": req("string")}}
	in := docs("input", `{"s":"`+strings.Repeat("z", 2<<20)+`","t":"abc"}`)
	x := mustCheck(t, "${{ input.t }}", Select, r)
	b := NewBudget()
	b.time = 50 * time.Millisecond
	var err error
	for i := 0; i < 50 && err == nil; i++ {
		_, err = x.EvalWithin(b, in)
	}
	if err == nil || fault.KindOf(err) != fault.Invalid || !strings.Contains(err.Error(), "time limit") {
		t.Fatalf("50 evaluations over a 2 MiB document within 50 ms: err=%v, want a fault.Invalid at the time limit", err)
	}
}

// A defaulted field is bound once per evaluation, however many references read it: Check records each
// defaulted path once, so binding the documents decodes each default once.
func TestDefaultIsBoundOncePerPath(t *testing.T) {
	def := json.RawMessage(`{"s":"` + strings.Repeat("z", 250<<10) + `"}`)
	r := fakeResolver{roots: []string{"input"}, fields: map[string]Field{"input|big": {Type: "object", HasDefault: true, Default: def}}}
	x := mustCheck(t, "${{ "+strings.Repeat("input.big !== undefined && ", 800)+"true }}", Condition, r)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	ok, err := x.EvalBool(docs("input", `{}`))
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if err != nil || !ok {
		t.Fatalf("800 references to a defaulted field: ok=%v err=%v, want true", ok, err)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 || took > 500*time.Millisecond {
		t.Errorf("800 references to a 250 KiB default: allocated %d MiB in %v, want one decoding of it", n>>20, took)
	}
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
