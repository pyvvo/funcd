package contract

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// WithCallBounds bounds each Validate call (ADR-0303 Decision 6): pattern matching stops after budget, and each
// pattern asked later fails its value, with a root detail naming the budget; a number with more than maxDigits digits,
// or an exponent over maxDigits in magnitude, fails at its path before the engine reads any number. Zero leaves a
// bound off, as ADR-0301's default is.
func WithCallBounds(budget time.Duration, maxDigits int) Option {
	return func(o *options) { o.budget, o.maxDigits = budget, maxDigits }
}

// maxIdle is how many compiled instances a bounded Validator keeps for reuse; a call that finds none compiles one.
const maxIdle = 32

// instance is one compiled side whose pattern engine reads its own deadline, so concurrent calls bound their
// matching apart. A Validate call owns an instance from take to put.
type instance struct {
	schema   *jsonschema.Schema
	deadline atomic.Int64 // UnixNano past which every match fails; 0 is none
	cut      atomic.Bool  // a match failed because the deadline passed
}

// bounds is a bounded Validator's state: the side's JSON, to compile more instances, and the idle ones.
type bounds struct {
	doc  interface{}
	opts options
	idle chan *instance
}

// compileInstance compiles one more instance of a side, whose patterns read its deadline.
func compileInstance(doc interface{}, o options) (*instance, error) {
	in := &instance{}
	sch, err := compileSide(doc, o, func(pattern string) (jsonschema.Regexp, error) {
		re, err := ecmaScript(o.matchTimeout)(pattern)
		if err != nil {
			return nil, err
		}
		return boundedMatch{ecmaPattern: re.(ecmaPattern), in: in}, nil
	})
	in.schema = sch
	return in, err
}

func (b *bounds) take() (*instance, error) {
	select {
	case in := <-b.idle:
		return in, nil
	default:
		return compileInstance(b.doc, b.opts)
	}
}

func (b *bounds) put(in *instance) {
	in.deadline.Store(0)
	in.cut.Store(false)
	select {
	case b.idle <- in:
	default:
	}
}

// boundedMatch wraps a pattern so it fails its value once its instance's deadline passed.
type boundedMatch struct {
	ecmaPattern
	in *instance
}

func (m boundedMatch) MatchString(s string) bool {
	if d := m.in.deadline.Load(); d != 0 && time.Now().UnixNano() > d {
		m.in.cut.Store(true)
		return false
	}
	return m.ecmaPattern.MatchString(s)
}

// outsizedNumbers returns a detail for each number of v with more than limit digits, or an exponent over limit in
// magnitude: the engine's big.Rat reads every digit and expands the exponent.
func outsizedNumbers(v interface{}, at []string, limit int, out []Detail) []Detail {
	switch x := v.(type) {
	case map[string]interface{}:
		for k, e := range x {
			out = outsizedNumbers(e, append(at, k), limit, out)
		}
	case []interface{}:
		for i, e := range x {
			out = outsizedNumbers(e, append(at, strconv.Itoa(i)), limit, out)
		}
	case json.Number:
		mant, exp, _ := strings.Cut(strings.ToLower(string(x)), "e")
		exp = strings.TrimLeft(strings.TrimLeft(exp, "+-"), "0")
		n, err := strconv.Atoi(exp)
		digits := len(mant) - strings.Count(mant, "-") - strings.Count(mant, ".")
		if digits > limit || exp != "" && (err != nil || n > limit) {
			out = append(out, Detail{Path: pointer(at), Message: fmt.Sprintf("the number has more than %d digits or an exponent over %d", limit, limit)})
		}
	}
	return out
}
