package workerpipe_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// warnLog records the Warns an Output logs.
type warnLog struct {
	mu      sync.Mutex
	dropped []uint64
}

func (w *warnLog) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnLog) Handle(_ context.Context, r slog.Record) error {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "dropped" {
			w.mu.Lock()
			w.dropped = append(w.dropped, a.Value.Uint64())
			w.mu.Unlock()
		}
		return true
	})
	return nil
}
func (w *warnLog) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnLog) WithGroup(string) slog.Handler      { return w }

func (w *warnLog) all() []uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]uint64(nil), w.dropped...)
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func TestWriteNeverBlocksAndCountsDrops(t *testing.T) {
	log := &warnLog{}
	out := workerpipe.New(workerpipe.Options{QueueBytes: 1024, Logger: slog.New(log)})
	_ = out.Reader(workerpipe.Stdout) // taken, never read
	w := out.Writer(workerpipe.Stdout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		line := []byte(strings.Repeat("x", 99) + "\n")
		for range 10_000 {
			if n, err := w.Write(line); n != len(line) || err != nil {
				t.Errorf("Write = %d, %v", n, err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked on a reader that never reads")
	}
	if d := out.Dropped(); d < 9_990 {
		t.Fatalf("Dropped = %d, want almost every line", d)
	}
	out.Close()
	<-out.Done()
	if got := log.all(); len(got) != 1 || got[0] != out.Dropped() {
		t.Fatalf("Warns = %v, want one at Close with every drop (%d)", got, out.Dropped())
	}
	before := out.Dropped()
	if _, err := out.Writer(workerpipe.Stderr).Write([]byte("late\n")); err != nil {
		t.Fatal(err)
	}
	if got := out.Dropped(); got != before+1 {
		t.Fatalf("Dropped after a Write after Close = %d, want %d", got, before+1)
	}
}

func TestStreamsHaveTheirOwnQueueBudget(t *testing.T) {
	out := workerpipe.New(workerpipe.Options{QueueBytes: 1024})
	stdout, stderr := out.Reader(workerpipe.Stdout), out.Reader(workerpipe.Stderr)
	_, _ = out.Writer(workerpipe.Stdout).Write(bytes.Repeat([]byte("o\n"), 10_000))
	_, _ = out.Writer(workerpipe.Stderr).Write([]byte("crash\n"))
	out.Close()
	if got := readAll(t, stderr); got != "crash\n" {
		t.Fatalf("stderr = %q: a stdout flood pushed out the stderr line", got)
	}
	if got := readAll(t, stdout); len(got) > 512 || len(got) == 0 {
		t.Fatalf("stdout kept %d bytes, want at most its half of the queue", len(got))
	}
}

func TestLongLinesAreSplitAndPartialLinesQueuedAtClose(t *testing.T) {
	out := workerpipe.New(workerpipe.Options{LineBytes: 8})
	r := out.Reader(workerpipe.Stdout)
	w := out.Writer(workerpipe.Stdout)
	_, _ = w.Write([]byte("abcdefghij"))
	_, _ = w.Write([]byte("kl\nmn"))
	out.Close()
	if got := readAll(t, r); got != "abcdefgh\nijkl\nmn\n" {
		t.Fatalf("lines = %q", got)
	}
}

func TestLineOfExactlyLineBytesIsOneLine(t *testing.T) {
	out := workerpipe.New(workerpipe.Options{LineBytes: 8})
	r := out.Reader(workerpipe.Stdout)
	w := out.Writer(workerpipe.Stdout)
	_, _ = w.Write([]byte("12345678\nab\n"))
	_, _ = w.Write([]byte("1234"))
	_, _ = w.Write([]byte("5678\n123456789\n"))
	out.Close()
	if got := readAll(t, r); got != "12345678\nab\n12345678\n12345678\n9\n" {
		t.Fatalf("lines = %q", got)
	}
}

func TestDropsAreWarnedAtMostOnceAMinute(t *testing.T) {
	log := &warnLog{}
	out := workerpipe.New(workerpipe.Options{QueueBytes: 8, Logger: slog.New(log)})
	var mu sync.Mutex
	now := time.Unix(1_700_000_000, 0)
	workerpipe.SetClock(out, func() time.Time { mu.Lock(); defer mu.Unlock(); return now })
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	_ = out.Reader(workerpipe.Stdout) // taken, never read
	w := out.Writer(workerpipe.Stdout)
	drop := func(n int) { _, _ = w.Write(bytes.Repeat([]byte("xyz\n"), n)) }

	drop(3) // the first line fills the stream's half of the queue, the other two are dropped
	advance(59 * time.Second)
	drop(1)
	if got := log.all(); len(got) != 0 {
		t.Fatalf("Warns within the first minute = %v, want none", got)
	}
	advance(time.Second)
	drop(1)
	if got := log.all(); len(got) != 1 || got[0] != 4 {
		t.Fatalf("Warns after a minute = %v, want one with the 4 drops so far", got)
	}
	advance(30 * time.Second)
	drop(2)
	if got := log.all(); len(got) != 1 {
		t.Fatalf("Warns 30 s after the last one = %v, want still one", got)
	}
	out.Close()
	<-out.Done()
	if got := log.all(); len(got) != 2 || got[1] != 2 || out.Dropped() != 6 {
		t.Fatalf("Warns after Close = %v (Dropped %d), want a last one with the 2 drops not yet reported", got, out.Dropped())
	}
}

func TestTailKeepsTheNewestLinesStdoutThenStderr(t *testing.T) {
	out := workerpipe.New(workerpipe.Options{TailBytes: 64})
	_, _ = out.Writer(workerpipe.Stdout).Write([]byte("booting\n"))
	_, _ = out.Writer(workerpipe.Stderr).Write([]byte("load error\n"))
	_, _ = out.Writer(workerpipe.Stdout).Write([]byte("after\n"))
	if got := readAll(t, out.Tail()); got != "booting\nafter\nload error\n" {
		t.Fatalf("tail = %q", got)
	}
	// One budget for both streams, evicted by arrival: a later stdout flood pushes out the stderr line.
	_, _ = out.Writer(workerpipe.Stdout).Write(bytes.Repeat([]byte("0123456789\n"), 10))
	got := readAll(t, out.Tail())
	if len(got) > 64 || strings.Contains(got, "load error") || !strings.HasSuffix(got, "0123456789\n") {
		t.Fatalf("tail after the flood = %q", got)
	}
}

func TestCloseReadSealsTheReader(t *testing.T) {
	out := workerpipe.New(workerpipe.Options{})
	r := out.Reader(workerpipe.Stderr)
	_, _ = out.Writer(workerpipe.Stderr).Write([]byte("queued\n"))
	if err := r.CloseRead(); err != nil {
		t.Fatal(err)
	}
	_, _ = out.Writer(workerpipe.Stderr).Write([]byte("after the seal\n"))
	if got := readAll(t, r); got != "queued\n" {
		t.Fatalf("sealed reader = %q", got)
	}
}

func TestCloseBeforeDrainsEndWaitsForThem(t *testing.T) {
	out := workerpipe.New(workerpipe.Options{})
	r := out.Reader(workerpipe.Stdout)
	pr, pw := io.Pipe()
	out.Drain(workerpipe.Stdout, pr)
	out.Close()
	select {
	case <-out.Done():
		t.Fatal("Done closed while a Drain still reads")
	case <-time.After(50 * time.Millisecond):
	}
	_, _ = pw.Write([]byte("written before exit\npartial"))
	_ = pw.Close()
	select {
	case <-out.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed after the Drain ended")
	}
	if got := readAll(t, r); got != "written before exit\npartial\n" {
		t.Fatalf("lines = %q", got)
	}
	out.Close() // idempotent
}
