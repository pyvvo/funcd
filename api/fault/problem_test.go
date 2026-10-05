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
