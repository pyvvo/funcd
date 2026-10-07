package observability_test

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/platform/observability"
)

const fixedForm = `[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z`

// scenario: log-line-time-fixed-form — on a UTC+2 host the JSON and text daemon handlers and the audit recorder write
// a record's time in the ADR-0196 form. It swaps time.Local, so it is not parallel.
func TestLogLineTimeFixedForm(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("UTC+2", 2*60*60)
	t.Cleanup(func() { time.Local = saved })

	var js, text, audit bytes.Buffer
	jl, err := observability.NewLogger(observability.Config{Format: observability.FormatJSON}, &js)
	require.NoError(t, err)
	tl, err := observability.NewLogger(observability.Config{Format: observability.FormatText}, &text)
	require.NoError(t, err)
	jl.Root().Info("hello")
	tl.Component("c").Info("hello")
	require.NoError(t, observability.NewAuditRecorder(&audit).Record(context.Background(), observability.AuditEvent{
		Actor: "dev", Action: "function.apply", Resource: "team-a/fn", Decision: observability.AuditAllow,
	}))

	require.Regexp(t, `^\{"time":"`+fixedForm+`",`, js.String())
	require.Regexp(t, `^time=`+fixedForm+` `, text.String())
	require.Regexp(t, `^\{"time":"`+fixedForm+`",`, audit.String())
}

func TestReplaceAttrRewritesOnlyTheRecordTime(t *testing.T) {
	at := time.Date(2026, 10, 7, 22, 3, 35, 965999999, time.FixedZone("UTC+2", 2*60*60))
	got := observability.ReplaceAttr(nil, slog.Time(slog.TimeKey, at))
	require.Equal(t, slog.KindString, got.Value.Kind())
	require.Equal(t, "2026-10-07T20:03:35.965Z", got.Value.String())

	inGroup := observability.ReplaceAttr([]string{"g"}, slog.Time(slog.TimeKey, at))
	require.Equal(t, slog.KindTime, inGroup.Value.Kind(), "a time inside a group keeps its form")
	attr := observability.ReplaceAttr(nil, slog.Time("deadline", at))
	require.Equal(t, slog.KindTime, attr.Value.Kind(), "a time-valued attribute keeps its form")
	require.True(t, regexp.MustCompile(`^`+fixedForm+`$`).MatchString(got.Value.String()))
}
