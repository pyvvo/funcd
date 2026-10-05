package funclog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/pyvvo/funcd/api/fault"
)

// MaxLineBytes caps one channel line; a longer line is dropped, never buffered past the cap. It is the ceiling of
// funclog.maxRecordBytes (ADR-0168).
const MaxLineBytes = 1024 * 1024

// DefaultMaxRecordBytes bounds one shim record line when funclog.maxRecordBytes is 0 (ADR-0168).
const DefaultMaxRecordBytes = 64 << 10

// errLineTooLong reports a line over MaxLineBytes that was read through its newline and dropped.
var errLineTooLong = errors.New("line exceeds the 1 MiB cap")

// lineReader splits a channel into lines like bufio.ScanLines, but drops an over-long line and keeps
// reading where bufio.Scanner stops for good (bufio.ErrTooLong), so one huge record cannot end the
// drain (issue #32).
type lineReader struct {
	br   *bufio.Reader
	line []byte
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{br: bufio.NewReaderSize(r, 64*1024)}
}

// next returns the next line without its line end, valid until the following call. It returns
// errLineTooLong for a dropped line, and io.EOF (or the channel's read error) when the channel ends.
func (l *lineReader) next() ([]byte, error) {
	l.line = l.line[:0]
	tooLong := false
	for {
		frag, more, err := l.br.ReadLine()
		if err != nil {
			return nil, err
		}
		if len(l.line)+len(frag) > MaxLineBytes {
			tooLong = true
		}
		if !tooLong {
			l.line = append(l.line, frag...)
		}
		if !more {
			break
		}
	}
	if tooLong {
		return nil, errLineTooLong
	}
	return l.line, nil
}

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
	Member  string            `json:"funcd.member"` // the pooled Function that wrote it; empty from a solo worker
}

// ndjsonReader decodes Path B: one JSON record per line.
type ndjsonReader struct{ lr *lineReader }

// NewNDJSONReader decodes Path B (one JSON record per line) from the fd3/UDS channel.
func NewNDJSONReader(r io.Reader) Reader {
	return &ndjsonReader{lr: newLineReader(r)}
}

func (r *ndjsonReader) Read(_ context.Context) (Entry, error) {
	const op = "funclog.ndjsonReader.Read"
	for {
		line, err := r.lr.next()
		switch {
		case errors.Is(err, io.EOF):
			return Entry{}, io.EOF
		case errors.Is(err, errLineTooLong):
			return Entry{}, fault.Invalidf(op, "record skipped: %v", err)
		case err != nil:
			return Entry{}, fault.Wrapf(err, fault.Internal, op, "read channel")
		}
		if len(line) == 0 {
			continue // skip blank lines
		}
		var w wireRecord
		if err := json.Unmarshal(line, &w); err != nil {
			return Entry{}, fault.Invalidf(op, "malformed NDJSON record: %v", err)
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
	lr  *lineReader
	src Source
}

// NewRawReader decodes Path A (raw lines) from fd 1 or fd 2, assigning coarse severity by src
// (SourceStdout → INFO, SourceStderr → ERROR).
func NewRawReader(r io.Reader, src Source) Reader {
	return &rawReader{lr: newLineReader(r), src: src}
}

func (r *rawReader) Read(_ context.Context) (Entry, error) {
	const op = "funclog.rawReader.Read"
	line, err := r.lr.next()
	switch {
	case errors.Is(err, io.EOF):
		return Entry{}, io.EOF
	case errors.Is(err, errLineTooLong):
		return Entry{}, fault.Invalidf(op, "line skipped: %v", err)
	case err != nil:
		return Entry{}, fault.Wrapf(err, fault.Internal, op, "read fd")
	}
	sev := SevInfo
	if r.src == SourceStderr {
		sev = SevError
	}
	// A raw line carries no time of its own: it is stamped when read (ADR-0168).
	return Entry{Time: time.Now(), Severity: sev, Body: string(line), Source: r.src}, nil
}
