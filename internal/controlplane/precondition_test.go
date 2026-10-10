package controlplane

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// ADR-0210 Decision 1: one strong entity-tag or *; anything else is a 400 fault.Invalid, judged on every line.
func TestIfMatchResolve(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		lines []string
		rv    string
		bad   bool
	}{
		"absent":           {},
		"star":             {lines: []string{"*"}},
		"quoted":           {lines: []string{`"42"`}, rv: "42"},
		"quoted with OWS":  {lines: []string{` "T1-130" `}, rv: "T1-130"},
		"weak":             {lines: []string{`W/"42"`}, bad: true},
		"list":             {lines: []string{`"41", "42"`}, bad: true},
		"list with star":   {lines: []string{`*, "42"`}, bad: true},
		"two lines":        {lines: []string{`"42"`, `"41"`}, bad: true},
		"two equal lines":  {lines: []string{`"42"`, `"42"`}, bad: true},
		"unquoted":         {lines: []string{"42"}, bad: true},
		"open quote":       {lines: []string{`"42`}, bad: true},
		"empty":            {lines: []string{""}, bad: true},
		"empty tag":        {lines: []string{`""`}, bad: true},
		"space in the tag": {lines: []string{`"4 2"`}, bad: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodDelete, "/", nil)
			for _, l := range tc.lines {
				req.Header.Add("If-Match", l)
			}
			p := IfMatchParams{rv: "unset"}
			errs := p.Resolve(humatest.NewContext(nil, req, httptest.NewRecorder()))
			if !tc.bad {
				require.Empty(t, errs)
				require.Equal(t, tc.rv, p.rv)
				return
			}
			require.Len(t, errs, 1)
			var fe *faultError
			require.True(t, errors.As(errs[0], &fe), "%T", errs[0])
			require.Equal(t, http.StatusBadRequest, fe.GetStatus())
			require.Equal(t, "unset", p.rv, "a refused precondition sets no version")
		})
	}
}

// ADR-0210 Decision 1: a PUT's version is the header's or the body's, both when equal; different is ambiguous.
func TestReplaceVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ header, body, want string }{
		{"", "", ""},
		{"7", "", "7"},
		{"", "7", "7"},
		{"7", "7", "7"},
	} {
		got, err := replaceVersion(tc.header, tc.body)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	_, err := replaceVersion("6", "7")
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.ErrorContains(t, err, `If-Match "6" and metadata.resourceVersion "7" differ`)
}
