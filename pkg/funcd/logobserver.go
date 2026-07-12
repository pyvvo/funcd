package funcd

import (
	"context"
	"time"

	"github.com/green-0-rabbit/funcd/internal/funclog"
)

// LogLine is one structured function-log record surfaced to a LogObserver — a stable projection of
// the internal funclog Entry, so the public option leaks no internal type. Attrs is read-only.
type LogLine struct {
	Namespace  string
	Function   string
	Replica    string
	Invocation string            // per-invocation id (may be empty for pre-invocation lines)
	Time       time.Time         // real event time
	Severity   string            // TRACE | DEBUG | INFO | WARN | ERROR | FATAL
	Body       string            // the log message
	Attrs      map[string]string // structured attributes
}

// LogObserver receives every captured function log line as it happens, IN ADDITION to the normal
// blob-persisted capture. `funcdctl dev` uses it to stream logs to the terminal in real time. It runs
// on the log-routing goroutine and must not block.
type LogObserver func(LogLine)

// teeLogSink wraps the real funclog.Sink so each Append both persists (inner) and streams live
// (observe). Flush/Close delegate to inner. observe must not block (it runs inline with routing).
type teeLogSink struct {
	inner   funclog.Sink
	observe LogObserver
}

func (t *teeLogSink) Append(ctx context.Context, res funclog.Resource, e funclog.Entry) error {
	t.observe(LogLine{
		Namespace:  res.Namespace,
		Function:   res.Function,
		Replica:    res.Replica,
		Invocation: e.Invocation,
		Time:       e.Time,
		Severity:   string(e.Severity),
		Body:       e.Body,
		Attrs:      e.Attrs,
	})
	return t.inner.Append(ctx, res, e)
}

func (t *teeLogSink) Flush(ctx context.Context, res funclog.Resource) (string, error) {
	return t.inner.Flush(ctx, res)
}

func (t *teeLogSink) Close() error { return t.inner.Close() }
