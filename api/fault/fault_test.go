package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Kind
	}{
		{
			name: "nil error returns empty",
			err:  nil,
			want: "",
		},
		{
			name: "direct NotFound",
			err:  NotFoundf("store.Get", "no widget with id %q", "x"),
			want: NotFound,
		},
		{
			name: "direct Invalid",
			err:  Invalidf("validate", "name must not be empty"),
			want: Invalid,
		},
		{
			name: "wrapped NotFound preserves kind",
			err:  fmt.Errorf("render route: %w", NotFoundf("store.Get", "no widget with id %q", "x")),
			want: NotFound,
		},
		{
			name: "double-wrapped Conflict preserves kind",
			err: fmt.Errorf("controller reconcile: %w",
				fmt.Errorf("store update: %w", Conflictf("store.Put", "version mismatch"))),
			want: Conflict,
		},
		{
			name: "Wrapf preserves kind through fmt.Errorf",
			err: fmt.Errorf("outer: %w",
				Wrapf(errors.New("connection refused"), Unavailable, "store.Connect", "could not reach db")),
			want: Unavailable,
		},
		{
			name: "plain error returns Internal",
			err:  errors.New("something broke"),
			want: Internal,
		},
		{
			name: "wrapped plain error still returns Internal",
			err:  fmt.Errorf("outer: %w", errors.New("something broke")),
			want: Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := KindOf(tt.err)
			if got != tt.want {
				t.Errorf("KindOf(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

func TestErrorUnwrap(t *testing.T) {
	cause := errors.New("original")
	ferr := Wrapf(cause, Internal, "op", "msg")

	if !errors.Is(ferr, cause) {
		t.Error("errors.Is should find the wrapped cause")
	}

	var target *Error
	if !errors.As(ferr, &target) {
		t.Error("errors.As should find the *Error")
	}
	if target.Kind != Internal {
		t.Errorf("Kind = %q, want %q", target.Kind, Internal)
	}
}

func TestErrorString(t *testing.T) {
	// With wrapped cause
	ferr := Wrapf(errors.New("connection refused"), Unavailable, "store.Connect", "could not reach db")
	got := ferr.Error()
	if got == "" {
		t.Error("Error() should not be empty")
	}

	// Without wrapped cause
	ferr2 := NotFoundf("store.Get", "no widget")
	got2 := ferr2.Error()
	if got2 == "" {
		t.Error("Error() should not be empty")
	}
}

func TestAllConstructors(t *testing.T) {
	// Verify every constructor returns the right Kind.
	tests := []struct {
		name string
		err  *Error
		want Kind
	}{
		{"NotFoundf", NotFoundf("op", "msg"), NotFound},
		{"Invalidf", Invalidf("op", "msg"), Invalid},
		{"Conflictf", Conflictf("op", "msg"), Conflict},
		{"Unauthorizedf", Unauthorizedf("op", "msg"), Unauthorized},
		{"Forbiddenf", Forbiddenf("op", "msg"), Forbidden},
		{"Unavailablef", Unavailablef("op", "msg"), Unavailable},
		{"Internalf", Internalf("op", "msg"), Internal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Kind != tt.want {
				t.Errorf("Kind = %q, want %q", tt.err.Kind, tt.want)
			}
		})
	}
}
