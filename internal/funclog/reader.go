package funclog

import (
	"bufio"
	"context"
	"encoding/json"
	"io"

	"github.com/pyvvo/funcd/api/fault"
)

// Reader decodes one instance's telemetry channel into Entries. NDJSON (Path B) or raw fd lines
// (Path A). A frozen instance stops sending, so Read blocks — which pauses the Pump and prevents
// in-function backlog loss.
type Reader interface {
	// Read returns the next Entry, or io.EOF when the channel closes (instance gone).
	Read(ctx context.Context) (Entry, error)
}

// wireRecord is the NDJSON line a harness emits (the language-agnostic Path B wire, ADR-0081).
type wireRecord struct {
	TS      int64             `json:"ts"`    // epoch nanos
	Sev     string            `json:"sev"`   // OTel severity (TRACE…FATAL)
	Body    string            `json:"body"`  // the message
	Attrs   map[string]string `json:"attrs"` // structured attributes
	Inv     string            `json:"inv"`   // per-invocation id
	TraceID string            `json:"trace_id"`
	SpanID  string            `json:"span_id"`
	Source  string            `json:"funcd.source"`
}

// ndjsonReader decodes Path B: one JSON record per line.
type ndjsonReader struct{ sc *bufio.Scanner }

// NewNDJSONReader decodes Path B (one JSON record per line) from the fd3/UDS channel.
func NewNDJSONReader(r io.Reader) Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // tolerate long structured lines
	return &ndjsonReader{sc: sc}
}

func (r *ndjsonReader) Read(_ context.Context) (Entry, error) {
	for {
		if !r.sc.Scan() {
			if err := r.sc.Err(); err != nil {
				return Entry{}, fault.Wrapf(err, fault.Internal, "funclog.ndjsonReader.Read", "scan channel")
			}
			return Entry{}, io.EOF
		}
		line := r.sc.Bytes()
		if len(line) == 0 {
			continue // skip blank lines
		}
		var w wireRecord
		if err := json.Unmarshal(line, &w); err != nil {
			return Entry{}, fault.Invalidf("funclog.ndjsonReader.Read", "malformed NDJSON record: %v", err)
		}
		return w.toEntry(), nil
	}
}

func (w wireRecord) toEntry() Entry {
	sev := Severity(w.Sev)
	if !sev.valid() {
		sev = SevInfo
	}
	src := Source(w.Source)
	if src == "" {
		src = SourceConsole
	}
	return Entry{
		Time:       timeFromNanos(w.TS),
		Severity:   sev,
		Body:       w.Body,
		Attrs:      w.Attrs,
		Invocation: w.Inv,
		TraceID:    w.TraceID,
		SpanID:     w.SpanID,
		Source:     src,
	}
}

// rawReader decodes Path A: raw fd 1/2 lines into coarse Entries (no structure, fd-based severity).
type rawReader struct {
	sc  *bufio.Scanner
	src Source
}

// NewRawReader decodes Path A (raw lines) from fd 1 or fd 2, assigning coarse severity by src
// (SourceStdout → INFO, SourceStderr → ERROR).
func NewRawReader(r io.Reader, src Source) Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &rawReader{sc: sc, src: src}
}

func (r *rawReader) Read(_ context.Context) (Entry, error) {
	if !r.sc.Scan() {
		if err := r.sc.Err(); err != nil {
			return Entry{}, fault.Wrapf(err, fault.Internal, "funclog.rawReader.Read", "scan fd")
		}
		return Entry{}, io.EOF
	}
	sev := SevInfo
	if r.src == SourceStderr {
		sev = SevError
	}
	return Entry{Severity: sev, Body: r.sc.Text(), Source: r.src}, nil
}
