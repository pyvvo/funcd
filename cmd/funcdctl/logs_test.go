package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/auth"
	"github.com/green-0-rabbit/funcd/internal/auth/rbac"
	"github.com/green-0-rabbit/funcd/internal/blob/gocloud"
	"github.com/green-0-rabbit/funcd/internal/controlplane"
	"github.com/green-0-rabbit/funcd/internal/controlplane/middleware"
	"github.com/green-0-rabbit/funcd/internal/funclog/compact"
	"github.com/green-0-rabbit/funcd/internal/funclog/logread"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/store/memory"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// scenario: funcdctl-logs-prints — `funcdctl logs <fn> -n <ns>` prints one line per record, oldest-first.
// Drives the full vertical: a seeded compacted Parquet → logread → control-plane logs route → SDK → CLI.
func TestScenarioFuncdctlLogsPrints(t *testing.T) {
	ctx := context.Background()
	bucket, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bucket.Close() })

	base := time.Date(2026, 6, 29, 10, 30, 0, 0, time.UTC).UnixNano()
	rows := []compact.Row{
		{TimeUnixNano: base, SeverityText: "INFO", SeverityNumber: 9, Body: "first", Namespace: "team-a", Function: "fn", Replica: "0", Source: "console"},
		{TimeUnixNano: base + 1, SeverityText: "WARN", SeverityNumber: 13, Body: "second", Namespace: "team-a", Function: "fn", Replica: "0", Source: "console", AttrsJSON: `{"i":"80","batch":"default"}`},
	}
	var buf bytes.Buffer
	w := parquet.NewGenericWriter[compact.Row](&buf)
	_, err = w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	date := time.Unix(0, base).UTC().Format("2006-01-02")
	require.NoError(t, bucket.Put(ctx, fmt.Sprintf("logs/team-a/fn/%s/%d.parquet", date, base), buf.Bytes()))

	creds := middleware.NewStaticCredentials(map[string]auth.Identity{
		devToken: {Subject: "dev", Role: auth.RoleDeveloper, Namespaces: []v1.NamespaceName{"team-a"}},
	})
	h, err := controlplane.NewServer(controlplane.Deps{
		Store:       store.New(memory.New()),
		Authorizer:  rbac.New(),
		Credentials: creds,
		Logs:        logread.NewBlobReader(bucket),
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := sdk.New(srv.URL, sdk.WithToken(devToken))
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "logs", "fn", "-n", "team-a"))
	got := out.String()
	require.Contains(t, got, "[INFO]")
	require.Contains(t, got, "[WARN]")
	require.Contains(t, got, "first")
	require.Contains(t, got, "second")
	// Oldest-first (tail order): "first" prints before "second".
	require.Less(t, strings.Index(got, "first"), strings.Index(got, "second"))

	// -o wide appends source + the structured attrs inline as key=value (the JSON content, as text).
	var wide bytes.Buffer
	require.NoError(t, execCLI(&wide, c, "logs", "fn", "-n", "team-a", "-o", "wide"))
	wstr := wide.String()
	require.Contains(t, wstr, "source=console")
	require.Contains(t, wstr, "i=80")
	require.Contains(t, wstr, "batch=default")

	// -o json emits one JSON record per line (the full Line DTO).
	var js bytes.Buffer
	require.NoError(t, execCLI(&js, c, "logs", "fn", "-n", "team-a", "-o", "json"))
	require.Contains(t, js.String(), `"severityNumber":13`)
	require.Contains(t, js.String(), `"body":"second"`)
}
