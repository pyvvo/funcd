// Package controlplane owns the huma API registration, typed operations,
// the Handlers seam, and the api/fault → huma problem+json error bridge (ADR-0005).
//
// ADR-0005 owns the routes/contract; P-L/F07 fills Handlers + middleware/admission.
//
//go:generate go run ./cmd/specgen/ -out ../../api/openapi/funcd.v1alpha1.yaml
package controlplane
