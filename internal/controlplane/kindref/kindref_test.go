package kindref

import (
	"errors"
	"flag"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane"
)

//nolint:gochecknoglobals // the -update flag of this package's tests
var update = flag.Bool("update", false, "rewrite testdata/undescribed.txt from the API document")

const listFile = "testdata/undescribed.txt"

// scenario: spec-tab-generated
func TestScenarioSpecTabGenerated(t *testing.T) {
	pages, err := Render(controlplane.SpecAPI().OpenAPI())
	require.NoError(t, err)
	require.Contains(t, pages, CommonPage)
	kv := string(pages["develop/services/kv/spec.md"])
	require.True(t, strings.HasPrefix(kv, "---\n"+Marker+"\n"), kv)
	require.Contains(t, kv, "| `maxKeyBytes` | integer, 0 to 64000 |")
	require.Contains(t, kv, "### `KVStore.status`")
	require.Contains(t, kv, "](../../common-definitions.md#objectmeta)")
	require.Contains(t, kv, "](../../../platform/services/kv.md)")
	require.Contains(t, kv, "| `conditions` | array of [Condition](../../common-definitions.md#condition)")
	require.Contains(t, kv, "## KVStore\n\n::include[../../../../../api/types/v1alpha1/examples/kv/kvstore.yaml#kvstore]")
	require.Contains(t, kv, "## Handler API")
	require.Contains(t, kv, "### `ctx.kv`\n\n#### TypeScript")
	require.Contains(t, kv, "| `getText(binding: string, key: string): Promise<string \\| null>` |")
	require.Contains(t, kv, "#### Python")
	require.Contains(t, kv, "| `get_str(binding: str, key: str) -> str \\| None` |")
}

// TestEveryHandlerMethodIsDescribed holds every client and method that a Spec page lists under its Handler API to a
// doc written for users, in each language: present, and naming no ADR.
func TestEveryHandlerMethodIsDescribed(t *testing.T) {
	adr := regexp.MustCompile(`\bADR-\d{4}\b`)
	for feature, names := range clients {
		for _, name := range names {
			views, err := handlerClients(name, languages)
			require.NoError(t, err)
			for i, c := range views {
				where := languages[i].name + " " + ctxName(name) + " (" + feature + ")"
				require.NotEmpty(t, c.Doc, "%s: the client has no doc", where)
				require.NotRegexp(t, adr, c.Doc, "%s: a doc is written for users and names no ADR", where)
				for _, m := range c.Methods {
					require.NotEmpty(t, m.Doc, "%s: %s has no doc", where, m.Name)
					require.NotRegexp(t, adr, m.Doc, "%s: %s's doc names an ADR", where, m.Name)
				}
			}
		}
	}
}

func TestProseFitsATableCell(t *testing.T) {
	for in, want := range map[string]string{
		"a | b\n  c":                      `a \| b c`,
		"x < y > z, {a}":                  "x &lt; y &gt; z, &#123;a&#125;",
		"`get(k) <T> | null {}` then <b>": "`get(k) <T> \\| null {}` then &lt;b&gt;",
		"``{\"columns\": [x]}`` {y}":      "``{\"columns\": [x]}`` &#123;y&#125;",
		"``a ` b`` <c>":                   "``a ` b`` &lt;c&gt;",
		"``a ``` b`` <c> `d`":             "``a ``` b`` &lt;c&gt; `d`",
		"it`s <here>":                     "it`s &lt;here&gt;",
	} {
		require.Equal(t, want, prose(in), "prose(%q)", in)
	}
}

func TestHandlerSectionGroupsOverloads(t *testing.T) {
	api := []byte(`{"clients": [{"name": "", "doc": "The call's context.", "methods": [
		{"name": "log", "signature": "log(msg: string): void", "doc": ""},
		{"name": "log", "signature": "log(level: Level, msg: string): void", "doc": "Writes a line."},
		{"name": "put", "signature": "put(k: string): void", "doc": "First."},
		{"name": "put", "signature": "put(k: string, v: string): void", "doc": "Second."}]}]}`)
	lines, err := handlerSection([]string{""}, []language{{"TypeScript", api}})
	require.NoError(t, err)
	page := strings.Join(lines, "\n")
	require.Contains(t, page, "### `ctx`\n\n#### TypeScript\n\nThe call's context.")
	require.Contains(t, page, "| `log(msg: string): void` or `log(level: Level, msg: string): void` | Writes a line. |")
	require.Contains(t, page, "| `put(k: string): void` or `put(k: string, v: string): void` | First. |")
	require.NotContains(t, page, "Second.")
	_, err = handlerSection([]string{"kv"}, []language{{"TypeScript", api}})
	require.ErrorContains(t, err, "the TypeScript shim has no ctx.kv")
}

// scenario: every-kind-in-one-feature
func TestScenarioEveryKindInOneFeature(t *testing.T) {
	listed := map[v1.Kind]int{}
	for _, f := range Features {
		for _, k := range f.Kinds {
			listed[k]++
		}
	}
	for _, k := range Omitted {
		listed[k]++
	}
	for _, k := range v1.AllKinds() {
		require.Equal(t, 1, listed[k], "kind %s: one feature lists it, or Omitted does", k)
		delete(listed, k)
	}
	require.Empty(t, listed, "kinds that v1.AllKinds() does not return")
}

// TestEveryFieldIsDescribed holds every field a Spec page shows to a description, but the fields that
// testdata/undescribed.txt lists; that list only shrinks (ADR-0323 Decision 2). go test -run TestEveryFieldIsDescribed
// -update rewrites it.
func TestEveryFieldIsDescribed(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	got := Undescribed(doc)
	if *update {
		require.NoError(t, os.WriteFile(listFile, []byte(strings.Join(got, "\n")+"\n"), 0o644))
		return
	}
	require.NoError(t, check(got, readList(t)))
	_, fields, err := render(doc.Components.Schemas.Map(), Features)
	require.NoError(t, err)
	for path, desc := range fields {
		require.NotContains(t, desc, "ADR-", "%s: a description is written for users and names no ADR", path)
	}
}

// scenario: undescribed-field-refused
func TestScenarioUndescribedFieldRefused(t *testing.T) {
	got := undescribedOf(t, reflect.TypeFor[testKind]())
	require.ErrorContains(t, check(got, nil), "TestKindSpec.newField")
}

// scenario: undescribed-list-only-shrinks
func TestScenarioUndescribedListOnlyShrinks(t *testing.T) {
	got := undescribedOf(t, reflect.TypeFor[testKind]())
	err := check(got, []string{"TestKindSpec.newField", "TestKindSpec.described"})
	require.ErrorContains(t, err, "TestKindSpec.described")
}

type testKindSpec struct {
	Described string `json:"described" doc:"A field with a description."`
	NewField  string `json:"newField"`
}

type testKind struct {
	Spec testKindSpec `json:"spec"`
}

// undescribedOf renders a Spec page for one test kind, with no common definitions, and returns its undescribed fields.
func undescribedOf(t *testing.T, kind reflect.Type) []string {
	t.Helper()
	registry := huma.NewMapRegistry("#/components/schemas/", huma.DefaultSchemaNamer)
	registry.Schema(kind, true, "")
	schemas := registry.Map()
	for _, name := range common {
		schemas[name] = &huma.Schema{Type: huma.TypeObject}
	}
	name := huma.DefaultSchemaNamer(kind, "")
	_, fields, err := render(schemas, []Feature{{Path: "test/kind", Kinds: []v1.Kind{v1.Kind(name)}}})
	require.NoError(t, err)
	return undescribed(fields)
}

// check fails on an undescribed field that the list does not hold, and on a listed field that is no longer
// undescribed: the list only shrinks.
func check(got, listed []string) error {
	var problems []string
	if missing := slices.DeleteFunc(slices.Clone(got), func(f string) bool { return slices.Contains(listed, f) }); len(missing) > 0 {
		problems = append(problems, "fields with no description, which need a doc tag written for users: "+strings.Join(missing, ", "))
	}
	if stale := slices.DeleteFunc(slices.Clone(listed), func(f string) bool { return slices.Contains(got, f) }); len(stale) > 0 {
		problems = append(problems, "listed fields that have a description or are gone, to remove from "+listFile+": "+strings.Join(stale, ", "))
	}
	if len(problems) == 0 {
		return nil
	}
	return errors.New(strings.Join(problems, "; "))
}

func readList(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(listFile)
	require.NoError(t, err)
	return slices.DeleteFunc(strings.Split(strings.TrimSpace(string(b)), "\n"), func(s string) bool { return s == "" })
}
