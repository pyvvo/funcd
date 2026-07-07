package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"time"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"
)

// runGoja benchmarks the goja (pure-Go ECMAScript) engine as the expression
// evaluator, on the same representative condition as the internal/expr baseline
// and the QuickJS/wasm lane:
//
//	step.stats.output.rows > 0 && input.publish === true
//
// It measures the numbers a reconcile-time-checked, goja-evaluated engine would
// pay:
//   - parse (goja/parser AST — the reconcile-time input to funcd's type-checker);
//   - program compile (goja.Compile, once per Workflow revision);
//   - cold eval        — fresh Runtime per evaluation (max isolation);
//   - warm eval        — pooled Runtime, per-eval global rebinding (the realistic
//     production shape: one Runtime per engine goroutine);
//   - per-Runtime heap — the cost of a Runtime pool on a RAM-bound host;
//   - RSS at exit.
func runGoja(iters int) {
	const src = `step.stats.output.rows > 0 && input.publish === true`

	// Documents arrive as JSON (as in the engine); bind them as nested globals.
	stepDoc := mustParseJSON(`{"stats":{"output":{"rows":1200}}}`)
	inputDoc := mustParseJSON(`{"publish":true,"region":"eu"}`)

	// --- parse: the AST funcd's type-checker would walk at reconcile ---
	t0 := time.Now()
	parseIters := 20000
	for i := 0; i < parseIters; i++ {
		if _, err := parser.ParseFile(nil, "expr.js", src, 0); err != nil {
			fatal("parse: %v", err)
		}
	}
	parseUS := float64(time.Since(t0).Microseconds()) / float64(parseIters)

	// --- compile once (per Workflow revision) ---
	t0 = time.Now()
	prog, err := goja.Compile("expr.js", src, true)
	if err != nil {
		fatal("compile: %v", err)
	}
	compileUS := float64(time.Since(t0).Microseconds())

	// --- cold: fresh Runtime per eval ---
	t0 = time.Now()
	coldIters := min(iters, 5000) // Runtime creation is the dominant cost; keep the lane short
	for i := 0; i < coldIters; i++ {
		vm := goja.New()
		bind(vm, stepDoc, inputDoc)
		v, err := vm.RunProgram(prog)
		if err != nil || !v.ToBoolean() {
			fatal("cold eval %d: %v (%v)", i, err, v)
		}
	}
	coldUS := float64(time.Since(t0).Microseconds()) / float64(coldIters)

	// --- warm: one pooled Runtime, rebind globals per eval ---
	vm := goja.New()
	t0 = time.Now()
	for i := 0; i < iters; i++ {
		bind(vm, stepDoc, inputDoc)
		v, err := vm.RunProgram(prog)
		if err != nil || !v.ToBoolean() {
			fatal("warm eval %d: %v (%v)", i, err, v)
		}
	}
	elapsed := time.Since(t0)
	warmUS := float64(elapsed.Microseconds()) / float64(iters)
	throughput := float64(iters) / elapsed.Seconds()

	// --- per-Runtime heap: retain a pool of 256 used Runtimes, measure the delta ---
	perRuntimeKB := measureRuntimeHeapKB(256, prog, stepDoc, inputDoc)

	res := map[string]any{
		"stack":               "goja (pure-Go ECMAScript)",
		"parse_ast_us":        round2(parseUS),
		"compile_program_us":  round2(compileUS),
		"cold_per_eval_us":    round2(coldUS),
		"warm_per_eval_us":    round2(warmUS),
		"throughput_eval_s":   int(throughput),
		"per_runtime_heap_KB": round2(perRuntimeKB),
		"iterations":          iters,
		"rss_MB":              round2(processRSSMB()),
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
}

// bind sets the condition's root documents as globals on the runtime.
func bind(vm *goja.Runtime, stepDoc, inputDoc map[string]any) {
	_ = vm.Set("step", stepDoc)
	_ = vm.Set("input", inputDoc)
}

func mustParseJSON(s string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		fatal("parse doc: %v", err)
	}
	return m
}

// measureRuntimeHeapKB retains n Runtimes that have each evaluated the program
// once (goja initializes lazily — an unused Runtime is nearly free) and reports
// the average heap growth per Runtime — the marginal cost of each pool slot.
func measureRuntimeHeapKB(n int, prog *goja.Program, stepDoc, inputDoc map[string]any) float64 {
	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	pool := make([]*goja.Runtime, n)
	for i := range pool {
		pool[i] = goja.New()
		bind(pool[i], stepDoc, inputDoc)
		if _, err := pool[i].RunProgram(prog); err != nil {
			fatal("pool eval: %v", err)
		}
	}
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	delta := int64(after.HeapAlloc) - int64(before.HeapAlloc) // signed: GC can shrink the heap
	if delta < 0 {
		delta = 0
	}
	perRuntime := float64(delta) / float64(n) / 1024
	runtime.KeepAlive(pool) // the pool must survive both ReadMemStats calls
	return perRuntime
}
