package v1alpha1

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// DurationPattern is the duration grammar (ADR-0194): the single source for ParseDuration and the OpenAPI schema.
const DurationPattern = `^([0-9]+h([0-9]+m)?([0-9]+s)?([0-9]+ms)?|[0-9]+m([0-9]+s)?([0-9]+ms)?|[0-9]+s([0-9]+ms)?|[0-9]+ms)$`

// durationGrammar describes DurationPattern in words: the OpenAPI patternDescription and every parse error.
const durationGrammar = "a duration: whole numbers with the units h, m, s and ms, largest unit first (10m, 1h30m, 500ms)"

// Duration is a span of time: int64 nanoseconds in memory, a duration string on the wire (ADR-0194).
type Duration time.Duration

// MaxDuration is the largest value the grammar admits (2562047h47m16s854ms); hi = MaxDuration means "no upper bound".
const MaxDuration = Duration(math.MaxInt64 - math.MaxInt64%int64(time.Millisecond))

var durationRe = regexp.MustCompile(DurationPattern)

// ParseDuration matches s against DurationPattern, then computes it with time.ParseDuration; a mismatch or an
// overflow is fault.Invalid naming s and the grammar.
func ParseDuration(s string) (Duration, error) {
	const op = "v1alpha1.ParseDuration"
	if !durationRe.MatchString(s) {
		return 0, fault.Invalidf(op, "%q is not %s", s, durationGrammar)
	}
	d, err := time.ParseDuration(s)
	if err != nil || d%time.Millisecond != 0 {
		return 0, fault.Invalidf(op, "%q is not %s: it exceeds %s", s, durationGrammar, MaxDuration)
	}
	return Duration(d), nil
}

// CheckDuration bounds every API duration field and config duration key: fault.Invalid(op) naming field (a field
// path or config key), d and [lo, hi] in the normalized form ("at least lo" when hi is MaxDuration) when d < lo,
// d > hi or d is not a whole millisecond.
func CheckDuration(op, field string, d, lo, hi Duration) error {
	whole := time.Duration(d)%time.Millisecond == 0
	if d >= lo && d <= hi && whole {
		return nil
	}
	bounds := fmt.Sprintf("[%s, %s]", lo, hi)
	if hi == MaxDuration {
		bounds = "at least " + lo.String()
	}
	if !whole {
		return fault.Invalidf(op, "%s %s is not a whole number of milliseconds: want %s", field, d, bounds)
	}
	return fault.Invalidf(op, "%s %s is out of bounds: want %s", field, d, bounds)
}

// String writes the normalized form; a negative or sub-millisecond value falls back to time.Duration's String.
func (d Duration) String() string {
	if d < 0 || time.Duration(d)%time.Millisecond != 0 {
		return time.Duration(d).String()
	}
	if d == 0 {
		return "0s"
	}
	var b strings.Builder
	rest := time.Duration(d)
	for _, u := range []struct {
		unit time.Duration
		name string
	}{{time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}, {time.Millisecond, "ms"}} {
		if n := rest / u.unit; n > 0 {
			b.WriteString(strconv.FormatInt(int64(n), 10))
			b.WriteString(u.name)
			rest -= n * u.unit
		}
	}
	return b.String()
}

// MarshalJSON writes the normalized form as a JSON string; a negative or sub-millisecond value is refused.
func (d Duration) MarshalJSON() ([]byte, error) {
	if d < 0 || time.Duration(d)%time.Millisecond != 0 {
		return nil, fault.Invalidf("v1alpha1.Duration.MarshalJSON", "%s is not %s", time.Duration(d), durationGrammar)
	}
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts only a JSON string in the grammar; null leaves d unchanged.
func (d *Duration) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var s string
	if len(b) == 0 || b[0] != '"' || json.Unmarshal(b, &s) != nil {
		return fault.Invalidf("v1alpha1.Duration.UnmarshalJSON", "%s is not a JSON string: want %s", b, durationGrammar)
	}
	v, err := ParseDuration(s)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// Schema returns a new schema on each call (huma writes field tags into it): {type: string, pattern:
// DurationPattern, patternDescription}.
func (Duration) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Pattern: DurationPattern, PatternDescription: durationGrammar}
}
