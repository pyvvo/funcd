package v1alpha1

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	huma "github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// Decision 2's examples: ParseDuration and the published pattern agree on every one.
func TestParseDurationGrammar(t *testing.T) {
	re := regexp.MustCompile(DurationPattern)
	valid := map[string]time.Duration{
		"10m":                 10 * time.Minute,
		"1h30m":               90 * time.Minute,
		"90s":                 90 * time.Second,
		"500ms":               500 * time.Millisecond,
		"1s500ms":             1500 * time.Millisecond,
		"0s":                  0,
		"1h1ms":               time.Hour + time.Millisecond,
		"2562047h47m16s854ms": time.Duration(MaxDuration),
	}
	for s, want := range valid {
		d, err := ParseDuration(s)
		require.NoError(t, err, s)
		require.Equal(t, want, time.Duration(d), s)
		require.True(t, re.MatchString(s), "pattern refuses %q", s)
	}
	for _, s := range []string{"1.5s", "-5m", "10", "1d", "500us", "500µs", "10ns", "30m1h", "1m1m", "", "0", " 1s", "1s ", "+1s", "1H"} {
		_, err := ParseDuration(s)
		require.Error(t, err, s)
		require.Equal(t, fault.Invalid, fault.KindOf(err), s)
		require.ErrorContains(t, err, "units h, m, s and ms", s)
		require.False(t, re.MatchString(s), "pattern admits %q", s)
	}
}

// The overflow pair: the largest value parses, one millisecond more is refused although it matches the pattern.
func TestParseDurationOverflow(t *testing.T) {
	require.Equal(t, "2562047h47m16s854ms", MaxDuration.String())
	d, err := ParseDuration(MaxDuration.String())
	require.NoError(t, err)
	require.Equal(t, MaxDuration, d)
	for _, s := range []string{"2562047h47m16s855ms", "99999999999999999999h"} {
		require.True(t, regexp.MustCompile(DurationPattern).MatchString(s))
		_, err = ParseDuration(s)
		require.Equal(t, fault.Invalid, fault.KindOf(err), s)
	}
}

func TestCheckDurationMessages(t *testing.T) {
	require.NoError(t, CheckDuration("op", "spec.timeout", Duration(time.Hour), 0, Duration(time.Hour)))
	err := CheckDuration("op", "spec.timeout", Duration(2*time.Hour), 0, Duration(time.Hour))
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, "spec.timeout 2h is out of bounds: want [0s, 1h]")
	err = CheckDuration("op", "runtime.supervisionPeriod", 0, Duration(time.Millisecond), MaxDuration)
	require.ErrorContains(t, err, "runtime.supervisionPeriod 0s is out of bounds: want at least 1ms")
	err = CheckDuration("op", "spec.timeout", Duration(1500*time.Microsecond), 0, Duration(time.Hour))
	require.ErrorContains(t, err, "spec.timeout 1.5ms is not a whole number of milliseconds: want [0s, 1h]")
	require.Error(t, CheckDuration("op", "f", -1, 0, MaxDuration))
}

func TestDurationStringRoundTrips(t *testing.T) {
	for in, want := range map[string]string{
		"90m": "1h30m", "1500ms": "1s500ms", "3600s": "1h", "90s": "1m30s", "0s": "0s", "1h0m0s": "1h",
		"25h": "25h", "61m1ms": "1h1m1ms", "1000ms": "1s",
	} {
		d, err := ParseDuration(in)
		require.NoError(t, err, in)
		require.Equal(t, want, d.String(), in)
		back, err := ParseDuration(d.String())
		require.NoError(t, err)
		require.Equal(t, d, back)
	}
	require.Equal(t, "-5m0s", Duration(-5*time.Minute).String())
	require.Equal(t, "1.5µs", Duration(1500).String())
}

func TestDurationMarshalJSON(t *testing.T) {
	b, err := json.Marshal(struct {
		D Duration `json:"d"`
		Z Duration `json:"z,omitempty"`
	}{D: Duration(90 * time.Minute)})
	require.NoError(t, err)
	require.JSONEq(t, `{"d":"1h30m"}`, string(b))
	for _, bad := range []Duration{-1, Duration(time.Microsecond), Duration(-time.Second)} {
		_, err := json.Marshal(bad)
		require.Error(t, err, bad)
	}
}

func TestDurationUnmarshalJSON(t *testing.T) {
	var d Duration
	require.NoError(t, json.Unmarshal([]byte(`"1h30m"`), &d))
	require.Equal(t, Duration(90*time.Minute), d)
	require.NoError(t, json.Unmarshal([]byte(`null`), &d))
	require.Equal(t, Duration(90*time.Minute), d, "null leaves the value unchanged")
	for _, bad := range []string{`30`, `30000000000`, `1.5`, `true`, `"30"`, `"1.5s"`, `"2562047h47m16s855ms"`, `{}`} {
		err := json.Unmarshal([]byte(bad), &d)
		require.Error(t, err, bad)
		require.ErrorContains(t, err, "units h, m, s and ms", bad)
	}
}

func TestDurationSchema(t *testing.T) {
	a, b := Duration(0).Schema(nil), Duration(0).Schema(nil)
	require.NotSame(t, a, b, "huma writes field tags into the schema: each call returns a new one")
	require.Equal(t, huma.TypeString, a.Type)
	require.Equal(t, DurationPattern, a.Pattern)
	require.True(t, strings.Contains(a.PatternDescription, "units h, m, s and ms"))
	require.Nil(t, a.Minimum)
	require.Nil(t, a.Maximum)
	require.Empty(t, a.Format)
}
