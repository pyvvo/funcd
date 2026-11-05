package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

const rawCause = "stat data/blob/k: permission denied"

// A handler's plain error answers huma's own text: the error goes to the log.
func TestPlainHandlerErrorAnswersHumasText(t *testing.T) {
	api := NewAPI(chi.NewRouter(), NewStubHandlers())
	huma.Register(api, huma.Operation{OperationID: "plainError", Method: http.MethodGet, Path: "/plain-error"},
		func(context.Context, *struct{}) (*struct{}, error) { return nil, errors.New(rawCause) })
	resp := humatest.Wrap(t, api).Get("/plain-error")
	var p fault.Problem
	if err := json.Unmarshal(resp.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode the problem: %v", err)
	}
	if resp.Code != http.StatusInternalServerError || p.Detail != "unexpected error occurred" {
		t.Fatalf("answer = %d %+v", resp.Code, p)
	}
}

// unmarshalable is an object whose document does not encode, or encodes as what cannot decode.
type unmarshalable struct {
	*v1.Function
	doc []byte
}

func (u unmarshalable) MarshalJSON() ([]byte, error) {
	if u.doc == nil {
		return nil, errors.New(rawCause)
	}
	return u.doc, nil
}

// The JSON round trips of a write hold their error's text out of the Msg, so a 500's detail holds no cause.
func TestRoundTripFaultsHoldNoCause(t *testing.T) {
	fn := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	for _, tc := range []struct {
		name  string
		call  func() error
		cause string
	}{
		{"jsonFields' marshal", func() error { _, err := jsonFields(unmarshalable{Function: fn}); return err }, rawCause},
		{"jsonFields' unmarshal", func() error { _, err := jsonFields(unmarshalable{Function: fn, doc: []byte(`[1]`)}); return err }, "cannot unmarshal"},
		{"withStatus' unmarshal", func() error {
			_, err := withStatus(unmarshalable{Function: fn, doc: []byte(`{"spec": {"timeout": "not-a-duration"}}`)}, nil)
			return err
		}, "not-a-duration"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.cause) {
				t.Fatalf("the error %q has lost its cause %q", err, tc.cause)
			}
			if detail := fault.ToProblem(err).Detail; strings.Contains(detail, tc.cause) {
				t.Fatalf("detail = %q holds the cause %q", detail, tc.cause)
			}
		})
	}
}
