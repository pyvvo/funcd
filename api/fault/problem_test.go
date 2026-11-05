package fault

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// scenario: error-maps-to-problem (ADR-0002)
func TestScenario_ErrorMapsToProblem(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
	}{
		{
			name:       "NotFound maps to 404",
			err:        NotFoundf("store.Get", "no function with id %q", "x"),
			wantStatus: http.StatusNotFound,
			wantType:   "urn:funcd:problem:not-found",
		},
		{
			name:       "Invalid maps to 400",
			err:        Invalidf("validate", "name must not be empty"),
			wantStatus: http.StatusBadRequest,
			wantType:   "urn:funcd:problem:invalid",
		},
		{
			name:       "Conflict maps to 409",
			err:        Conflictf("store.Put", "version mismatch"),
			wantStatus: http.StatusConflict,
			wantType:   "urn:funcd:problem:conflict",
		},
		{
			name:       "Unauthorized maps to 401",
			err:        Unauthorizedf("auth", "invalid token"),
			wantStatus: http.StatusUnauthorized,
			wantType:   "urn:funcd:problem:unauthorized",
		},
		{
			name:       "Forbidden maps to 403",
			err:        Forbiddenf("auth", "insufficient permissions"),
			wantStatus: http.StatusForbidden,
			wantType:   "urn:funcd:problem:forbidden",
		},
		{
			name:       "Unavailable maps to 503",
			err:        Unavailablef("store.Connect", "could not reach db"),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "urn:funcd:problem:unavailable",
		},
		{
			name:       "ResourceExhausted maps to 429",
			err:        ResourceExhaustedf("edge.limit", "rate limit exceeded"),
			wantStatus: http.StatusTooManyRequests,
			wantType:   "urn:funcd:problem:resource-exhausted",
		},
		{
			name:       "PayloadTooLarge maps to 413",
			err:        PayloadTooLargef("edge.limit", "body exceeds %d bytes", 1024),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantType:   "urn:funcd:problem:payload-too-large",
		},
		{
			name:       "InsufficientStorage maps to 507",
			err:        InsufficientStoragef("quota.Admit", "bucket default/media holds 1048576 of its 1048576 bytes"),
			wantStatus: http.StatusInsufficientStorage,
			wantType:   "urn:funcd:problem:insufficient-storage",
		},
		{
			name:       "Internal maps to 500",
			err:        Internalf("controller.refresh", "unexpected nil pointer"),
			wantStatus: http.StatusInternalServerError,
			wantType:   "urn:funcd:problem:internal",
		},
		{
			name:       "wrapped error preserves kind mapping",
			err:        fmt.Errorf("handler: %w", NotFoundf("store.Get", "no widget with id %q", "x")),
			wantStatus: http.StatusNotFound,
			wantType:   "urn:funcd:problem:not-found",
		},
		{
			name:       "unknown error defaults to 500",
			err:        errors.New("something went wrong"),
			wantStatus: http.StatusInternalServerError,
			wantType:   "urn:funcd:problem:internal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ToProblem(tt.err)
			if p.Status != tt.wantStatus {
				t.Errorf("Status = %d, want %d", p.Status, tt.wantStatus)
			}
			if p.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", p.Type, tt.wantType)
			}
			if p.Detail == "" {
				t.Error("Detail should not be empty")
			}
		})
	}
}

func TestWriteProblem_SetsHeaders(t *testing.T) {
	// We can't easily test WriteProblem without an httptest.ResponseRecorder,
	// but we can verify ToProblem produces the full struct.
	err := NotFoundf("store.Get", "no widget")
	p := ToProblem(err)
	if p.Title == "" {
		t.Error("Title should not be empty")
	}
	if p.Status == 0 {
		t.Error("Status should not be zero")
	}
}

func TestIssue339_WriteProblemEscapesControlCharacters(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, Invalidf("op", "bad key a\x01b\x1f\"\\\n<&>"))

	body := rec.Body.Bytes()
	if !json.Valid(body) {
		t.Fatalf("problem body is not valid JSON: %q", body)
	}
	var p Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("unmarshal problem body: %v", err)
	}
	if want := ToProblem(Invalidf("op", "bad key a\x01b\x1f\"\\\n<&>")); p != want {
		t.Fatalf("decoded problem = %+v, want %+v", p, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// ADR-0151: a response deadline is a 504 with its own type, distinct from the 503 of a failed wake.
func TestDeadlineExceededMapsTo504(t *testing.T) {
	p := ToProblem(DeadlineExceededf("activator.response-deadline", "default/agent did not start its response within 1s (spec.timeout)"))
	if p.Status != http.StatusGatewayTimeout || p.Type != "urn:funcd:problem:deadline-exceeded" || p.Title != "Gateway Timeout" {
		t.Fatalf("DeadlineExceeded maps to %+v", p)
	}
	if p.Detail != "activator.response-deadline: default/agent did not start its response within 1s (spec.timeout)" {
		t.Fatalf("detail = %q", p.Detail)
	}
}

// A 5xx is the platform's fault: its detail carries the operation and its message, or a fixed text when it has no
// message, and the cause goes to funcd's log.
func TestServerFaultDetailHoldsNoCause(t *testing.T) {
	const fixed = "internal error: the cause is in funcd's log"
	cause := errors.New("stat data/blob/k: permission denied")
	tests := []struct {
		name   string
		err    error
		status int
		detail string
	}{
		{"an Internal fault", Wrapf(cause, Internal, "store.Get", "read %s", "x"), 500, "store.Get: read x"},
		{"a kind taken from a plain cause", Wrapf(cause, KindOf(cause), "dataplane.ServeHTTP", "function default/f"), 500, "dataplane.ServeHTTP: function default/f"},
		{"the outer of two faults", Wrapf(Wrapf(cause, Internal, "badger.Get", "read the key"), Internal, "store.Get", "read x"), 500, "store.Get: read x"},
		{"a fault inside plain wrapping", fmt.Errorf("handler: %w", Wrapf(cause, Internal, "store.Get", "read x")), 500, "store.Get: read x"},
		{"a plain error", cause, 500, fixed},
		{"a fault with no message", &Error{Kind: Internal, Op: "store.Get", Err: cause}, 500, fixed},
		{"a kind the map does not know", &Error{Kind: "other", Op: "op", Msg: "msg", Err: cause}, 500, "op: msg"},
		{"an Unavailable fault", Wrapf(cause, Unavailable, "activator.Wake", "resolve upstream for default/f"), 503, "activator.Wake: resolve upstream for default/f"},
		{"a DeadlineExceeded fault", Wrapf(cause, DeadlineExceeded, "activator.response-deadline", "default/f did not start"), 504, "activator.response-deadline: default/f did not start"},
		{"an InsufficientStorage fault", Wrapf(cause, InsufficientStorage, "badger.Update", "commit"), 507, "badger.Update: commit"},
		{"a fault with no cause", Unavailablef("edge.limit", "in-flight concurrency ceiling reached"), 503, "edge.limit: in-flight concurrency ceiling reached"},
		{"a 503 over a fault that names a state", Wrapf(Unavailablef("storescaler", "function default/f is Failed (ShapeInvalid)"), Unavailable, "activator.activate", "scale up default/f"),
			503, "activator.activate: scale up default/f: storescaler: function default/f is Failed (ShapeInvalid)"},
		{"a 503 over a 503 with a cause", Wrapf(Wrapf(cause, Unavailable, "quota.Admit", "read the quota"), Unavailable, "activator.Wake", "resolve default/f"),
			503, "activator.Wake: resolve default/f: quota.Admit: read the quota"},
		{"a 503 over an Internal fault", Wrapf(Wrapf(cause, Internal, "badger.Get", "read the key"), Unavailable, "activator.Wake", "resolve default/f"),
			503, "activator.Wake: resolve default/f"},
		{"a 503 over a NotFound fault", Wrapf(NotFoundf("artifact.Pull", "fetch x: %v", cause), Unavailable, "contractcheck", "the contract of default/f cannot be read"),
			503, "contractcheck: the contract of default/f cannot be read"},
		{"a 503 over plain wrapping of a fault", Wrapf(fmt.Errorf("retry: %w", Unavailablef("quota.Admit", "measure x")), Unavailable, "op", "msg"),
			503, "op: msg: quota.Admit: measure x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := ToProblem(tt.err)
			if p.Status != tt.status || p.Detail != tt.detail {
				t.Fatalf("problem = %d %q, want %d %q", p.Status, p.Detail, tt.status, tt.detail)
			}
		})
	}
}

// A 4xx is the caller's to correct, so its detail keeps the whole text.
func TestCallerFaultDetailKeepsItsText(t *testing.T) {
	cause := errors.New("line 3: unknown field")
	for _, k := range []Kind{Invalid, NotFound, Conflict, Unauthorized, Forbidden, PayloadTooLarge, ResourceExhausted} {
		err := Wrapf(cause, k, "admission", "Policy %q", "p")
		if got, want := ToProblem(err).Detail, `admission: Policy "p": line 3: unknown field`; got != want {
			t.Errorf("%s detail = %q, want %q", k, got, want)
		}
	}
}
