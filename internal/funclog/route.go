package funclog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
)

// Sinks bundles the per-signal sinks the demux routes into. A nil sink drops that signal (e.g.
// traces disabled ⇒ Traces == nil ⇒ span lines are read off the channel and discarded).
type Sinks struct {
	Logs   Sink
	Traces TraceSink
}

// spanWire is the NDJSON line a harness emits for the traces signal (ADR-0101). It shares the
// channel with the log wire; "funcd.signal":"traces" discriminates it. A log line carries no
// "funcd.signal" key, so the demux routes it to the logs sink (back-compat with the F50 wire).
type spanWire struct {
	Signal    string            `json:"funcd.signal"`
	TraceID   string            `json:"trace_id"`
	SpanID    string            `json:"span_id"`
	ParentID  string            `json:"parent_id"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Start     int64             `json:"start"` // epoch nanos
	End       int64             `json:"end"`   // epoch nanos
	Status    string            `json:"status"`
	StatusMsg string            `json:"status_msg"`
	Attrs     map[string]string `json:"attrs"`
	Inv       string            `json:"inv"`
}

func (w spanWire) toSpan() Span {
	kind := SpanKind(w.Kind)
	if !kind.valid() {
		kind = SpanServer // the only emitter today is the auto invocation span
	}
	status := SpanStatus(w.Status)
	if !status.valid() {
		status = StatusUnset
	}
	return Span{
		TraceID:    w.TraceID,
		SpanID:     w.SpanID,
		ParentID:   w.ParentID,
		Name:       w.Name,
		Kind:       kind,
		Start:      timeFromNanos(w.Start),
		End:        timeFromNanos(w.End),
		Status:     status,
		StatusMsg:  w.StatusMsg,
		Attrs:      w.Attrs,
		Invocation: w.Inv,
	}
}

// signalPeek reads just the discriminator so the demux can decode a line to the right record type.
type signalPeek struct {
	Signal string `json:"funcd.signal"`
}

// Route drains one instance's telemetry channel, decoding each NDJSON line and dispatching by its
// "funcd.signal" tag: "traces" → Sinks.Traces.AppendSpan(Span); absent/other → Sinks.Logs.Append(Entry).
// It replaces the single-Sink Pump at the composition-root wiring; Pump/NewNDJSONReader remain for the
// logs-only contract tests. Loss-safe: a frozen instance stops sending, so the blocking Scan pauses the
// drain — no already-emitted record is dropped. A single malformed line is logged and skipped, never
// killing the stream; on EOF (channel closed) or ctx cancellation both sinks' remaining segments are sealed.
func Route(ctx context.Context, r io.Reader, sinks Sinks, res Resource, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // tolerate long structured lines
	for {
		if err := ctx.Err(); err != nil {
			return flushSinks(ctx, sinks, res)
		}
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				log.WarnContext(ctx, "funclog: channel scan error", "function", res.Function, "error", err)
			}
			// EOF or a scan error: channel gone → seal remaining segments.
			return flushSinks(context.Background(), sinks, res)
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue // skip blank lines
		}
		var peek signalPeek
		if err := json.Unmarshal(line, &peek); err != nil {
			log.WarnContext(ctx, "funclog: skipping unreadable record", "function", res.Function, "error", err)
			continue
		}
		if Signal(peek.Signal) == SignalTraces {
			if sinks.Traces == nil {
				continue // traces disabled: read the line off the channel and drop it
			}
			var sw spanWire
			if err := json.Unmarshal(line, &sw); err != nil {
				log.WarnContext(ctx, "funclog: skipping unreadable span", "function", res.Function, "error", err)
				continue
			}
			if err := sinks.Traces.AppendSpan(ctx, res, sw.toSpan()); err != nil {
				log.WarnContext(ctx, "funclog: span append failed", "function", res.Function, "error", err)
			}
			continue
		}
		if sinks.Logs == nil {
			continue
		}
		var lw wireRecord
		if err := json.Unmarshal(line, &lw); err != nil {
			log.WarnContext(ctx, "funclog: skipping unreadable record", "function", res.Function, "error", err)
			continue
		}
		if err := sinks.Logs.Append(ctx, res, lw.toEntry()); err != nil {
			log.WarnContext(ctx, "funclog: append failed", "function", res.Function, "error", err)
		}
	}
}

// flushSinks seals both signals' remaining segments for res (best-effort; returns the first error).
func flushSinks(ctx context.Context, sinks Sinks, res Resource) error {
	var errs []error
	if sinks.Logs != nil {
		if _, err := sinks.Logs.Flush(ctx, res); err != nil {
			errs = append(errs, err)
		}
	}
	if sinks.Traces != nil {
		if _, err := sinks.Traces.Flush(ctx, res); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
