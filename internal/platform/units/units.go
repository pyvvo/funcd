// Package units parses byte sizes in the binary-suffix grammar a Function's memory hint and funcdctl backup plan's
// --size and --speed share (ADR-0223): an integer with an optional Ki, Mi, Gi or Ti suffix.
package units

import (
	"math"
	"strconv"
	"strings"
)

// ParseBytes parses s ("4Gi", "512Mi", "1024") into bytes; (0, false) on an empty, negative, unparseable or
// overflowing value.
func ParseBytes(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}} {
		if rest, ok := strings.CutSuffix(s, u.suffix); ok {
			s, mult = rest, u.mult
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/mult {
		return 0, false
	}
	return n * mult, true
}
