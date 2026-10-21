//go:build dev

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

// The watch commands' timing (ADR-0255 Decision 3): dev's own stop grace (ADR-0167), the lines a failed command
// prints, and the cap of its restart delay, which a run at least that long resets to a second. Their output (ADR-0256):
// the most of it dev keeps to print, and how long the log file stays still before a burst ends, devWatchBuiltQuiet
// once a build the command owns was written, so a failing save soon after a build gets a burst of its own.
const (
	devWatchGrace      = process.DefaultStopGrace
	devWatchLastLines  = 20
	devWatchMaxDelay   = 30 * time.Second
	devWatchTail       = 64 << 10
	devWatchQuiet      = 3 * time.Second
	devWatchBuiltQuiet = time.Second
)

// ownedFiles reads the size and mtime of each file of the builds a watch command owns, by path (ADR-0256 Decision 2).
type ownedFiles func() map[string]string

// devCommands supervises the watch commands of ADR-0255: each runs as `sh -c <run>` in its own process group, recorded
// in the process dir's "commands" registry (ADR-0167), so a run that did not stop is reaped at the next start.
type devCommands struct {
	writef  func(format string, args ...any) error
	entries []devWatchEntry
	logs    map[string]string // name → its log file
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex // guards reg, finished and stopped
	reg     *procreg.Registry
	// finished holds the commands that exited 0; stopped refuses a start once stop began.
	finished map[string]bool
	stopped  bool
}

// startDevCommands reaps the commands a run that did not stop left behind, empties each command's log file and starts
// every command, each followed by its burst of output; owned reads the builds of each command that owns one.
func startDevCommands(ctx context.Context, op, processDir string, entries []devWatchEntry, owned map[string]ownedFiles, writef func(string, ...any) error) (*devCommands, error) {
	reg, err := procreg.Open(processDir, "commands")
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), op, "open the watch commands' registry")
	}
	if _, err := reg.Reap(ctx, devWatchGrace); err != nil {
		return nil, errors.Join(fault.Wrapf(err, fault.Internal, op, "reap an earlier run's watch commands"), reg.Close())
	}
	logDir := filepath.Join(processDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, errors.Join(fault.Wrapf(err, fault.Internal, op, "create %s", logDir), reg.Close())
	}
	cctx, cancel := context.WithCancel(ctx)
	c := &devCommands{writef: writef, entries: entries, logs: map[string]string{}, cancel: cancel, reg: reg, finished: map[string]bool{}}
	for _, e := range entries {
		path := filepath.Join(logDir, e.Name+".log")
		log, err := os.Create(path) //nolint:gosec // dev's own log file, named after a checked watch name
		if err != nil {
			return nil, errors.Join(fault.Wrapf(err, fault.Internal, op, "create %s", path), c.stop())
		}
		c.logs[e.Name] = path
		r, err := os.Open(path) //nolint:gosec // the log file dev just created
		if err != nil {
			_ = log.Close()
			return nil, errors.Join(fault.Wrapf(err, fault.Internal, op, "open %s", path), c.stop())
		}
		b := &devBurst{name: e.Name, log: r, files: owned[e.Name], quiet: devWatchQuiet, builtQuiet: devWatchBuiltQuiet}
		if err := writef("watch %s: %s\n", e.Name, path); err != nil {
			_, _ = log.Close(), r.Close()
			return nil, errors.Join(err, c.stop())
		}
		c.wg.Add(2)
		go c.follow(cctx, b)
		go c.supervise(cctx, e, log, b)
	}
	return c, nil
}

// supervise runs e until ctx ends: a command that exits non-zero is printed with its last lines and started again after
// a delay doubling up to devWatchMaxDelay, one that exits 0 is done. Each exit ends b's burst (ADR-0256 Decision 5).
func (c *devCommands) supervise(ctx context.Context, e devWatchEntry, log *os.File, b *devBurst) {
	defer c.wg.Done()
	defer func() { _ = log.Close() }()
	delay := time.Second
	for ctx.Err() == nil {
		began := time.Now()
		cmd := exec.Command("sh", "-c", e.Run) //nolint:gosec // the command funcdctl.config.yaml names (ADR-0255)
		cmd.Dir, cmd.Stdout, cmd.Stderr = e.Dir, log, log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		b.started()
		if err := c.start(e.Name, cmd); err != nil {
			if !errors.Is(err, errDevStopped) {
				_ = c.writef("watch %s did not start: %v\n", e.Name, err)
			}
			return
		}
		err := cmd.Wait()
		// The command's exit ends what it started, as a worker's exit does (ADR-0167).
		if pid := cmd.Process.Pid; pid > 1 {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		c.forget(e.Name)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			c.mu.Lock()
			c.finished[e.Name] = true
			c.mu.Unlock()
			_ = c.writef("%s%s finished\n", b.exited(true), e.Name)
			return
		}
		b.exited(false)
		wait, next := restartDelay(delay, time.Since(began))
		_ = c.writef("%s exited %d; restarting in %s\n%s", e.Name, commandExitCode(err), wait, lastLines(log.Name(), e.Name))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		delay = next
	}
}

// restartDelay is how long a command that failed after running for ran waits before it starts again, given the delay
// its last restart used, and the delay for the restart after: a second after a run of devWatchMaxDelay or more, else
// the last delay, doubling up to devWatchMaxDelay (ADR-0255 Decision 3).
func restartDelay(last, ran time.Duration) (wait, next time.Duration) {
	wait = last
	if ran >= devWatchMaxDelay {
		wait = time.Second
	}
	return wait, min(2*wait, devWatchMaxDelay)
}

// errDevStopped refuses a command's start once stop began.
var errDevStopped = errors.New("funcdctl dev is stopping")

// start starts cmd and records it under name, unless stop began.
func (c *devCommands) start(name string, cmd *exec.Cmd) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopped {
		return errDevStopped
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	st, err := procreg.StartTime(pid)
	if err != nil {
		return err
	}
	boot, err := procreg.BootID()
	if err != nil {
		return err
	}
	return c.reg.Put(procreg.Entry{ID: name, PID: pid, PGID: pid, StartTime: st, BootID: boot, Token: name})
}

// forget drops name's entry once its command exited.
func (c *devCommands) forget(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.reg.Delete(name)
}

// allFinished reports whether every command exited 0.
func (c *devCommands) allFinished() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.finished) == len(c.entries)
}

// stop ends every command: SIGTERM to each recorded process group, SIGKILL after devWatchGrace, through the registry's
// guard (procreg.Reap signals only an owned group above 1). A second stop does nothing.
func (c *devCommands) stop() error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return nil
	}
	c.stopped = true
	c.cancel()
	_, err := c.reg.Reap(context.Background(), devWatchGrace)
	c.mu.Unlock()
	c.wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Join(err, c.reg.Close())
}

// commandExitCode is a command's exit status, -1 when a signal ended it.
func commandExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// lastLines is the last devWatchLastLines lines of the log file at path, each prefixed with name, read from at most its
// last devWatchTail bytes.
func lastLines(path, name string) string {
	f, err := os.Open(path) //nolint:gosec // dev's own log file
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err == nil && fi.Size() > devWatchTail {
		_, _ = f.Seek(-devWatchTail, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
	return prefixLines(data, name)
}

// prefixLines is the last devWatchLastLines lines of data, each prefixed with name.
func prefixLines(data []byte, name string) string {
	lines := strings.Split(strings.TrimRight(string(bytes.ToValidUTF8(data, nil)), "\n"), "\n")
	if len(lines) > devWatchLastLines {
		lines = lines[len(lines)-devWatchLastLines:]
	}
	var b strings.Builder
	for _, l := range lines {
		if l != "" {
			b.WriteString(name + " | " + l + "\n")
		}
	}
	return b.String()
}

// follow polls b every devReloadPoll until ctx ends, printing each burst that led to no build (ADR-0256).
func (c *devCommands) follow(ctx context.Context, b *devBurst) {
	defer c.wg.Done()
	defer b.close()
	t := time.NewTicker(devReloadPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if lines := b.poll(time.Now()); lines != "" {
				_ = c.writef("%s", lines)
			}
		}
	}
}

// devBurst follows a watch command's log file as it grows (ADR-0256): the output since the command last went quiet,
// judged when it ends by whether a build the command owns was written meanwhile. The follow loop and the supervisor's
// starts and exits share it.
type devBurst struct {
	name              string
	log               *os.File      // read from where the last read stopped
	files             ownedFiles    // nil when the command owns no build
	quiet, builtQuiet time.Duration // devWatchQuiet and devWatchBuiltQuiet, which a test shortens
	mu                sync.Mutex    // guards the fields below and log
	off               int64         // how far log was read
	buf               []byte        // the open burst, its last devWatchTail bytes
	open              bool
	grew              time.Time         // when log last grew
	last              map[string]string // the owned files at the last poll or the command's start, whichever came later
	base              map[string]string // last, when the open burst's first line was read
}

// started reads the owned files as the command starts.
func (b *devBurst) started() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last = b.ownedNow()
}

// poll reads what the log file gained and ends the open burst once the log file stayed still long enough; it returns
// the lines to print.
func (b *devBurst) poll(now time.Time) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	files := b.ownedNow()
	if b.read() {
		b.grew = now
	}
	b.last = files
	if !b.open {
		return ""
	}
	quiet := b.quiet
	if b.wrote(files) {
		quiet = b.builtQuiet
	}
	if now.Sub(b.grew) < quiet {
		return ""
	}
	return b.end(files)
}

// wrote reports whether, since the open burst began, a file of a build the command owns was added or rewritten:
// removing files writes nothing (ADR-0256 Decision 3).
func (b *devBurst) wrote(files map[string]string) bool {
	for p, stamp := range files {
		if b.base[p] != stamp {
			return true
		}
	}
	return false
}

// exited ends the open burst as its command exits (ADR-0256 Decision 5): an exit 0 judges it, another drops it, as
// the exit prints the log file's last lines.
func (b *devBurst) exited(ok bool) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.read()
	if !b.open {
		return ""
	}
	if !ok {
		b.open, b.buf = false, nil
		return ""
	}
	return b.end(b.ownedNow())
}

// read appends what the log file gained since the last read to the burst, which it opens against the owned files the
// last poll or start read, and reports whether it gained any.
func (b *devBurst) read() bool {
	fi, err := b.log.Stat()
	if err != nil || fi.Size() <= b.off {
		return false
	}
	if fi.Size()-b.off > devWatchTail {
		b.off, b.buf = fi.Size()-devWatchTail, b.buf[:0]
	}
	data := make([]byte, fi.Size()-b.off)
	n, _ := b.log.ReadAt(data, b.off)
	if n == 0 {
		return false
	}
	b.off += int64(n)
	if !b.open {
		b.open, b.base = true, b.last
	}
	b.buf = append(b.buf, data[:n]...)
	if len(b.buf) > devWatchTail {
		b.buf = b.buf[len(b.buf)-devWatchTail:]
	}
	return true
}

// end closes the open burst: its last lines when no build the command owns was written since the burst began, else
// none.
func (b *devBurst) end(files map[string]string) string {
	data := b.buf
	b.open, b.buf = false, nil
	if b.wrote(files) {
		return ""
	}
	return prefixLines(data, b.name)
}

// ownedNow is the owned builds' files as they are now, none when the command owns no build.
func (b *devBurst) ownedNow() map[string]string {
	if b.files == nil {
		return nil
	}
	return b.files()
}

// close closes the log file's read handle once the follow loop ends.
func (b *devBurst) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = b.log.Close()
}

// buildMiss is a build a lookup did not find, which a watch command may still write (ADR-0255 Decision 2). It keeps the
// refusal's message and kind for a stop; wait, when set, is what dev prints while it waits, else the refusal's message.
type buildMiss struct {
	error
	wait string
}

func (m buildMiss) Unwrap() error { return m.error }

// waiting is what dev prints while it waits on m: its wait, else the refusal's message without its op.
func (m buildMiss) waiting() string {
	if m.wait != "" {
		return m.wait
	}
	var fe *fault.Error
	if errors.As(m.error, &fe) && fe.Err == nil {
		return fe.Msg
	}
	return m.Error()
}

// contractMiss is err, sdk.LoadContract's refusal, marked a miss when dir holds neither contract file of name
// (sdk's .contract.yaml and .contract.json) yet.
func contractMiss(dir, name string, err error) error {
	for _, ext := range []string{".contract.yaml", ".contract.json"} {
		if _, serr := os.Stat(filepath.Join(dir, name+ext)); serr == nil {
			return err
		}
	}
	return buildMiss{error: err, wait: fmt.Sprintf("the contract file of %s in %s", name, dir)}
}

// waitForBuilds calls lookup every devReloadPoll until it finds every build and the inputs' stamp it returns reads the
// same on two polls (#1118). A miss is printed once and waited on; another refusal, or a miss once every command exited
// 0, stops the wait once it holds on such a settled poll, so a file caught mid-write never stops dev. The end of ctx
// stops it too.
func waitForBuilds(ctx context.Context, cmds *devCommands, writef func(string, ...any) error, lookup func() (string, error)) error {
	prev, printed := "", ""
	t := time.NewTicker(devReloadPoll)
	defer t.Stop()
	for {
		stamp, err := lookup()
		if stamp == "" && err != nil {
			return err
		}
		settled := stamp == prev
		prev = stamp
		var miss buildMiss
		switch {
		case err == nil:
			if settled {
				return nil
			}
		case !errors.As(err, &miss) || cmds.allFinished():
			if settled {
				return err
			}
		default:
			if msg := miss.waiting(); msg != printed {
				printed = msg
				if werr := writef("waiting for %s\n", msg); werr != nil {
					return werr
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
