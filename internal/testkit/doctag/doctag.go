// Package doctag pins an API field's description to the named constants it copies (ADR-0323 Decision 4): the test
// of the package that owns a constant names the field and the constant, so that a changed constant fails until the
// description follows.
package doctag

import (
	"reflect"
	"regexp"
	"testing"
)

// Names fails t unless the doc tag of owner's field holds each of want, not as part of a longer word or number.
func Names(t testing.TB, owner reflect.Type, field string, want ...string) {
	t.Helper()
	f, ok := owner.FieldByName(field)
	if !ok {
		t.Fatalf("%s has no field %s", owner.Name(), field)
	}
	doc := f.Tag.Get("doc")
	for _, w := range want {
		if !regexp.MustCompile(`(^|[^\w.])` + regexp.QuoteMeta(w) + `($|[^\w])`).MatchString(doc) {
			t.Errorf("the doc tag of %s.%s does not name %q: %q", owner.Name(), field, w, doc)
		}
	}
}
