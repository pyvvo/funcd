package funclog_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/platform/clock"
)

const poolWorker = "__pool__nodejs22__agents"

func poolRes() funclog.Resource {
	return funclog.Resource{Namespace: "default", Function: poolWorker, Replica: "0"}
}

func membersOf(names ...string) func(string) bool {
	return func(name string) bool { return slices.Contains(names, name) }
}

func memberLog(member, body string) string {
	return `{"sev":"INFO","body":"` + body + `","funcd.source":"console","funcd.member":"` + member + `"}`
}

func memberSpan(member, name string) string {
	return `{"funcd.signal":"traces","trace_id":"` + testTraceID + `","span_id":"` + testSpanID + `","name":"` + name +
		`","kind":"SERVER","start":1,"end":2,"status":"OK","inv":"i","funcd.member":"` + member + `"}`
}

func channelOf(lines ...string) io.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func requireNoObjects(t *testing.T, b blob.Bucket, prefixes ...string) {
	t.Helper()
	for _, prefix := range prefixes {
		objs, err := b.List(context.Background(), prefix)
		require.NoError(t, err)
		require.Empty(t, objs, "nothing stored under %s", prefix)
	}
}

// warnLines returns the warnings a text slog handler wrote to buf.
func warnLines(buf *bytes.Buffer) []string {
	var out []string
	for line := range strings.Lines(buf.String()) {
		if strings.Contains(line, "level=WARN") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// steppedClock moves forward by step on every reading.
type steppedClock struct {
	now  time.Time
	step time.Duration
}

func (c *steppedClock) Now() time.Time {
	c.now = c.now.Add(c.step)
	return c.now
}

// recordingSink notes the Function of every Resource it is asked to flush.
type recordingSink struct{ flushed []string }

func (s *recordingSink) Append(context.Context, funclog.Resource, funclog.Entry) error { return nil }

func (s *recordingSink) AppendSpan(context.Context, funclog.Resource, funclog.Span) error { return nil }

func (s *recordingSink) Close() error { return nil }

func (s *recordingSink) Flush(_ context.Context, res funclog.Resource) (string, error) {
	s.flushed = append(s.flushed, res.Function)
	return "", nil
}

// A pool worker's logs and spans are stored under the pooled Function each record names, with the
// pool's namespace and replica, and nothing under the pool worker's own name.
func TestRoutePoolStoresRecordsUnderMember(t *testing.T) {
	b := memBucket(t)
	sinks := funclog.Sinks{Logs: newSink(t, b, 1<<20), Traces: newTraceSink(t, b, 1<<20)}
	ch := channelOf(memberLog("a", "hello-a"), memberSpan("a", "span-a"), memberLog("b", "hello-b"), memberSpan("b", "span-b"))

	require.NoError(t, funclog.RoutePool(context.Background(), ch, sinks, poolRes(), membersOf("a", "b"), nil))

	for _, m := range []string{"a", "b"} {
		logs := readBackOne(t, b, "logs/default/"+m+"/")
		assert.Equal(t, []string{"hello-" + m}, logBodies(logs))
		attrs := logs.ResourceLogs().At(0).Resource().Attributes().AsRaw()
		assert.Equal(t, m, attrs["function"])
		assert.Equal(t, "default", attrs["namespace"])
		assert.Equal(t, "0", attrs["replica"])
		spans := readBackTraces(t, b, "traces/default/"+m+"/").ResourceSpans().At(0).ScopeSpans().At(0).Spans()
		require.Equal(t, 1, spans.Len())
		assert.Equal(t, "span-"+m, spans.At(0).Name())
	}
	requireNoObjects(t, b, "logs/default/"+poolWorker+"/", "traces/default/"+poolWorker+"/")
}

// A record with no member, or naming a Function outside the pool's set, is dropped and counted; the
// warning is logged at most once a minute and carries the count since the previous one.
func TestRoutePoolDropsNonMember(t *testing.T) {
	anonymous := `{"sev":"INFO","body":"anonymous","funcd.source":"console"}`

	t.Run("drops in one minute log once", func(t *testing.T) {
		b := memBucket(t)
		sinks := funclog.Sinks{Logs: newSink(t, b, 1<<20), Traces: newTraceSink(t, b, 1<<20)}
		var buf bytes.Buffer
		ch := channelOf(anonymous, memberLog("c", "from-c"), memberSpan("c", "span-c"), memberLog("a", "kept"))

		require.NoError(t, funclog.RoutePoolWithClock(context.Background(), ch, sinks, poolRes(), membersOf("a", "b"),
			slog.New(slog.NewTextHandler(&buf, nil)), clock.Fake(time.Unix(1_700_000_000, 0))))

		warns := warnLines(&buf)
		require.Len(t, warns, 1, "three drops within a minute log one warning")
		assert.Contains(t, warns[0], "dropped=1")
		assert.Equal(t, []string{"kept"}, logBodies(readBackOne(t, b, "logs/default/a/")))
		requireNoObjects(t, b, "logs/default/c/", "traces/default/c/", "logs/default/"+poolWorker+"/", "traces/default/")
	})

	t.Run("a drop after a minute logs the count", func(t *testing.T) {
		var buf bytes.Buffer
		sinks := funclog.Sinks{Logs: &recordingSink{}, Traces: &recordingSink{}}
		ch := channelOf(anonymous, memberLog("c", "x"), memberLog("d", "y"))
		clk := &steppedClock{now: time.Unix(1_700_000_000, 0), step: 40 * time.Second}

		require.NoError(t, funclog.RoutePoolWithClock(context.Background(), ch, sinks, poolRes(), membersOf("a"),
			slog.New(slog.NewTextHandler(&buf, nil)), clk))

		warns := warnLines(&buf)
		require.Len(t, warns, 2, "drops at 40s and 80s log once; the one at 120s logs again")
		assert.Contains(t, warns[0], "dropped=1")
		assert.Contains(t, warns[1], "dropped=2")
		assert.Contains(t, warns[1], "pool="+poolWorker)
	})
}

// At the end of the channel every member a record was stored for is sealed, on both signals, and the
// pool worker's own Resource is never flushed.
func TestRoutePoolFlushesMembers(t *testing.T) {
	logs, traces := &recordingSink{}, &recordingSink{}
	ch := channelOf(memberLog("b", "x"), memberSpan("a", "s"), memberLog("a", "y"), memberLog("z", "dropped"))

	require.NoError(t, funclog.RoutePool(context.Background(), ch, funclog.Sinks{Logs: logs, Traces: traces}, poolRes(),
		membersOf("a", "b"), slog.New(slog.DiscardHandler)))

	assert.Equal(t, []string{"a", "b"}, logs.flushed)
	assert.Equal(t, []string{"a", "b"}, traces.flushed)
}

// A solo worker's records are stored under its own Resource even when a line names a member.
func TestRouteIgnoresMember(t *testing.T) {
	b := memBucket(t)
	sinks := funclog.Sinks{Logs: newSink(t, b, 1<<20), Traces: newTraceSink(t, b, 1<<20)}
	ch := channelOf(memberLog("other", "solo-line"), memberSpan("other", "solo-span"))

	require.NoError(t, funclog.Route(context.Background(), ch, sinks, defaultRes(), nil))

	assert.Equal(t, []string{"solo-line"}, logBodies(readBackOne(t, b, "logs/default/hello/")))
	assert.Equal(t, 1, readBackTraces(t, b, "traces/default/hello/").SpanCount())
	requireNoObjects(t, b, "logs/default/other/", "traces/default/other/")
}
