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

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/workernode/local"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// localGot are the errors that the local API's endpoints answered the tests, which the cause matrices read too.
//
//nolint:gochecknoglobals // the test binary's one recorder, installed by TestMain
var localGot = &localErrors{}

// The e2e build has its own TestMain, in app_revision_e2e_test.go, which ends the same way, with no answer check.
func TestMain(m *testing.M) {
	got := &answers{}
	funcd.SetAnswerRecorderForTest(got.record)
	local.SetErrorRecorderForTest(localGot.record)
	code := m.Run()
	full := checksAll(code, flagValue)
	if err := errors.Join(compareAnswers(controlplane.SpecAPI().OpenAPI(), got.seen(), full),
		compareCauses(local.Endpoints(), localGot.seen(), full)); err != nil {
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

// localErrors count the errors that each local API endpoint answered the tests, by cause and kind (ADR-0330 Decision 7).
type localErrors struct {
	mu  sync.Mutex
	got map[localError]int
}

type localError struct {
	pattern string
	cause   *fault.KnownCause
	kind    fault.Kind
}

func (l *localErrors) record(pattern string, cause *fault.KnownCause, kind fault.Kind) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.got == nil {
		l.got = map[localError]int{}
	}
	l.got[localError{pattern, cause, kind}]++
}

func (l *localErrors) seen() map[localError]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return maps.Clone(l.got)
}

// count is how often pattern answered cause under its own kind.
func (l *localErrors) count(pattern string, cause *fault.KnownCause) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.got[localError{pattern, cause, cause.Kind()}]
}

// compareCauses holds each described endpoint to the errors the tests got from it: it fails on an error, not internal,
// with no known cause, a cause that the endpoint does not declare or a kind that is not its cause's and, when full, on a
// declared cause that no test got. Each failure names the endpoint and the cause or the kind.
func compareCauses(endpoints []local.Endpoint, got map[localError]int, full bool) error {
	declared := map[string][]*fault.KnownCause{}
	for _, e := range endpoints {
		if e.Causes != nil {
			declared[e.Pattern] = e.Causes
		}
	}
	var errs []error
	for _, g := range slices.SortedFunc(maps.Keys(got), byLocalError) {
		causes, described := declared[g.pattern]
		switch {
		case !described || g.kind == fault.Internal:
		case g.cause == nil:
			errs = append(errs, fmt.Errorf("%s answered an error of kind %s with no known cause", g.pattern, g.kind))
		case !slices.Contains(causes, g.cause):
			errs = append(errs, fmt.Errorf("%s answered %s, which it does not declare", g.pattern, g.cause.Name()))
		case g.cause.Kind() != g.kind:
			errs = append(errs, fmt.Errorf("%s answered %s with the kind %s, not its own %s", g.pattern, g.cause.Name(),
				g.kind, g.cause.Kind()))
		}
	}
	if full {
		for _, e := range endpoints {
			for _, c := range e.Causes {
				if got[localError{e.Pattern, c, c.Kind()}] == 0 {
					errs = append(errs, fmt.Errorf("%s declares %s, which no test got", e.Pattern, c.Name()))
				}
			}
		}
	}
	return errors.Join(errs...)
}

func byLocalError(a, b localError) int {
	name := func(c *fault.KnownCause) string {
		if c == nil {
			return ""
		}
		return c.Name()
	}
	return cmp.Or(cmp.Compare(a.pattern, b.pattern), cmp.Compare(name(a.cause), name(b.cause)), cmp.Compare(a.kind, b.kind))
}

// provenCauses are the causes that the described endpoints declare: the errors of a run that proves them all.
func provenCauses(t *testing.T, endpoints []local.Endpoint) map[localError]int {
	t.Helper()
	got := map[localError]int{}
	for _, e := range endpoints {
		for _, c := range e.Causes {
			got[localError{e.Pattern, c, c.Kind()}] = 1
		}
	}
	require.Equal(t, 1, got[localError{"PUT /kv/{binding}/{key...}", kvstore.ErrNoBinding, fault.Forbidden}],
		"PUT /kv declares kvstore.no-binding")
	return got
}

// scenario: undeclared-cause-fails-a-test
func TestScenarioUndeclaredCauseFailsATest(t *testing.T) {
	// GET /undescribed stands for an endpoint not yet described, however far the rollout has gone.
	endpoints := append(local.Endpoints(), local.Endpoint{Pattern: "GET /undescribed"})
	got := provenCauses(t, endpoints)
	require.NoError(t, compareCauses(endpoints, got, true))
	const put = "PUT /kv/{binding}/{key...}"
	got[localError{put, local.ErrKeyNotFound, fault.NotFound}]++
	got[localError{put, nil, fault.Conflict}]++
	got[localError{put, kvstore.ErrNoBinding, fault.Conflict}]++
	got[localError{put, nil, fault.Internal}]++                 // internal: implicit on every endpoint
	got[localError{"GET /undescribed", nil, fault.Forbidden}]++ // an endpoint not yet described
	for _, full := range []bool{true, false} {
		require.EqualError(t, compareCauses(endpoints, got, full), put+" answered an error of kind conflict with no known cause\n"+
			put+" answered kvstore.no-binding with the kind conflict, not its own forbidden\n"+
			put+" answered local.key-not-found, which it does not declare")
	}
}

// scenario: declared-cause-unproven-fails
func TestScenarioDeclaredCauseUnprovenFails(t *testing.T) {
	endpoints := local.Endpoints()
	got := provenCauses(t, endpoints)
	delete(got, localError{"GET /kv/{binding}/{key...}", local.ErrKeyNotFound, fault.NotFound})
	require.EqualError(t, compareCauses(endpoints, got, true),
		"GET /kv/{binding}/{key...} declares local.key-not-found, which no test got")
	require.NoError(t, compareCauses(endpoints, got, false), "a filtered run checks only what the tests got")
}
