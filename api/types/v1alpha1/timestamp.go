package v1alpha1

import (
	"encoding/json"
	"regexp"
	"time"

	huma "github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
)

// TimestampLayout is the one timestamp form (ADR-0196), applied to a UTC time.
const TimestampLayout = "2006-01-02T15:04:05.000Z07:00"

// TimestampPattern is TimestampLayout's RE2 form: the single source for UnmarshalJSON and the OpenAPI schema.
const TimestampPattern = `^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`

// TimestampForm describes TimestampPattern in words: the OpenAPI patternDescription and every decode error.
const TimestampForm = "an RFC3339 UTC timestamp with exactly 3 fractional digits (2026-10-07T20:03:35.965Z)"

var timestampRe = regexp.MustCompile(TimestampPattern)

// Timestamp is an instant: a time.Time in memory, TimestampLayout in UTC on the wire (ADR-0196).
type Timestamp time.Time

// NewTimestamp returns t in UTC, truncated to the millisecond.
func NewTimestamp(t time.Time) Timestamp {
	return Timestamp(t.UTC().Truncate(time.Millisecond))
}

// IsZero reports whether t is the zero instant; omitzero uses it.
func (t Timestamp) IsZero() bool { return time.Time(t).IsZero() }

// String writes the form; UTC and truncation apply again so a converted value cannot escape it.
func (t Timestamp) String() string {
	return time.Time(NewTimestamp(time.Time(t))).Format(TimestampLayout)
}

// MarshalJSON writes the form as a JSON string; a year outside 0000–9999 is refused.
func (t Timestamp) MarshalJSON() ([]byte, error) {
	if y := time.Time(t).UTC().Year(); y < 0 || y > 9999 {
		return nil, fault.Invalidf("v1alpha1.Timestamp.MarshalJSON", "year %d is outside 0000-9999: want %s", y, TimestampForm)
	}
	return json.Marshal(t.String())
}

// UnmarshalJSON accepts only a JSON string matching TimestampPattern that parses with TimestampLayout; null leaves t
// unchanged.
func (t *Timestamp) UnmarshalJSON(b []byte) error {
	const op = "v1alpha1.Timestamp.UnmarshalJSON"
	if string(b) == "null" {
		return nil
	}
	var s string
	if len(b) == 0 || b[0] != '"' || json.Unmarshal(b, &s) != nil {
		return fault.Invalidf(op, "%s is not a JSON string: want %s", b, TimestampForm)
	}
	if !timestampRe.MatchString(s) {
		return fault.Invalidf(op, "%q is not %s", s, TimestampForm)
	}
	v, err := time.Parse(TimestampLayout, s)
	if err != nil {
		return fault.Invalidf(op, "%q is not %s: %v", s, TimestampForm, err)
	}
	*t = Timestamp(v)
	return nil
}

// Schema returns a new schema on each call (huma writes field tags into it): {type: string, format: date-time,
// pattern: TimestampPattern, patternDescription}.
func (Timestamp) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{
		Type:               huma.TypeString,
		Format:             "date-time",
		Pattern:            TimestampPattern,
		PatternDescription: TimestampForm,
	}
}
