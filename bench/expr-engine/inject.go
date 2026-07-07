package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// runInject experiments with HOW to express funcd conditions on goja: can we keep
// the ADR-0094/0095 method vocabulary (greaterThan/isTrue/in/exists) by injecting
// it into the VM, or must we adopt native JS operators? Each experiment prints what
// actually happens — the point is an honest picture before the ADR.
func runInject(iters int) {
	fmt.Println("== goja method-injection experiments ==")
	e1PrototypeMethods()
	e2ExistsGuard()
	e3NativeOperators()
	e4WrapperObjects()
	e5Perf(iters)
}

// bindDocs sets the two root documents as globals.
func bindDocs(vm *goja.Runtime) {
	step := mustParseJSON(`{"stats":{"output":{"rows":1200}}}`)
	input := mustParseJSON(`{"publish":true,"region":"eu"}`)
	_ = vm.Set("step", step)
	_ = vm.Set("input", input)
}

// e1: inject greaterThan/lessThan/isTrue/oneOf via prototype extension.
func e1PrototypeMethods() {
	vm := goja.New()
	bindDocs(vm)
	prelude := `
		Number.prototype.greaterThan = function(n) { return this.valueOf() > n; };
		Number.prototype.lessThan    = function(n) { return this.valueOf() < n; };
		Boolean.prototype.isTrue     = function()  { return this.valueOf() === true; };
		String.prototype.oneOf       = function()  { return Array.prototype.includes.call(arguments, this.valueOf()); };
	`
	if _, err := vm.RunString(prelude); err != nil {
		report("E1 prelude", nil, err)
		return
	}
	v, err := vm.RunString(`step.stats.output.rows.greaterThan(0) && input.publish.isTrue()`)
	report("E1 prototype methods (rows.greaterThan(0) && publish.isTrue())", v, err)
	// A string-membership method:
	v, err = vm.RunString(`input.region.oneOf("us","eu","apac")`)
	report("E1 String.oneOf(...)", v, err)
}

// e2: the exists() guard — the make-or-break case. A method cannot be called on an
// absent (undefined) field, so `.exists()` as a method THROWS exactly when it is
// most needed. The native spelling is `!== undefined`.
func e2ExistsGuard() {
	vm := goja.New()
	bindDocs(vm)
	_, _ = vm.RunString(`Object.prototype.exists = function() { return this !== undefined && this !== null; };`)

	v, err := vm.RunString(`step.stats.output.rows.exists()`) // present → fine
	report("E2 exists() on a PRESENT field", v, err)

	v, err = vm.RunString(`step.stats.output.missing.exists()`) // absent → TypeError
	report("E2 exists() on an ABSENT field (method form)", v, err)

	v, err = vm.RunString(`step.stats.output.missing !== undefined`) // native guard
	report("E2 native guard (missing !== undefined)", v, err)

	// The guarded pattern: native guard short-circuits before the method call.
	v, err = vm.RunString(`(step.stats.output.missing !== undefined) && step.stats.output.missing > 0`)
	report("E2 guarded access (native, short-circuits)", v, err)
}

// e3: native operators — no injection at all. The checker enforces the subset on
// goja's AST; goja just evaluates.
func e3NativeOperators() {
	vm := goja.New()
	bindDocs(vm)
	v, err := vm.RunString(`step.stats.output.rows > 0 && input.publish === true`)
	report("E3 native operators (rows > 0 && publish === true)", v, err)
	// Coercion demo: this is what the CHECKER must reject at reconcile (goja happily runs it).
	v, err = vm.RunString(`input.pubish === true`) // typo → undefined === true → false, no error
	report("E3 typo'd field (JS silently yields)", v, err)
	v, err = vm.RunString(`input.region > 0`) // string > number → coerces, no error
	report("E3 coercing comparison (JS silently yields)", v, err)
}

// e4: wrapper objects — bind values already wrapped with typed methods. Shows the
// navigation problem: only the wrapped leaf has methods; `.rows` off a plain object
// does not.
func e4WrapperObjects() {
	vm := goja.New()
	// A Go-side typed wrapper exposed to JS with methods.
	num := func(n float64) *goja.Object {
		o := vm.NewObject()
		_ = o.Set("greaterThan", func(x float64) bool { return n > x })
		_ = o.Set("value", n)
		return o
	}
	_ = vm.Set("rows", num(1200))
	v, err := vm.RunString(`rows.greaterThan(0)`)
	report("E4 Go-side wrapper (rows.greaterThan(0))", v, err)
	// But nested navigation to reach a wrapper is the awkward part: every leaf of
	// every document would need wrapping at bind time (deep, per-eval cost).
}

// e5: perf — prototype-method style vs native-operator style, warm pooled Runtime.
func e5Perf(iters int) {
	// prototype style
	vmP := goja.New()
	bindDocs(vmP)
	_, _ = vmP.RunString(`Number.prototype.greaterThan = function(n){return this.valueOf()>n;}; Boolean.prototype.isTrue=function(){return this.valueOf()===true;};`)
	progP, _ := goja.Compile("p.js", `step.stats.output.rows.greaterThan(0) && input.publish.isTrue()`, true)
	t0 := time.Now()
	for i := 0; i < iters; i++ {
		if _, err := vmP.RunProgram(progP); err != nil {
			report("E5 proto run", nil, err)
			return
		}
	}
	protoUS := float64(time.Since(t0).Microseconds()) / float64(iters)

	// native style
	vmN := goja.New()
	bindDocs(vmN)
	progN, _ := goja.Compile("n.js", `step.stats.output.rows > 0 && input.publish === true`, true)
	t0 = time.Now()
	for i := 0; i < iters; i++ {
		if _, err := vmN.RunProgram(progN); err != nil {
			report("E5 native run", nil, err)
			return
		}
	}
	nativeUS := float64(time.Since(t0).Microseconds()) / float64(iters)

	fmt.Printf("  E5 warm per-eval: prototype-methods %.2f µs/op  vs  native-operators %.2f µs/op\n", protoUS, nativeUS)
}

func report(label string, v goja.Value, err error) {
	if err != nil {
		fmt.Printf("  %-58s → ERROR: %v\n", label, oneLine(err.Error()))
		return
	}
	b, _ := json.Marshal(v.Export())
	fmt.Printf("  %-58s → %s\n", label, string(b))
}

func oneLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i] + " …"
		}
	}
	return s
}
