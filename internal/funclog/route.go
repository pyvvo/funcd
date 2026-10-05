package funclog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/pyvvo/funcd/internal/platform/clock"
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
	Links     []string          `json:"links"`        // ADR-0105: fan-in edges (same-trace span-ids)
	Member    string            `json:"funcd.member"` // the pooled Function that wrote it; empty from a solo worker
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
		Links:      w.Links,
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
// Every record is stored under res: a solo worker's "funcd.member" key is ignored.
func Route(ctx context.Context, r io.Reader, sinks Sinks, res Resource, log *slog.Logger) error {
	return drain(ctx, r, sinks, soloDest{res: res}, res.Function, log)
}

// RoutePool drains a pool worker's channel like Route, but stores each record under the pooled Function
// its "funcd.member" key names: pool's Namespace, Replica and Tenant with Function set to the member,
// when isMember(member) holds. A record naming no member, or one outside the set, is dropped and counted,
// with a warning at most once a minute; nothing is stored under the pool worker's own name. On EOF or ctx
// cancellation it seals the segments of every member it stored records for.
func RoutePool(ctx context.Context, r io.Reader, sinks Sinks, pool Resource, isMember func(name string) bool, log *slog.Logger) error {
	return routePool(ctx, r, sinks, pool, isMember, log, clock.System())
}

func routePool(ctx context.Context, r io.Reader, sinks Sinks, pool Resource, isMember func(name string) bool, log *slog.Logger, clk clock.Clock) error {
	if log == nil {
		log = slog.Default()
	}
	d := &poolDest{pool: pool, isMember: isMember, log: log, clock: clk, members: map[string]Resource{}}
	return drain(ctx, r, sinks, d, pool.Function, log)
}

// destination places the records of one channel: resource returns the Resource a record naming member
// is stored under (false drops it), and seal flushes what was stored once the channel ends.
type destination interface {
	resource(ctx context.Context, member string) (Resource, bool)
	seal(ctx context.Context, sinks Sinks) error
}

// soloDest stores every record of a solo worker under its own Resource.
type soloDest struct{ res Resource }

func (d soloDest) resource(context.Context, string) (Resource, bool) { return d.res, true }

func (d soloDest) seal(ctx context.Context, sinks Sinks) error { return flushSinks(ctx, sinks, d.res) }

// dropWarnEvery bounds how often a pool logs the records it dropped.
const dropWarnEvery = time.Minute

// poolDest stores a pool worker's records under the members they name. A drain is one goroutine, so it
// needs no lock.
type poolDest struct {
	pool     Resource
	isMember func(name string) bool
	log      *slog.Logger
	clock    clock.Clock
	members  map[string]Resource // members a record was stored for, sealed when the channel ends
	dropped  int                 // records dropped since the last warning
	warned   time.Time           // the last warning; zero before the first
}

func (d *poolDest) resource(ctx context.Context, member string) (Resource, bool) {
	if member != "" && d.isMember != nil && d.isMember(member) {
		res, ok := d.members[member]
		if !ok {
			res = Resource{Namespace: d.pool.Namespace, Function: member, Replica: d.pool.Replica, Tenant: d.pool.Tenant}
			d.members[member] = res
		}
		return res, true
	}
	d.dropped++
	if now := d.clock.Now(); d.warned.IsZero() || now.Sub(d.warned) >= dropWarnEvery {
		d.log.WarnContext(ctx, "funclog: dropping pool records that name no pooled Function", "pool", d.pool.Function, "dropped", d.dropped)
		d.warned, d.dropped = now, 0
	}
	return Resource{}, false
}

func (d *poolDest) seal(ctx context.Context, sinks Sinks) error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(d.members)) {
		errs = append(errs, flushSinks(ctx, sinks, d.members[name]))
	}
	return errors.Join(errs...)
}

// drain is the decode loop of Route and RoutePool; name labels the channel in its warnings.
func drain(ctx context.Context, r io.Reader, sinks Sinks, dest destination, name string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	lr := newLineReader(r)
	for {
		if err := ctx.Err(); err != nil {
			return dest.seal(ctx, sinks)
		}
		line, err := lr.next()
		if errors.Is(err, errLineTooLong) {
			log.WarnContext(ctx, "funclog: skipping unreadable record", "function", name, "error", err)
			continue
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.WarnContext(ctx, "funclog: channel scan error", "function", name, "error", err)
			}
			// EOF or a read error: channel gone → seal remaining segments.
			return dest.seal(context.Background(), sinks)
		}
		if len(line) > 0 {
			routeLine(ctx, line, sinks, dest, name, log)
		}
	}
}

// routeLine decodes one channel line and appends it to the sink of its signal, under dest's Resource.
func routeLine(ctx context.Context, line []byte, sinks Sinks, dest destination, name string, log *slog.Logger) {
	var peek signalPeek
	if err := json.Unmarshal(line, &peek); err != nil {
		log.WarnContext(ctx, "funclog: skipping unreadable record", "function", name, "error", err)
		return
	}
	if Signal(peek.Signal) == SignalTraces {
		if sinks.Traces == nil {
			return // traces disabled: read the line off the channel and drop it
		}
		var sw spanWire
		if err := json.Unmarshal(line, &sw); err != nil {
			log.WarnContext(ctx, "funclog: skipping unreadable span", "function", name, "error", err)
			return
		}
		if res, ok := dest.resource(ctx, sw.Member); ok {
			if err := sinks.Traces.AppendSpan(ctx, res, sw.toSpan()); err != nil {
				log.WarnContext(ctx, "funclog: span append failed", "function", res.Function, "error", err)
			}
		}
		return
	}
	if sinks.Logs == nil {
		return
	}
	var lw wireRecord
	if err := json.Unmarshal(line, &lw); err != nil {
		log.WarnContext(ctx, "funclog: skipping unreadable record", "function", name, "error", err)
		return
	}
	if res, ok := dest.resource(ctx, lw.Member); ok {
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
