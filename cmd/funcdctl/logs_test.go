package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/auth/rbac"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/controlplane"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/sdk"
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
	require.NoError(t, bucket.Put(ctx, fmt.Sprintf("logs/team-a/fn/%s/%d.parquet", date, base), buf.Bytes(), blob.PutOptions{}))

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

// A record's text fields come from the function, so the default and wide renderings must escape what a
// terminal would act on: one record stays one printed line (ADR-0084) and no control byte reaches the tty.
func TestRenderLogLinesEscapesControlBytes(t *testing.T) {
	at := time.Date(2026, 10, 2, 10, 40, 54, 0, time.UTC)
	lines := []logread.Line{
		{
			Time: at, Severity: "INFO", Replica: "0\r",
			Body:   "multi\n2026-10-02T10:40:54Z [ERROR] 0 forged\x1b]0;title\x07\x00\x9b\u009b\u2028end",
			Source: "console\x1b[2J", Invocation: "inv\n", TraceID: "t\x07",
			Attrs: []byte(`{"k\u001b":"a\u001b[31mb\nc","n":{` + "\n" + `"x":1}}`),
		},
		{Time: at, Severity: "WARN", Replica: "1", Body: `plain "quoted" \path`},
	}
	for _, output := range []string{"", "wide"} {
		var buf bytes.Buffer
		require.NoError(t, (&cli{out: &buf}).renderLogLines(lines, output))
		got := buf.String()
		require.Equal(t, len(lines), strings.Count(got, "\n"), "output %q: one line per record:\n%s", output, got)
		require.True(t, utf8.ValidString(got), "output %q: invalid UTF-8 reaches the terminal", output)
		for _, printed := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
			for _, r := range printed {
				require.False(t, r < 0x20 || (r >= 0x7f && r <= 0x9f) || r == 0x2028, "output %q: raw control %U in %q", output, r, printed)
			}
		}
		require.Contains(t, got, `multi\n2026-10-02T10:40:54Z [ERROR] 0 forged\x1b]0;title\a\x00\x9b\u009b\u2028end`)
		require.Contains(t, got, `[WARN] 1 plain "quoted" \path`+"\n")
		if output == "wide" {
			require.Contains(t, got, `source=console\x1b[2J inv=inv\n trace=t\a`)
			require.Contains(t, got, `k\x1b=a\x1b[31mb\nc n={\n"x":1}`)
		}
	}
}
