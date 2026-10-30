package invoke_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/invoke"
)

// step is a plugin at every point that logs its name and refuses when told.
type step struct {
	name    string
	refuse  bool
	log     *[]string
	applies func(*invoke.Call) bool
}

func (s step) Name() string { return s.name }
func (s step) Applies(c *invoke.Call) bool {
	return s.applies == nil || s.applies(c)
}

func (s step) run(point string) *invoke.Refusal {
	*s.log = append(*s.log, point+":"+s.name)
	if s.refuse {
		return &invoke.Refusal{Err: fault.Forbiddenf("test", "%s refused at %s", s.name, point)}
	}
	return nil
}

func (s step) Admit(context.Context, *invoke.Call) *invoke.Refusal  { return s.run("admit") }
func (s step) Before(context.Context, *invoke.Call) *invoke.Refusal { return s.run("before") }
func (s step) After(context.Context, *invoke.Call, *invoke.Answer) *invoke.Refusal {
	return s.run("after")
}

// Each point runs its plugins in order, and the first refusal stops it.
func TestChainRunsInOrderAndStopsAtTheFirstRefusal(t *testing.T) {
	t.Parallel()
	var log []string
	ch := invoke.NewChain(step{name: "a", log: &log}, step{name: "b", refuse: true, log: &log}, step{name: "c", log: &log})
	c := &invoke.Call{Caller: invoke.External}
	for _, rf := range []*invoke.Refusal{
		ch.Admit(context.Background(), c),
		ch.Before(context.Background(), c),
		ch.After(context.Background(), c, invoke.NewAnswer(&http.Response{StatusCode: 200, Body: http.NoBody})),
	} {
		if rf == nil || fault.KindOf(rf) != fault.Forbidden {
			t.Fatalf("refusal = %v, want b's Forbidden", rf)
		}
	}
	want := []string{"admit:a", "admit:b", "before:a", "before:b", "after:a", "after:b"}
	if !slices.Equal(log, want) {
		t.Fatalf("log = %q, want %q", log, want)
	}
}

// Applies is asked at each point, so a plugin that needs the Function runs once the entry has read it.
func TestChainAsksAppliesAtEachPoint(t *testing.T) {
	t.Parallel()
	var log []string
	ch := invoke.NewChain(step{name: "fn", log: &log, applies: func(c *invoke.Call) bool { return c.Function != nil }})
	c := &invoke.Call{Caller: invoke.External}
	_ = ch.Admit(context.Background(), c)
	c.Function = &v1.Function{}
	_ = ch.Before(context.Background(), c)
	if want := []string{"before:fn"}; !slices.Equal(log, want) {
		t.Fatalf("log = %q, want %q", log, want)
	}
}

func TestNilChainRunsNothing(t *testing.T) {
	t.Parallel()
	var ch *invoke.Chain
	c := &invoke.Call{Caller: invoke.External}
	if ch.Admit(context.Background(), c) != nil || ch.Before(context.Background(), c) != nil ||
		ch.After(context.Background(), c, invoke.NewAnswer(&http.Response{StatusCode: 200, Body: http.NoBody})) != nil {
		t.Fatal("a nil chain refused a call")
	}
}

// Body refuses a body over its limit, reads on for a larger limit, and leaves the whole body to the caller.
func TestAnswerBodyLimitsAndKeepsTheBody(t *testing.T) {
	t.Parallel()
	res := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("hello world"))}
	a := invoke.NewAnswer(res)
	if _, err := a.Body(5); fault.KindOf(err) != fault.PayloadTooLarge {
		t.Fatalf("Body(5) = %v, want PayloadTooLarge", err)
	}
	if b, err := a.Body(100); err != nil || string(b) != "hello world" {
		t.Fatalf("Body(100) = %q, %v", b, err)
	}
	rest, err := io.ReadAll(res.Body)
	if err != nil || string(rest) != "hello world" {
		t.Fatalf("the caller reads %q, %v; want the whole body", rest, err)
	}
}

// A 1xx answer has no body to read: an upgrade's tunnel follows it.
func TestAnswerBodyRefusesA1xx(t *testing.T) {
	t.Parallel()
	a := invoke.NewAnswer(&http.Response{StatusCode: http.StatusSwitchingProtocols, Body: http.NoBody})
	if _, err := a.Body(10); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("Body on a 101 = %v, want Invalid", err)
	}
}

// A refusal answers as a problem from its error, or with its own status, headers and body (Decision 6).
func TestRefusalForms(t *testing.T) {
	t.Parallel()
	problem := &invoke.Refusal{Err: fault.Unavailablef("test", "held"), Header: http.Header{"Retry-After": {"30"}}}
	rec := httptest.NewRecorder()
	problem.WriteAnswer(rec)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "30" || rec.Header().Get("Content-Type") != "application/problem+json" ||
		problem.StatusCode() != 503 || fault.KindOf(problem) != fault.Unavailable || problem.Error() != "test: held" {
		t.Fatalf("problem form: %d %v %q, StatusCode %d, Error %q", rec.Code, rec.Header(), rec.Body, problem.StatusCode(), problem.Error())
	}
	own := &invoke.Refusal{Status: 422, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"error":"x"}`)}
	rec = httptest.NewRecorder()
	own.WriteAnswer(rec)
	if rec.Code != 422 || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != `{"error":"x"}` ||
		own.StatusCode() != 422 || own.Error() != `422 Unprocessable Entity: {"error":"x"}` {
		t.Fatalf("own form: %d %v %q, StatusCode %d, Error %q", rec.Code, rec.Header(), rec.Body, own.StatusCode(), own.Error())
	}
}
