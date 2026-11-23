package funcd

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// ADR-0342: an engine's output is kept and read as a Function's logs.

// engineLogsRig is a platform whose runtime hands it the workers' output, over a bucket the test reads, with the
// daemon's log in a buffer.
func engineLogsRig(t *testing.T, opts ...Option) (*Platform, *outputRuntime, blob.Bucket, *syncBuffer) {
	t.Helper()
	mem, err := gocloud.Open(context.Background(), "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	logs := &syncBuffer{}
	rt := &outputRuntime{captureRuntime: captureRuntime{Runtime: process.New(nil)}}
	p, err := New(append([]Option{InMemory(), WithRuntime(rt), WithBlob(&logKeysBucket{Bucket: mem}), WithoutLogCompaction(),
		WithLogger(slog.New(slog.NewJSONHandler(logs, nil)))}, opts...)...)
	require.NoError(t, err)
	require.NotNil(t, rt.outputs, "the platform installs its raw-output hook")
	return p, rt, mem, logs
}

// write hands the platform one worker's output, then ends it.
func (o *outputRuntime) write(spec runtime.WorkerSpec, stdout, stderr string) {
	out := workerpipe.New(workerpipe.Options{})
	o.outputs(spec, out)
	_, _ = out.Writer(workerpipe.Stdout).Write([]byte(stdout))
	_, _ = out.Writer(workerpipe.Stderr).Write([]byte(stderr))
	out.Close()
}

func readLines(t *testing.T, b blob.Bucket, q logread.Query) []logread.Line {
	t.Helper()
	q.Namespace = "default"
	lines, err := logread.NewBlobReader(b).Read(context.Background(), q)
	require.NoError(t, err)
	return lines
}

func bodiesOf(lines []logread.Line) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Body)
	}
	return out
}

// identityLogin is the worker of IdentityProvider login's engine.
func identityLogin() runtime.WorkerSpec {
	return runtime.WorkerSpec{Namespace: "default", Name: "login", OwnerKind: v1.KindAddonObject, ServedKind: "IdentityProvider"}
}

// scenario: engine-logs-addon-object — an IdentityProvider's engine line is kept at INFO with its stream, read with
// the object's kind and name, and funcd's own log still holds its engine output record.
func TestScenarioEngineLogsAddonObject(t *testing.T) {
	t.Parallel()
	p, rt, b, logs := engineLogsRig(t)
	rt.write(identityLogin(), "", "rauthy is listening\n")
	runThenCancel(t, p)

	lines := readLines(t, b, logread.Query{Kind: "IdentityProvider", Name: "login"})
	require.Len(t, lines, 1)
	l := lines[0]
	require.Equal(t, []string{"rauthy is listening", "INFO", "stderr", "IdentityProvider", "login", ""},
		[]string{l.Body, l.Severity, l.Source, l.Kind, l.Name, l.Function})
	require.Contains(t, logs.String(), `"msg":"engine output","stream":"stderr","kind":"AddonObject","namespace":"default","name":"login"`)
}

// scenario: engine-logs-lake-and-sql — a Lake's and a SQLDatabase's engines keep their lines apart.
func TestScenarioEngineLogsLakeAndSQL(t *testing.T) {
	t.Parallel()
	p, rt, b, _ := engineLogsRig(t)
	rt.write(runtime.WorkerSpec{Namespace: "default", Name: "sales", OwnerKind: v1.KindLake}, "lake line\n", "")
	rt.write(runtime.WorkerSpec{Namespace: "default", Name: "orders", OwnerKind: v1.KindSQLDatabase}, "sql line\n", "")
	runThenCancel(t, p)

	require.Equal(t, []string{"lake line"}, bodiesOf(readLines(t, b, logread.Query{Kind: "Lake", Name: "sales"})))
	require.Equal(t, []string{"sql line"}, bodiesOf(readLines(t, b, logread.Query{Kind: "SQLDatabase", Name: "orders"})))
}

// scenario: engine-logs-shared-name — a Function and an IdentityProvider of one name keep their lines apart.
func TestScenarioEngineLogsSharedName(t *testing.T) {
	t.Parallel()
	p, rt, b, _ := engineLogsRig(t)
	rt.write(runtime.WorkerSpec{Namespace: "default", Name: "login", OwnerKind: v1.KindFunction}, "function line\n", "")
	rt.write(identityLogin(), "engine line\n", "")
	runThenCancel(t, p)

	require.Equal(t, []string{"function line"}, bodiesOf(readLines(t, b, logread.Query{Function: "login"})))
	require.Equal(t, []string{"engine line"}, bodiesOf(readLines(t, b, logread.Query{Kind: "IdentityProvider", Name: "login"})))
}

// scenario: engine-logs-funclog-off — with funclog off nothing is kept, and funcd's own log holds the line.
func TestScenarioEngineLogsFunclogOff(t *testing.T) {
	t.Parallel()
	p, rt, b, logs := engineLogsRig(t, WithoutFunclog())
	rt.write(runtime.WorkerSpec{Namespace: "default", Name: "sales", OwnerKind: v1.KindLake}, "lake line\n", "")
	runThenCancel(t, p)

	require.Empty(t, readLines(t, b, logread.Query{Kind: "Lake", Name: "sales"}))
	require.Contains(t, logs.String(), `"line":"lake line"`)
}

// Under dev, a line that the add-on runtime follows itself is kept, and funcd's own log, dev's terminal, never gets it
// (ADR-0342 Decision 5).
func TestDevEngineLinesStayOutOfTheDaemonLog(t *testing.T) {
	t.Parallel()
	p, _, b, logs := engineLogsRig(t)
	spec := provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: "default", Name: "login"}, ServedKind: "IdentityProvider"}
	p.exec.keepDevEngineLine(spec, "stdout", "followed line")
	p.exec.keepDevEngineLine(provider.ProviderSpec{Ref: spec.Ref}, "stdout", "a line of no served kind")
	runThenCancel(t, p)

	lines := readLines(t, b, logread.Query{Kind: "IdentityProvider", Name: "login"})
	require.Equal(t, []string{"followed line"}, bodiesOf(lines))
	require.Equal(t, "stdout", lines[0].Source)
	require.NotContains(t, logs.String(), "followed line")
}

// lineRuntime is a dev runtime that reads its engines' output itself; a test hands it lines.
type lineRuntime struct {
	provider.Runtime
	provider.LineHook
}

// ADR-0343: dev's Lake and SQL engine runners, whose specs name no served kind, have their lines kept under Lake and
// SQLDatabase, past dev's terminal.
func TestDevLakeAndSQLLinesAreKeptUnderTheirKind(t *testing.T) {
	t.Parallel()
	p, _, b, logs := engineLogsRig(t)
	lake, sql := &lineRuntime{}, &lineRuntime{}
	followDevLines(lake, v1.KindLake, p.exec.keepDevEngineLine)
	followDevLines(sql, v1.KindSQLDatabase, p.exec.keepDevEngineLine)
	lake.LinesOf(provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: "default", Name: "sales"}}, "stderr")("lake line")
	sql.LinesOf(provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: "default", Name: "orders"}}, "stderr")("sql line")
	runThenCancel(t, p)

	require.Equal(t, []string{"lake line"}, bodiesOf(readLines(t, b, logread.Query{Kind: "Lake", Name: "sales"})))
	require.Equal(t, []string{"sql line"}, bodiesOf(readLines(t, b, logread.Query{Kind: "SQLDatabase", Name: "orders"})))
	require.NotContains(t, logs.String(), "lake line")
}
