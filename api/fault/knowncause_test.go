package fault

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
)

// scenario: cause-keeps-the-detail
// An error made from a known cause answers the same problem, byte for byte, as the bare constructor it replaces, and
// errors.Is finds the cause through any wrapping.
func TestScenarioCauseKeepsTheDetail(t *testing.T) {
	lower := errors.New("disk said no")
	for _, k := range []Kind{Invalid, NotFound, Forbidden, PayloadTooLarge, PreconditionFailed, Unavailable,
		InsufficientStorage, Internal} {
		c := NewKnownCause(k, "test.cause", "It happens.", "Do this.")
		for name, pair := range map[string][2]error{
			"Errorf": {c.Errorf("test.op", "key %q of %d bytes", "k", 3), &Error{Kind: k, Op: "test.op", Msg: `key "k" of 3 bytes`}},
			"Wrapf":  {c.Wrapf(lower, "test.op", "commit %s", "now"), Wrapf(lower, k, "test.op", "commit %s", "now")},
		} {
			got, want := answer(pair[0]), answer(pair[1])
			if got != want {
				t.Errorf("%s %s: the answer changed:\n got %s\nwant %s", k, name, got, want)
			}
			if pair[0].Error() != pair[1].Error() {
				t.Errorf("%s %s: Error() prints %q, want %q", k, name, pair[0].Error(), pair[1].Error())
			}
			if KindOf(pair[0]) != k || KnownCauseOf(pair[0]) != c {
				t.Errorf("%s %s: kind %s and cause %v, want %s and %s", k, name, KindOf(pair[0]), KnownCauseOf(pair[0]), k, c)
			}
		}
	}

	inner := NewKnownCause(NotFound, "test.inner", "It is gone.", "Make it.")
	outer := NewKnownCause(Forbidden, "test.outer", "It is not yours.", "Ask for it.")
	other := NewKnownCause(NotFound, "test.other", "It never was.", "Make it.")
	raised := inner.Errorf("test.get", "no %s", "x")
	for name, err := range map[string]error{
		"raised":                     raised,
		"behind %w":                  fmt.Errorf("render: %w", raised),
		"behind a Wrapf":             Wrapf(raised, Unavailable, "test.call", "call it"),
		"behind another known cause": outer.Wrapf(raised, "test.call", "call it"),
		"behind a joined error":      errors.Join(errors.New("first"), fmt.Errorf("second: %w", raised)),
	} {
		if !errors.Is(err, inner) {
			t.Errorf("%s: errors.Is does not find the cause", name)
		}
		if errors.Is(err, other) {
			t.Errorf("%s: errors.Is finds a cause it was not raised from", name)
		}
	}
	if got := KnownCauseOf(outer.Wrapf(raised, "test.call", "call it")); got != outer {
		t.Errorf("KnownCauseOf is %v, want the outermost cause %v", got, outer)
	}
	if got := KnownCauseOf(Wrapf(raised, Unavailable, "test.call", "call it")); got != inner {
		t.Errorf("KnownCauseOf is %v, want %v through an Error with no cause", got, inner)
	}
	for _, err := range []error{nil, lower, NotFoundf("test.get", "no x")} {
		if got := KnownCauseOf(err); got != nil {
			t.Errorf("KnownCauseOf(%v) is %v, want nil", err, got)
		}
	}
	if inner.Error() != "test.inner" || inner.Name() != "test.inner" || inner.Kind() != NotFound ||
		inner.When() != "It is gone." || inner.Fix() != "Make it." {
		t.Errorf("the cause's accessors do not return what NewKnownCause took: %+v", inner)
	}
}

// answer is the response WriteProblem writes for err: its status, content type and body.
func answer(err error) string {
	rec := httptest.NewRecorder()
	WriteProblem(rec, err)
	return fmt.Sprintf("%d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
}
