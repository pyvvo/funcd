// Package doctag pins an API field's or a config key's description to the named constants it copies (ADR-0323
// Decision 4): the test of the package that owns a constant names the field and the constant, so that a changed
// constant fails until the description follows.
package doctag

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Names fails t unless the doc tag of owner's field holds each of want, not as part of a longer word or number.
func Names(t testing.TB, owner reflect.Type, field string, want ...string) {
	t.Helper()
	f, ok := owner.FieldByName(field)
	if !ok {
		t.Fatalf("%s has no field %s", owner.Name(), field)
	}
	names(t, owner.Name()+"."+field, f.Tag.Get("doc"), want)
}

// NamesAt is Names for the field at a dotted path of json names under root, such as kvstore.backup.interval or
// auth.credentials[].tokenFile.
func NamesAt(t testing.TB, root reflect.Type, path string, want ...string) {
	t.Helper()
	owner, parts := root, strings.Split(path, ".")
	for i, part := range parts {
		if owner.Kind() != reflect.Struct {
			t.Fatalf("%s has no key %s: %s is not a group of keys", root.Name(), path, strings.Join(parts[:i], "."))
		}
		f, ok := byJSONName(owner, strings.TrimSuffix(part, "[]"))
		if !ok {
			t.Fatalf("%s has no key %s", root.Name(), path)
		}
		if i == len(parts)-1 {
			names(t, path, f.Tag.Get("doc"), want)
			return
		}
		for owner = f.Type; owner.Kind() == reflect.Pointer || owner.Kind() == reflect.Slice; {
			owner = owner.Elem()
		}
	}
}

func byJSONName(owner reflect.Type, name string) (reflect.StructField, bool) {
	for i := range owner.NumField() {
		f := owner.Field(i)
		if n, _, _ := strings.Cut(f.Tag.Get("json"), ","); n == name {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func names(t testing.TB, what, doc string, want []string) {
	t.Helper()
	for _, w := range want {
		if !regexp.MustCompile(`(^|[^\w.])` + regexp.QuoteMeta(w) + `($|[^\w])`).MatchString(doc) {
			t.Errorf("the doc tag of %s does not name %q: %q", what, w, doc)
		}
	}
}
