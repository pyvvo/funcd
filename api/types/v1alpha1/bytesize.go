package v1alpha1

import (
	"math"
	"regexp"

	"github.com/dustin/go-humanize"

	"github.com/pyvvo/funcd/api/fault"
)

// ByteSize is a size written with an optional binary unit (ADR-0266): "1048576", "512Ki", "2GiB".
type ByteSize string

// byteSizeForm is ADR-0266 Decision 1's grammar: at most 15 digits, so go-humanize's float maths stays exact, and an
// optional binary unit; it keeps out the decimal units, fractions and spaces humanize.ParseBytes also accepts.
var byteSizeForm = regexp.MustCompile(`^[0-9]{1,15}((Ki|Mi|Gi|Ti)B?)?$`)

// Bytes is the size's byte count: Invalid for a string the pattern refuses, else humanize.ParseBytes's count, Invalid
// past math.MaxInt64.
func (s ByteSize) Bytes() (int64, error) {
	const op = "ByteSize.Bytes"
	if !byteSizeForm.MatchString(string(s)) {
		return 0, fault.Invalidf(op, "size %q: want digits and an optional Ki, Mi, Gi or Ti (or KiB, MiB, GiB, TiB)", s)
	}
	n, err := humanize.ParseBytes(string(s))
	if err != nil || n > math.MaxInt64 {
		return 0, fault.Invalidf(op, "size %q is past %d bytes", s, int64(math.MaxInt64))
	}
	return int64(n), nil
}
