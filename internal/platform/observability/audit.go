package observability

import (
	"context"
	"io"
	"log/slog"

	"github.com/pyvvo/funcd/api/fault"
)

// AuditDecision is the outcome recorded for a security-relevant action.
type AuditDecision string

const (
	// AuditAllow records that the action was permitted.
	AuditAllow AuditDecision = "allow"
	// AuditDeny records that the action was denied.
	AuditDeny AuditDecision = "deny"
)

// Validate reports whether d is a known decision; unknown maps to fault.Invalid.
func (d AuditDecision) Validate() error {
	switch d {
	case AuditAllow, AuditDeny:
		return nil
	default:
		return fault.Invalidf("observability.AuditDecision.Validate",
			"unknown audit decision %q (want %q or %q)", string(d), AuditAllow, AuditDeny)
	}
}

// AuditEvent is one security-relevant event — who did what to which resource and
// the decision. Typed (no any); it travels the dedicated audit stream, never the
// operational log stream.
type AuditEvent struct {
	Actor    string        // authenticated principal / API key id
	Action   string        // e.g. "function.apply", "egress.connect"
	Resource string        // namespaced resource ref
	Decision AuditDecision // allow | deny
	Reason   string        // optional human reason (e.g. PDP rule)
}

// AuditRecorder writes audit events to a dedicated sink labeled source=audit,
// kept separate from operational logs (blueprint: audit is not ops logging). The
// V1 sink is a structured JSON writer; durable retention is a V2 audit ADR.
type AuditRecorder struct {
	log *slog.Logger
}

// NewAuditRecorder builds an AuditRecorder writing to w (the composition root
// passes the audit stream; tests pass a buffer).
func NewAuditRecorder(w io.Writer) *AuditRecorder {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: ReplaceAttr})
	return &AuditRecorder{log: slog.New(handler).With("source", "audit")}
}

// Record writes one audit event. It returns fault.Invalid if Decision is unknown.
func (r *AuditRecorder) Record(ctx context.Context, e AuditEvent) error {
	if err := e.Decision.Validate(); err != nil {
		return err
	}
	r.log.LogAttrs(ctx, slog.LevelInfo, "audit",
		slog.String("actor", e.Actor),
		slog.String("action", e.Action),
		slog.String("resource", e.Resource),
		slog.String("decision", string(e.Decision)),
		slog.String("reason", e.Reason),
	)
	return nil
}
