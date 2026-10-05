// Package workerpipe (ADR-0168): funcd's end of a worker's raw stdout and stderr, read as written into a bounded queue
// for funclog and a bounded tail. Both drivers use it; it imports no container or observability package.
package workerpipe

import (
	"bytes"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Stream names one of a worker's two raw output streams.
type Stream int

const (
	Stdout Stream = 1
	Stderr Stream = 2
)

const (
	DefaultLineBytes  = 64 << 10  // a longer line is split into lines of this size
	DefaultQueueBytes = 256 << 10 // the queue for funclog; half of it per stream
	DefaultTailBytes  = 64 << 10  // the tail Runtime.Logs returns
)

// dropWarnEvery spaces the drop Warns of one Output.
const dropWarnEvery = time.Minute

// Options sizes an Output; a zero size is its default and a nil Logger is slog.Default().
type Options struct {
	LineBytes, QueueBytes, TailBytes int
	Logger                           *slog.Logger
}

type tailLine struct {
	s Stream
	b []byte
}

type stream struct {
	partial []byte   // bytes after the last "\n"
	queue   [][]byte // "\n"-ended lines for the Reader
	queued  int
	reading bool // Reader was called: lines are queued from then on
	sealed  bool // CloseRead: no more lines are queued
	gone    bool // the Reader was closed
	cur     []byte
}

// Output holds funcd's end of one worker run's stdout and stderr.
type Output struct {
	mu       sync.Mutex
	cond     *sync.Cond
	opts     Options
	streams  [3]stream
	tail     []tailLine
	tailSize int
	dropped  uint64
	unwarned uint64 // drops since the last Warn
	lastWarn time.Time
	now      func() time.Time
	closed   bool
	drains   int
	finished bool
	done     chan struct{}
}

// New returns an Output sized by o.
func New(o Options) *Output {
	if o.LineBytes <= 0 {
		o.LineBytes = DefaultLineBytes
	}
	if o.QueueBytes <= 0 {
		o.QueueBytes = DefaultQueueBytes
	}
	if o.TailBytes <= 0 {
		o.TailBytes = DefaultTailBytes
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	out := &Output{opts: o, done: make(chan struct{}), lastWarn: time.Now(), now: time.Now}
	out.cond = sync.NewCond(&out.mu)
	return out
}

// Drain reads r on its own goroutine until EOF or an error, then closes r. A Drain that starts after Close still runs;
// Done waits for every Drain.
func (o *Output) Drain(s Stream, r io.ReadCloser) {
	o.mu.Lock()
	o.drains++
	o.mu.Unlock()
	go func() {
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				o.write(s, buf[:n], true)
			}
			if err != nil {
				break
			}
		}
		_ = r.Close()
		o.mu.Lock()
		o.drains--
		warn := o.finishLocked()
		o.mu.Unlock()
		o.warn(warn)
	}()
}

type writer struct {
	o *Output
	s Stream
}

func (w writer) Write(p []byte) (int, error) {
	w.o.write(w.s, p, false)
	return len(p), nil
}

// Writer returns a writer for s that never blocks and always returns len(p), nil: a line its stream's queue budget
// cannot hold is dropped and counted, and so is every line written after Close.
func (o *Output) Writer(s Stream) io.Writer { return writer{o: o, s: s} }

// write splits p into lines and files them; draining is a Drain's write, which Close does not cut off.
func (o *Output) write(s Stream, p []byte, draining bool) {
	o.mu.Lock()
	if o.closed && !draining {
		o.dropLocked(uint64(max(1, bytes.Count(p, []byte{'\n'}))))
		o.mu.Unlock()
		return
	}
	st := &o.streams[s]
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		var seg []byte
		if i < 0 {
			seg, p = p, nil
		} else {
			seg, p = p[:i], p[i+1:]
		}
		// Only a line over LineBytes is split: one of exactly LineBytes stays one line.
		for len(st.partial)+len(seg) > o.opts.LineBytes {
			n := o.opts.LineBytes - len(st.partial)
			o.lineLocked(s, append(st.partial, seg[:n]...))
			st.partial, seg = nil, seg[n:]
		}
		st.partial = append(st.partial, seg...)
		if i >= 0 {
			o.lineLocked(s, st.partial)
			st.partial = nil
		}
	}
	warn := o.warnDueLocked()
	o.mu.Unlock()
	o.warn(warn)
}

// lineLocked files one line (without its "\n") into the tail and, while a Reader takes s, its queue.
func (o *Output) lineLocked(s Stream, b []byte) {
	line := make([]byte, len(b)+1)
	copy(line, b)
	line[len(b)] = '\n'
	o.tail = append(o.tail, tailLine{s: s, b: line})
	o.tailSize += len(line)
	for o.tailSize > o.opts.TailBytes && len(o.tail) > 1 {
		o.tailSize -= len(o.tail[0].b)
		o.tail[0] = tailLine{}
		o.tail = o.tail[1:]
	}
	st := &o.streams[s]
	if !st.reading || st.sealed || st.gone {
		return
	}
	if st.queued+len(line) > o.opts.QueueBytes/2 {
		o.dropLocked(1)
		return
	}
	st.queue = append(st.queue, line)
	st.queued += len(line)
	o.cond.Broadcast()
}

func (o *Output) dropLocked(n uint64) {
	o.dropped += n
	o.unwarned += n
}

// warnDueLocked returns the drops to Warn about now, at most once a minute.
func (o *Output) warnDueLocked() uint64 {
	now := o.now()
	if o.unwarned == 0 || now.Sub(o.lastWarn) < dropWarnEvery {
		return 0
	}
	n := o.unwarned
	o.unwarned = 0
	o.lastWarn = now
	return n
}

func (o *Output) warn(n uint64) {
	if n > 0 {
		o.opts.Logger.Warn("worker output dropped: funclog did not keep up", "dropped", n)
	}
}

type reader struct {
	o *Output
	s Stream
}

func (r reader) Read(p []byte) (int, error) {
	o := r.o
	o.mu.Lock()
	defer o.mu.Unlock()
	st := &o.streams[r.s]
	for len(st.cur) == 0 {
		if len(st.queue) > 0 {
			st.cur = st.queue[0]
			st.queue[0] = nil
			st.queue = st.queue[1:]
			st.queued -= len(st.cur)
			break
		}
		if st.gone {
			return 0, io.ErrClosedPipe
		}
		if o.finished || st.sealed {
			return 0, io.EOF
		}
		o.cond.Wait()
	}
	n := copy(p, st.cur)
	st.cur = st.cur[n:]
	return n, nil
}

// Close drops what the Reader has not read and stops queueing for it.
func (r reader) Close() error {
	o := r.o
	o.mu.Lock()
	st := &o.streams[r.s]
	st.gone, st.queue, st.queued, st.cur = true, nil, 0, nil
	o.cond.Broadcast()
	o.mu.Unlock()
	return nil
}

// CloseRead seals the Reader: it returns the lines already queued, then EOF.
func (r reader) CloseRead() error {
	o := r.o
	o.mu.Lock()
	o.streams[r.s].sealed = true
	o.cond.Broadcast()
	o.mu.Unlock()
	return nil
}

// StreamReader is one stream's queued lines; CloseRead is the Shutdown seal: EOF after the queued lines.
type StreamReader interface {
	io.ReadCloser
	CloseRead() error
}

// Reader returns s's "\n"-ended lines, queued from the first call; it returns EOF once the Output is closed, drained
// and the queue is empty.
func (o *Output) Reader(s Stream) StreamReader {
	o.mu.Lock()
	o.streams[s].reading = true
	o.mu.Unlock()
	return reader{o: o, s: s}
}

// Tail returns a copy of the last TailBytes of output, the oldest line evicted first: stdout's lines, then stderr's.
func (o *Output) Tail() io.ReadCloser {
	o.mu.Lock()
	defer o.mu.Unlock()
	var b bytes.Buffer
	for _, s := range []Stream{Stdout, Stderr} {
		for _, l := range o.tail {
			if l.s == s {
				b.Write(l.b)
			}
		}
	}
	return io.NopCloser(&b)
}

// Dropped is how many lines were dropped.
func (o *Output) Dropped() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dropped
}

// Close never blocks and is idempotent: once every Drain has returned it files the last partial lines, Warns about any
// drop not yet reported, and closes Done. Writes after Close are dropped and counted.
func (o *Output) Close() {
	o.mu.Lock()
	o.closed = true
	warn := o.finishLocked()
	o.mu.Unlock()
	o.warn(warn)
}

// finishLocked completes a closed Output whose Drains have all returned, once, and returns the drops to Warn about.
func (o *Output) finishLocked() uint64 {
	if !o.closed || o.drains > 0 || o.finished {
		return 0
	}
	for _, s := range []Stream{Stdout, Stderr} {
		if st := &o.streams[s]; len(st.partial) > 0 {
			o.lineLocked(s, st.partial)
			st.partial = nil
		}
	}
	o.finished = true
	o.cond.Broadcast()
	close(o.done)
	n := o.unwarned
	o.unwarned = 0
	return n
}

// Done is closed once Close ran and every Drain returned.
func (o *Output) Done() <-chan struct{} { return o.done }
