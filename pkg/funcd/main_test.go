//go:build !e2e

package funcd_test

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The e2e build has its own TestMain, in app_revision_e2e_test.go, which ends the same way, with no answer check.
func TestMain(m *testing.M) {
	got := &answers{}
	funcd.SetAnswerRecorderForTest(got.record)
	code := m.Run()
	if err := compareAnswers(controlplane.SpecAPI().OpenAPI(), got.seen(), checksAll(code, flagValue)); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		code = 1
	}
	os.Exit(leftOpen(code))
}

// answers are the statuses that each operation gave the tests (ADR-0329 Decision 4).
type answers struct {
	mu  sync.Mutex
	got map[answer]bool
}

type answer struct {
	op     string
	status int
}

func (a *answers) record(op string, status int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.got == nil {
		a.got = map[answer]bool{}
	}
	a.got[answer{op, status}] = true
}

func (a *answers) seen() map[answer]bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return maps.Clone(a.got)
}

// checksAll reports whether TestMain also holds each listed status to a test: only after a run that passed and that
// held every test, with no -run, -skip or -list, as value reads the test flags.
func checksAll(code int, value func(flag string) string) bool {
	if code != 0 {
		return false
	}
	for _, name := range []string{"test.run", "test.skip", "test.list"} {
		if value(name) != "" {
			return false
		}
	}
	return true
}

func flagValue(name string) string {
	if f := flag.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// compareAnswers holds each described operation of doc to the statuses the tests got from it: it fails on a status
// the operation does not list and, when full, on a status it lists that no test got. Each failure names the operation
// and the status. An operation is described when it has a summary and no default response.
func compareAnswers(doc *huma.OpenAPI, got map[answer]bool, full bool) error {
	listed := map[string]map[int]bool{}
	for _, item := range doc.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil || op.Summary == "" || op.Responses["default"] != nil {
				continue
			}
			listed[op.OperationID] = map[int]bool{}
			for code := range op.Responses {
				status, err := strconv.Atoi(code)
				if err != nil {
					return fmt.Errorf("%s lists the response %q, which is no status", op.OperationID, code)
				}
				listed[op.OperationID][status] = true
			}
		}
	}
	var errs []error
	for _, a := range slices.SortedFunc(maps.Keys(got), byAnswer) {
		if statuses, ok := listed[a.op]; ok && !statuses[a.status] {
			errs = append(errs, fmt.Errorf("%s answered %d %s, which it does not list", a.op, a.status,
				http.StatusText(a.status)))
		}
	}
	if full {
		for _, op := range slices.Sorted(maps.Keys(listed)) {
			for _, status := range slices.Sorted(maps.Keys(listed[op])) {
				if !got[answer{op, status}] {
					errs = append(errs, fmt.Errorf("%s lists %d %s, which no test got", op, status,
						http.StatusText(status)))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func byAnswer(a, b answer) int {
	return cmp.Or(cmp.Compare(a.op, b.op), cmp.Compare(a.status, b.status))
}

// provenAnswers are the statuses that the described operations of doc list: the answers of a run that proves them all.
func provenAnswers(t *testing.T, doc *huma.OpenAPI) map[answer]bool {
	t.Helper()
	got := map[answer]bool{}
	for _, item := range doc.Paths {
		for _, op := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete} {
			if op == nil || op.Summary == "" || op.Responses["default"] != nil {
				continue
			}
			for code := range op.Responses {
				status, err := strconv.Atoi(code)
				require.NoError(t, err)
				got[answer{op.OperationID, status}] = true
			}
		}
	}
	require.True(t, got[answer{"replaceKVStore", http.StatusConflict}], "replaceKVStore lists 409")
	return got
}

// scenario: unlisted-answer-fails
func TestScenarioUnlistedAnswerFails(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	got := provenAnswers(t, doc)
	require.NoError(t, compareAnswers(doc, got, true))
	got[answer{"replaceKVStore", http.StatusTeapot}] = true
	got[answer{"listNamespaces", http.StatusTeapot}] = true // no described operation: nothing to compare
	for _, full := range []bool{true, false} {
		err := compareAnswers(doc, got, full)
		require.EqualError(t, err, "replaceKVStore answered 418 I'm a teapot, which it does not list")
	}
}

// scenario: listed-answer-unproven-fails
func TestScenarioListedAnswerUnprovenFails(t *testing.T) {
	doc := controlplane.SpecAPI().OpenAPI()
	got := provenAnswers(t, doc)
	delete(got, answer{"replaceKVStore", http.StatusConflict})
	err := compareAnswers(doc, got, true)
	require.EqualError(t, err, "replaceKVStore lists 409 Conflict, which no test got")
}

// scenario: filtered-run-checks-only-unlisted
func TestScenarioFilteredRunChecksOnlyUnlisted(t *testing.T) {
	none := func(string) string { return "" }
	only := func(name string) func(string) string {
		return func(flag string) string {
			if flag == name {
				return "TestOne"
			}
			return ""
		}
	}
	require.True(t, checksAll(0, none), "a full run that passed")
	require.False(t, checksAll(0, only("test.run")), "a run filtered with -run")
	require.False(t, checksAll(0, only("test.skip")), "a run filtered with -skip")
	require.False(t, checksAll(0, only("test.list")), "a run that lists the tests")
	require.False(t, checksAll(1, none), "a run that failed")

	doc := controlplane.SpecAPI().OpenAPI()
	got := provenAnswers(t, doc)
	delete(got, answer{"replaceKVStore", http.StatusConflict})
	require.NoError(t, compareAnswers(doc, got, false), "an unproven status passes")
	got[answer{"replaceKVStore", http.StatusTeapot}] = true
	require.EqualError(t, compareAnswers(doc, got, false), "replaceKVStore answered 418 I'm a teapot, which it does not list")
}
