package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	accepted = "# ADR\n\n- **Status**: Implemented 2026-01-01\n\n## Context\n"
	goodRow  = "| A call | Held | `invoke.activationTimeout` 30 s | 503 | decided | ADR-0001 Decision 1, `TestHeld` |\n"
	goodCode = `package invoke

import "time"

const heldIdleTimeout = 5 * time.Minute

func wait() time.Duration { return heldIdleTimeout }
`
)

const callsDoc = docDir + "/calls.md"

// tree is a small checkout: two chapters, two packages, two config keys, two kinds, one accepted ADR and one test.
type tree struct {
	t  *testing.T
	in inputs
}

func newTree(t *testing.T, rows string) tree {
	t.Helper()
	root := t.TempDir()
	tr := tree{t, inputs{
		root:     root,
		chapters: []chapter{{"Calls", []string{"internal/invoke"}, []string{"invoke"}}, {"Workers", []string{"internal/runtime"}, []string{"runtime"}}},
		keys:     map[string]string{"invoke.activationTimeout": "30s", "runtime.bootTimeout": "1m0s"},
		kinds:    []string{"Route", "WorkerNode"},
	}}
	tr.write("docs/adr/0001-x.md", accepted)
	tr.write("internal/invoke/invoke.go", goodCode)
	tr.write("internal/invoke/invoke_test.go", "package invoke\n\nimport \"testing\"\n\nfunc TestHeld(t *testing.T) {}\n")
	tr.write("internal/runtime/runtime.go", "package runtime\n\nimport \"time\"\n\nconst killWait = 10 * time.Second\n")
	tr.write(docDir+"/"+indexFile, "# Platform behavior\n\n[Calls](calls.md)\n[Workers](workers.md)\n")
	tr.write(docDir+"/workers.md", chapterText("Workers", "WorkerNode", ""))
	tr.write(callsDoc, chapterText("Calls", "Route", rows))
	require.NoError(t, run(tr.in, false))
	return tr
}

func chapterText(name, kinds, rows string) string {
	return "# " + name + "\n\nSee [the index](README.md).\n\nKinds: " + kinds + ".\n\n```mermaid\nstateDiagram-v2\n    [*] --> Held\n```\n\nA note. New words: Held.\n\n" +
		rowHeader + "\n|---|---|---|---|---|---|\n" + rows + "\n"
}

func (tr tree) write(path, text string) {
	tr.t.Helper()
	full := filepath.Join(tr.in.root, path)
	require.NoError(tr.t, os.MkdirAll(filepath.Dir(full), 0o750))
	require.NoError(tr.t, os.WriteFile(full, []byte(text), 0o600))
}

func (tr tree) read(path string) string {
	tr.t.Helper()
	data, err := os.ReadFile(filepath.Join(tr.in.root, path))
	require.NoError(tr.t, err)
	return string(data)
}

// edit changes the written file, keeping its generated block.
func (tr tree) edit(old, replacement string) {
	tr.t.Helper()
	text := tr.read(callsDoc)
	require.Contains(tr.t, text, old)
	tr.write(callsDoc, strings.Replace(text, old, replacement, 1))
}

func (tr tree) checkFails(want ...string) {
	tr.t.Helper()
	err := run(tr.in, true)
	require.Error(tr.t, err)
	for _, w := range want {
		require.Contains(tr.t, err.Error(), w)
	}
	require.True(tr.t, strings.HasSuffix(err.Error(), skillHint), "the last line names the skill: %s", err)
}

func TestScenarioValueChanged(t *testing.T) {
	tr := newTree(t, goodRow)
	require.NoError(t, run(tr.in, true))

	tr.write("internal/invoke/invoke.go", strings.Replace(goodCode, "5 * time.Minute", "6 * time.Minute", 1))
	tr.checkFails("now: | `heldIdleTimeout` | 6m |", "was: | `heldIdleTimeout` | 5m |")

	before := strings.Split(tr.read(callsDoc), blockBegin)[0]
	require.NoError(t, run(tr.in, false))
	require.Equal(t, before, strings.Split(tr.read(callsDoc), blockBegin)[0], "regenerating changes only the blocks")
	require.NoError(t, run(tr.in, true))

	tr.in.keys["invoke.activationTimeout"] = "45s"
	require.NoError(t, run(tr.in, false))
	tr.checkFails("invoke.activationTimeout is 45s in the code, not 30 s")
}

func TestScenarioLineMoved(t *testing.T) {
	tr := newTree(t, goodRow)
	tr.write("internal/invoke/invoke.go", strings.Replace(goodCode, "const heldIdleTimeout", "// A comment moves it.\nconst heldIdleTimeout", 1)+"\nfunc other() int { return 1 }\n")
	require.NoError(t, run(tr.in, true))
}

func TestScenarioInlineNumber(t *testing.T) {
	refused := map[string]string{
		"a duration":        `func f() { _, _ = context.WithTimeout(context.Background(), 30*time.Second) }`,
		"a size":            `func f() { _ = make([]byte, 1<<20) }`,
		"a kibi size":       `func f() { _ = make([]byte, 64*1024) }`,
		"a negative shift":  `func f() { _ = -1 << 31 }`,
		"a bare argument":   `func f() { time.Sleep(time.Second) }`,
		"a bare field":      `func f() { _ = http.Server{ReadTimeout: time.Minute} }`,
		"a bare assignment": `func f() { d := time.Hour; _ = d }`,
		"a bare return":     `func f() time.Duration { return time.Second }`,
		"a bare comparison": `func f(d time.Duration) bool { return d < time.Second }`,
		"a local const":     `func f() { const wait = 5 * time.Second; _ = wait }`,
		"a function value":  `var f = func() time.Duration { return 5 * time.Second }`,
		"a truncated day":   `func f(d time.Duration) time.Duration { return d.Truncate(24 * time.Hour) }`,
	}
	for name, code := range refused {
		t.Run(name, func(t *testing.T) {
			tr := newTree(t, goodRow)
			tr.write("internal/invoke/extra.go", "package invoke\n\n"+code+"\n")
			tr.checkFails("internal/invoke/extra.go:3: f() writes the number", "inline: give it a name at package level")
		})
	}
	allowed := map[string]string{
		"a package-level value": `const wait = 5 * time.Second`,
		"arithmetic on a unit":  `func f(d time.Duration) int64 { return int64(d / time.Second) }`,
		"a conversion":          `func f() float64 { return float64(time.Second) }`,
		"rounding":              `func f(d time.Duration) time.Duration { return d.Round(time.Second) }`,
		"a small unit":          `func f(d time.Duration) time.Duration { return max(d, time.Millisecond) }`,
		"beside its key":        `func f() { parse("invoke.activationTimeout", 30*time.Second) }`,
		"beside its key, bare":  `func f() { _ = field{"runtime.bootTimeout", time.Minute} }`,
		"a plain number":        `func f() int { return 64 }`,
		"a table of units":      `func f() { _ = []time.Duration{time.Hour, time.Minute} }`,
	}
	for name, code := range allowed {
		t.Run(name, func(t *testing.T) {
			tr := newTree(t, goodRow)
			tr.write("internal/invoke/extra.go", "package invoke\n\n"+code+"\n")
			require.NoError(t, run(tr.in, false))
			require.NoError(t, run(tr.in, true))
		})
	}
	t.Run("a fallback that differs from its key is refused", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.write("internal/invoke/extra.go", "package invoke\n\nfunc f() { parse(\"invoke.activationTimeout\", 20*time.Second) }\n")
		tr.checkFails("internal/invoke/extra.go:3: f() falls back to 20s for invoke.activationTimeout, whose default is 30s: one key has one default")
	})
	t.Run("a positional unit that differs from its key is refused", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.write("internal/invoke/extra.go", "package invoke\n\nfunc f() { _ = field{\"invoke.activationTimeout\", time.Second} }\n")
		tr.checkFails("falls back to 1s for invoke.activationTimeout, whose default is 30s")
	})
	t.Run("a config key with no default is refused", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.in.keys["invoke.nothing"] = noDefault
		require.NoError(t, run(tr.in, false))
		tr.checkFails("config key invoke.nothing has no default")
	})
}

func TestScenarioNewComponent(t *testing.T) {
	t.Run("a package", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.write("internal/vector/vector.go", "package vector\n")
		tr.checkFails("package internal/vector has no chapter")
		require.Error(t, run(tr.in, false), "regenerating fails too")
	})
	t.Run("a config section", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.in.keys["vector.limit"] = "3"
		tr.checkFails("config section vector has no chapter")
	})
	t.Run("a kind", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.in.kinds = append(tr.in.kinds, "Vector")
		tr.checkFails("kind Vector is on no chapter's Kinds line")
	})
	t.Run("a file with no chapter", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.write(docDir+"/vectors.md", "# Vectors\n")
		tr.checkFails(docDir + "/vectors.md is in no chapter")
	})
	t.Run("a chapter with no file", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.in.chapters = append(tr.in.chapters, chapter{name: "Vectors"})
		tr.checkFails(`chapter "Vectors" has no file`)
		require.Error(t, run(tr.in, false), "regenerating fails too")
	})
	t.Run("a chapter the index does not list", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.write(docDir+"/"+indexFile, "# Platform behavior\n\n[Calls](calls.md)\n")
		tr.checkFails("does not list chapter Workers")
	})
}

func TestChapterShape(t *testing.T) {
	cases := map[string]struct{ old, replacement, want string }{
		"no title":      {"# Calls\n", "", "chapter Calls has no title"},
		"no index link": {"See [the index](README.md).\n", "", "chapter Calls has no link to the index"},
		"no Kinds line": {"Kinds: Route.\n", "", "chapter Calls has no Kinds: line"},
		"no machine":    {"```mermaid\nstateDiagram-v2\n    [*] --> Held\n```\n", "", "chapter Calls has no machine"},
		"no table":      {rowHeader + "\n|---|---|---|---|---|---|\n" + goodRow, "", "chapter Calls has no table"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr := newTree(t, goodRow)
			tr.edit(c.old, c.replacement)
			tr.checkFails(c.want)
		})
	}
	t.Run("a machine's word the code does not have", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.edit("    [*] --> Held\n", "    [*] --> Waiting : a call\n    Waiting --> heldIdleTimeout\n")
		tr.checkFails("the machine's state Waiting is not in the code and the note does not mark it new")
		tr.edit("New words: Held.\n", "New words: Waiting.\n")
		require.NoError(t, run(tr.in, true))
	})
	t.Run("a title after the Kinds line", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.edit("# Calls\n", "")
		tr.edit("Kinds: Route.\n", "Kinds: Route.\n\n# Calls\n")
		tr.checkFails("does not follow the order")
	})
	t.Run("the table before the machine", func(t *testing.T) {
		tr := newTree(t, goodRow)
		machine := "```mermaid\nstateDiagram-v2\n    [*] --> Held\n```\n"
		tr.edit(machine, "")
		text := tr.read(callsDoc)
		tr.write(callsDoc, strings.Replace(text, blockBegin, machine+"\n"+blockBegin, 1))
		tr.checkFails("does not follow the order")
	})
	t.Run("over the cap", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.edit("A note. New words: Held.\n", strings.Repeat("A note. New words: Held.\n", maxChapter/10))
		tr.checkFails("over the cap of 16384: cut a row or split the chapter")
	})
	t.Run("a second table", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.edit(goodRow, goodRow+"\n"+rowHeader+"\n|---|---|---|---|---|---|\n")
		tr.checkFails("a second table")
	})
	t.Run("a row outside the table", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.edit("A note. New words: Held.\n", "A note. New words: Held.\n\n| a | stray | row |\n")
		tr.checkFails("a table row outside the chapter's table")
	})
	t.Run("a generated block out of order", func(t *testing.T) {
		tr := newTree(t, goodRow)
		tr.write("internal/invoke/a.go", "package invoke\n\nimport \"time\"\n\nconst firstWait = time.Second\n")
		require.NoError(t, run(tr.in, false))
		text := tr.read(callsDoc)
		first, second := "| `firstWait` | 1s | `internal/invoke/a.go` |\n", "| `heldIdleTimeout` | 5m | `internal/invoke/invoke.go` |\n"
		require.Contains(t, text, first+second)
		tr.write(callsDoc, strings.Replace(text, first+second, second+first, 1))
		tr.checkFails("out of order or repeats a row")
	})
}

func TestScenarioRowDoesNotHold(t *testing.T) {
	cases := map[string]struct{ row, want string }{
		"a missing ADR":               {"| A | B | none | C | decided | ADR-0009 Decision 1 |\n", "ADR-0009 does not exist"},
		"a superseded ADR, by Status": {"| A | B | none | C | decided | ADR-0002 Decision 1 |\n", "ADR-0002 is superseded"},
		"a superseded ADR, by header": {"| A | B | none | C | decided | ADR-0003 Decision 1 |\n", "ADR-0003 is superseded"},
		"an ADR not accepted":         {"| A | B | none | C | decided | ADR-0004 Decision 1 |\n", "ADR-0004 is not accepted: Proposed"},
		"a missing test":              {"| A | B | none | C | decided | ADR-0001, `TestGone` |\n", "no test named TestGone"},
		"a missing reason":            {"| A | B | none | `NoSuchReason` | decided | ADR-0001 |\n", "called NoSuchReason"},
		"a one-word reason":           {"| A | B | none | `Gibberish` | decided | ADR-0001 |\n", "called Gibberish"},
		"a missing link":              {"| A | [spec](../design/gone.md) | none | C | decided | ADR-0001 |\n", "links to ../design/gone.md"},
		"a bare link":                 {"| A | [Workers](#workers) | none | C | decided | ADR-0001 |\n", "a bare # link"},
		"a neighbor's value differs":  {"| A | B | `killWait` 9 s | C | decided | ADR-0001 |\n", "killWait is 10s in the code, not 9 s"},
		"accepted on an issue alone":  {"| A | B | none | C | accepted | #90 |\n", "cites no ADR, no test"},
		"a number that differs":       {"| A | B | `invoke.activationTimeout` 20 s | C | decided | ADR-0001 |\n", "invoke.activationTimeout is 30s in the code, not 20 s"},
		"a named value that differs":  {"| A | B | `heldIdleTimeout` 6 min | C | decided | ADR-0001 |\n", "heldIdleTimeout is 5m in the code, not 6 min"},
		"an unknown name":             {"| A | B | `noSuchKey` 6 min | C | decided | ADR-0001 |\n", "cites noSuchKey 6 min: no config key or value has that name"},
		"five cells":                  {"| A | B | none | decided | ADR-0001 |\n", "a row has 5 cells, not 6"},
		"an unknown status":           {"| A | B | none | C | maybe | ADR-0001 |\n", "status maybe is not"},
		"decided on a test alone":     {"| A | B | none | C | decided | `TestHeld` |\n", "cites no ADR, and no dated decider"},
		"a decider with no record":    {"| A | B | none | C | decided | decider 2026-11-04 |\n", "cites no ADR, and no dated decider"},
		"accepted with no source":     {"| A | B | none | C | accepted | a talk |\n", "cites no ADR, no test"},
		"a row over the cap":          {"| A | " + strings.Repeat("B", maxRow) + " | none | C | decided | ADR-0001 |\n", "over the cap of 400"},
		"deferred with a bare card":   {"| A | B | none | C | deferred | a card |\n", "cites no card"},
		"deferred with no holder":     {"| A | B | none | C | deferred | ADR-0001 |\n", "cites no card"},
		"uncovered with no source":    {"| A | B | none | C | **uncovered** |  |\n", "does not say what was read"},
		"differs with no clause":      {"| A | B | none | C | **differs** | the code |\n", "cites no ADR and not the blueprint"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tr := newTree(t, goodRow)
			tr.write("docs/adr/0002-x.md", "# ADR\n\n- **Status**: Superseded by ADR-0001\n\n## Context\n")
			tr.write("docs/adr/0003-x.md", "# ADR\n\n- **Status**: Implemented\n- **Superseded by**: ADR-0001\n\n## Context\n")
			tr.write("docs/adr/0004-x.md", "# ADR\n\n- **Status**: Proposed 2026-01-01\n\n## Context\n")
			tr.edit(goodRow, c.row)
			tr.checkFails(c.want)
			err := run(tr.in, true)
			require.Contains(t, err.Error(), callsDoc+":", "names the line")
		})
	}
	good := map[string]string{
		"a dated decider with an issue": "| A | B | none | C | decided | decider 2026-11-04, #1391 |\n",
		"a dated decider with a card":   "| A | B | none | C | accepted | decider 2026-11-04, card \"Answer fast\" |\n",
		"deferred to a card":            "| A | B | none | C | deferred | card \"Answer fast\" |\n",
		"deferred to an open question":  "| A | B | none | C | deferred | ADR-0001 Open questions |\n",
		"deferred to an issue":          "| A | B | none | C | deferred | #1391 |\n",
		"uncovered, bold":               "| A | B | none | C | **uncovered** | read ADR-0001 |\n",
		"differs, citing its clause":    "| A | B | none | C | **differs** | ADR-0001 Decision 1 says otherwise |\n",
		"a partly superseded ADR":       "| A | B | none | C | decided | ADR-0005 Decision 1 |\n",
		"a pipe in a cell":              "| A | B `x\\|y` | none | C | decided | ADR-0001 |\n",
		"a value by its name":           "| A | B | `heldIdleTimeout` 5 min | C | decided | ADR-0001 |\n",
		"accepted on a test":            "| A | B | none | C | accepted | `TestHeld` |\n",
		"a neighbor's value":            "| A | B | `killWait` 10 s | C | decided | ADR-0001 |\n",
		"a neighbor's file":             "| A | [Workers](workers.md) | none | C | decided | ADR-0001 |\n",
		"a word the note marks new":     "| A | B | none | `Held` | decided | ADR-0001 |\n",
		"a colon after a unit":          "| A | `invoke.activationTimeout` 30 s: held | none | C | decided | ADR-0001 |\n",
		"an address is no number":       "| A | B `invoke.activationTimeout` 127.0.0.1:0 | none | C | decided | ADR-0001 |\n",
		"a name in the code":            "| A | `heldIdleTimeout` runs | none | C | decided | ADR-0001 |\n",
	}
	for name, row := range good {
		t.Run(name, func(t *testing.T) {
			tr := newTree(t, goodRow)
			tr.write("docs/adr/0005-x.md", "# ADR\n\n- **Status**: Implemented\n- **Superseded in part by**: ADR-0001\n\n## Context\n")
			tr.edit(goodRow, row)
			require.NoError(t, run(tr.in, true))
		})
	}
}

func TestScenarioParallelPrs(t *testing.T) {
	tr := newTree(t, goodRow)
	tr.write("internal/invoke/a.go", "package invoke\n\nimport \"time\"\n\nconst firstWait = time.Second\n")
	tr.write("internal/invoke/b.go", "package invoke\n\nimport \"time\"\n\nconst secondWait = time.Minute\n")
	tr.write(callsDoc, strings.Replace(tr.read(callsDoc), "<summary>Calls: values</summary>", "<<<<<<< ours\n<summary>Calls: values</summary>\n>>>>>>> theirs", 1))
	require.NoError(t, run(tr.in, false), "a conflict inside a block clears by regenerating")
	require.NotContains(t, tr.read(callsDoc), "<<<<<<<")
	require.NoError(t, run(tr.in, true))
}

func TestScenarioAdrStatesItsRows(t *testing.T) {
	tr := newTree(t, goodRow)
	tr.write("docs/adr/0314-x.md", accepted)
	tr.checkFails("docs/adr/0314-x.md has no Behavior rows section")
	tr.write("docs/adr/0314-x.md", accepted+"\n## Behavior rows\n\nNone: nothing changes.\n")
	require.NoError(t, run(tr.in, true))
	tr.write("docs/adr/0313-x.md", accepted)
	require.NoError(t, run(tr.in, true), "older ADRs carry no section")
}

func TestScenarioSurpriseLookedUp(t *testing.T) {
	data, err := os.ReadFile("../../" + docDir + "/functions.md")
	require.NoError(t, err)
	functions := string(data)
	require.Contains(t, functions, "`invoke.activationTimeout` 30 s", "a 503 after 30 s has a row")
	require.Contains(t, functions, "ADR-0161 Decision 4", "and the row says what to read next")
	require.Regexp(t, "The gate clears under a held call.*uncovered", functions, "the case nothing speaks of has its own row")
	index, err := os.ReadFile("../../" + docDir + "/" + indexFile)
	require.NoError(t, err)
	require.Contains(t, string(index), "grep -rl", "the index starts a lookup with a grep")
	rule, err := os.ReadFile("../../.claude/CLAUDE.md")
	require.NoError(t, err)
	require.Contains(t, string(rule), "docs/behavior/", "the bug rule starts there")
}

// The rule runs while a change is written, not only in CI: at each commit and in the lint recipe.
func TestTheRuleRunsAtEachStep(t *testing.T) {
	hook, err := os.ReadFile("../../lefthook.yml")
	require.NoError(t, err)
	require.Contains(t, string(hook), "go run ./scripts/behavior -check")
	recipes, err := os.ReadFile("../../justfile")
	require.NoError(t, err)
	require.Contains(t, string(recipes), "lint: check-behavior")
	require.Regexp(t, "(?m)^ci: .*check-behavior", string(recipes))
}

func TestValues(t *testing.T) {
	code := `package invoke

import "time"

const (
	aWait       = 5 * time.Minute
	aSize       = 64 << 20
	aKibiSize   = 64 * 1024
	maxCopies   = 1024
	listPage    = 500
	plainPort   = 8080
	derivedMax  = maxCopies * 2
	derivedTimeout = aWait * 2
	aName       = "not a number"
	heldRetryAfter = "30"
	defaultMemory  = "256Mi"
	aVersion       = "1.0"
)

var (
	retryLimit = compute()
	notAValue  = []int{1}
)
`
	tr := newTree(t, goodRow)
	tr.write("internal/invoke/values.go", code)
	require.NoError(t, run(tr.in, false))
	block := tr.read(callsDoc)
	for _, want := range []string{
		"| `aWait` | 5m |", "| `aSize` | 64MiB |", "| `aKibiSize` | 64KiB |", "| `maxCopies` | 1024 |", "| `listPage` | 500 |",
		"| `derivedMax` | `maxCopies * 2` |", "| `derivedTimeout` | `aWait * 2` |",
		"| `heldRetryAfter` | 30 |", "| `defaultMemory` | 256Mi |",
	} {
		require.Contains(t, block, want)
	}
	for _, not := range []string{"plainPort", "aName", "aVersion", "retryLimit", "notAValue"} {
		require.NotContains(t, block, not)
	}
}

func TestSkippedFiles(t *testing.T) {
	inline := "package invoke\n\nimport \"testing\"\n\nvar _ testing.T\n\nfunc f() { _ = 5 * time.Second }\n"
	tr := newTree(t, goodRow)
	tr.write("internal/invoke/suite.go", inline)
	tr.write("internal/invoke/x_test.go", "package invoke\n\nfunc g() { _ = 5 * time.Second }\n")
	tr.write("internal/invoke/gen.go", "// Code generated by x. DO NOT EDIT.\n\npackage invoke\n\nfunc h() { _ = 5 * time.Second }\n")
	tr.write("internal/invoke/bench.go", "package invoke\n\nfunc i() { _ = 5 * time.Second }\n")
	tr.write("e2e/x.go", "package e2e\n\nfunc j() { _ = 5 * time.Second }\n")
	require.NoError(t, run(tr.in, true))
}

func TestNameTest(t *testing.T) {
	for _, name := range []string{"defaultX", "maxX", "minX", "limitX", "graceX", "timeoutX", "intervalX", "retryX", "retriesX", "attemptsX", "budgetX",
		"xMax", "xLimit", "xTimeout", "xInterval", "xGrace", "xWait", "xPeriod", "xAfter", "xBudget", "xAttempts", "xRetries", "xBytes", "xSize", "xTTL", "xPage", "xBatch", "xConcurrency", "xConns", "xBuf"} {
		require.True(t, nameTest.MatchString(name), name)
	}
	for _, name := range []string{"port", "formatVersion", "flagBit", "engineStarts"} {
		require.False(t, nameTest.MatchString(name), name)
	}
}

func TestFormats(t *testing.T) {
	require.Equal(t, "1h", formatDuration(3600*1e9))
	require.Equal(t, "5m", formatDuration(300*1e9))
	require.Equal(t, "1m30s", formatDuration(90*1e9))
	require.Equal(t, "256MiB", formatSize(256<<20))
	require.Equal(t, "1025", formatSize(1025))
	require.True(t, sameValue("30s, or three supervisionPeriod when larger", "30", "s"))
	require.True(t, sameValue("5m", "5", "min"))
	require.True(t, sameValue("1048576", "1048576", ""))
	require.False(t, sameValue("5m", "6", "min"))
	require.True(t, sameValue("512Mi", "512", "MiB"))
	require.True(t, sameValue("0s", "0", ""))
}
