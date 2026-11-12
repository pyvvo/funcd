package v1alpha1

import (
	"math"
	"regexp"

	huma "github.com/danielgtaylor/huma/v2"
	"github.com/dustin/go-humanize"

	"github.com/pyvvo/funcd/api/fault"
)

// ByteSize is a size written with an optional binary unit (ADR-0266): "1048576", "512Ki", "2GiB".
type ByteSize string

// ByteSizePattern is ADR-0266 Decision 1's grammar: at most 15 digits, so go-humanize's float maths stays exact, and an
// optional binary unit; it keeps out the decimal units, fractions and spaces humanize.ParseBytes also accepts.
const ByteSizePattern = `^[0-9]{1,15}((Ki|Mi|Gi|Ti)B?)?$`

// ByteSizeGrammar says ByteSizePattern in words: the schema's patternDescription, which a refusal at the API names.
const ByteSizeGrammar = "a size: 1 to 15 digits and an optional binary unit, Ki, Mi, Gi or Ti, with or without a B (512Ki, 2GiB)"

var byteSizeForm = regexp.MustCompile(ByteSizePattern)

// Schema returns a new schema on each call (huma writes field tags into it), with the byte-size grammar as its pattern
// (ADR-0323 Decision 4), as Duration.Schema does.
func (ByteSize) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Pattern: ByteSizePattern, PatternDescription: ByteSizeGrammar}
}

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
