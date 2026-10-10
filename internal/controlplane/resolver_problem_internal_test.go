package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/go-chi/chi/v5"

	"github.com/pyvvo/funcd/api/fault"
)

type issue877Input struct {
	Faults int `query:"faults"`
}

func (in *issue877Input) Resolve(huma.Context) []error {
	errs := make([]error, 0, in.Faults)
	for i := range in.Faults {
		errs = append(errs, wrapFaultError(fault.Invalidf("controlplane.issue877", "bad tag %d", i)))
	}
	return errs
}

// TestIssue877_ResolverFaultKeepsProblemType: a typed fault from a huma input resolver keeps its problem type
// and detail (ADR-0005 §4); several resolver faults at once have no single Kind, so they join under about:blank.
func TestIssue877_ResolverFaultKeepsProblemType(t *testing.T) {
	api := NewAPI(chi.NewRouter(), NewStubHandlers())
	huma.Register(api, huma.Operation{OperationID: "issue877", Method: http.MethodGet, Path: "/issue877"},
		func(context.Context, *issue877Input) (*struct{}, error) { return &struct{}{}, nil })
	ta := humatest.Wrap(t, api)

	for _, tc := range []struct {
		faults int
		want   fault.Problem
	}{
		{1, fault.Problem{Type: "urn:funcd:problem:invalid", Status: http.StatusBadRequest, Detail: "controlplane.issue877: bad tag 0"}},
		{2, fault.Problem{Type: "about:blank", Status: http.StatusBadRequest, Detail: "validation failed: controlplane.issue877: bad tag 0; controlplane.issue877: bad tag 1"}},
	} {
		resp := ta.Get("/issue877?faults=" + strconv.Itoa(tc.faults))
		var p fault.Problem
		if err := json.Unmarshal(resp.Body.Bytes(), &p); err != nil {
			t.Fatalf("faults=%d: decode problem: %v", tc.faults, err)
		}
		if resp.Code != tc.want.Status || p.Type != tc.want.Type || p.Status != tc.want.Status || p.Detail != tc.want.Detail {
			t.Errorf("faults=%d: want %d %+v, got %d %+v", tc.faults, tc.want.Status, tc.want, resp.Code, p)
		}
	}
}
