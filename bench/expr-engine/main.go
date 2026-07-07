// Command expr-engine benchmarks the goja (pure-Go ECMAScript) engine as the
// funcd expression engine, versus the hand-rolled internal/expr baseline measured
// in that package's bench_test.go. The identical representative condition runs in
// both, so the two are directly comparable; see RESULTS.md for the verdict.
//
// Usage:
//
//	go run . goja   [iterations]   # perf lane: parse / compile / cold / warm / RSS
//	go run . inject [iterations]   # design lane: injected methods vs native operators
package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: expr-engine <goja | inject> [iterations]")
		os.Exit(2)
	}
	iters := 20000
	if len(os.Args) > 2 {
		if n, err := strconv.Atoi(os.Args[2]); err == nil {
			iters = n
		}
	}
	switch os.Args[1] {
	case "goja":
		runGoja(iters)
	case "inject":
		runInject(iters)
	default:
		fmt.Fprintf(os.Stderr, "unknown lane %q (want: goja | inject)\n", os.Args[1])
		os.Exit(2)
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
