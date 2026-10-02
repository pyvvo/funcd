package config

import (
	"fmt"
	"maps"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exampleKeyLine matches a yaml key line once a leading "# " is stripped, so commented-out keys count too.
var exampleKeyLine = regexp.MustCompile(`^( *)([A-Za-z0-9]+):(\s|$)`)

type exampleKey struct {
	commented bool
	line      string
}

// exampleKeys returns every key the example file documents, commented out or not, by dotted yaml path.
func exampleKeys(t *testing.T, path string) map[string]exampleKey {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // fixed repo-relative test path
	require.NoError(t, err)
	type frame struct {
		indent int
		name   string
	}
	var stack []frame
	keys := map[string]exampleKey{}
	for line := range strings.SplitSeq(string(data), "\n") {
		text, commented := line, false
		if i := len(line) - len(strings.TrimLeft(line, " ")); strings.HasPrefix(line[i:], "# ") {
			text, commented = line[:i]+line[i+2:], true
		}
		m := exampleKeyLine.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		indent := len(m[1])
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, frame{indent, m[2]})
		names := make([]string, len(stack))
		for i, f := range stack {
			names[i] = f.name
		}
		keys[strings.Join(names, ".")] = exampleKey{commented: commented, line: line}
	}
	return keys
}

// configLeaves collects every leaf key of the struct v by dotted yaml path, with its value.
func configLeaves(prefix string, v reflect.Value, out map[string]reflect.Value) {
	for i := range v.NumField() {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		if f := v.Field(i); f.Kind() == reflect.Struct {
			configLeaves(prefix+name+".", f, out)
		} else {
			out[prefix+name] = f
		}
	}
}

// The example's header claims it documents every key, each commented-out one with its default (issue #341).
func TestIssue341_ExampleDocumentsEveryKeyWithDefault(t *testing.T) {
	documented := exampleKeys(t, "../../../examples/funcdconfig.yaml")
	leaves := map[string]reflect.Value{}
	configLeaves("", reflect.ValueOf(defaults()), leaves)
	for _, path := range slices.Sorted(maps.Keys(leaves)) {
		doc, ok := documented[path]
		if !assert.Truef(t, ok, "config key %q is missing from the example", path) {
			continue
		}
		if doc.commented {
			assert.Containsf(t, doc.line, "default", "config key %q shows no default", path)
		}
		if def := leaves[path]; !def.IsZero() {
			assert.Containsf(t, doc.line, fmt.Sprint(def.Interface()), "config key %q does not show its default", path)
		}
	}
}
