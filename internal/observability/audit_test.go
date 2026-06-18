package observability_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/observability"
)

type auditLine struct {
	Source   string `json:"source"`
	Msg      string `json:"msg"`
	Actor    string `json:"actor"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Decision string `json:"decision"`
}

// scenario: audit-record — a typed AuditEvent lands on the dedicated source=audit
// sink with its fields; an unknown decision is rejected as fault.Invalid.
func TestScenarioAuditRecord(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	rec := observability.NewAuditRecorder(&buf)

	err := rec.Record(context.Background(), observability.AuditEvent{
		Actor:    "alice",
		Action:   "function.apply",
		Resource: "default/my-fn",
		Decision: observability.AuditAllow,
		Reason:   "rbac:namespace-admin",
	})
	require.NoError(t, err)

	var line auditLine
	require.NoError(t, json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line))
	require.Equal(t, "audit", line.Source, "audit events ride the dedicated source=audit stream")
	require.Equal(t, "audit", line.Msg)
	require.Equal(t, "alice", line.Actor)
	require.Equal(t, "function.apply", line.Action)
	require.Equal(t, "default/my-fn", line.Resource)
	require.Equal(t, "allow", line.Decision)

	// Unknown decision → fault.Invalid, nothing more written.
	before := buf.Len()
	err = rec.Record(context.Background(), observability.AuditEvent{Decision: observability.AuditDecision("maybe")})
	require.Error(t, err)
	require.Equal(t, fault.Invalid, fault.KindOf(err))
	require.Equal(t, before, buf.Len(), "rejected event is not written")
}
