//go:build dev

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

// The watch commands' timing (ADR-0255 Decision 3): dev's own stop grace (ADR-0167), the lines a failed command
// prints, and the cap of its restart delay, which a run at least that long resets to a second.
const (
	devWatchGrace     = 3 * time.Second
	devWatchLastLines = 20
	devWatchMaxDelay  = 30 * time.Second
)

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
// every command.
func startDevCommands(ctx context.Context, op, processDir string, entries []devWatchEntry, writef func(string, ...any) error) (*devCommands, error) {
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
		if err := writef("watch %s: %s\n", e.Name, path); err != nil {
			_ = log.Close()
			return nil, errors.Join(err, c.stop())
		}
		c.wg.Add(1)
		go c.supervise(cctx, e, log)
	}
	return c, nil
}

// supervise runs e until ctx ends: a command that exits non-zero is printed with its last lines and started again after
// a delay doubling up to devWatchMaxDelay, one that exits 0 is done.
func (c *devCommands) supervise(ctx context.Context, e devWatchEntry, log *os.File) {
	defer c.wg.Done()
	defer func() { _ = log.Close() }()
	delay := time.Second
	for ctx.Err() == nil {
		began := time.Now()
		cmd := exec.Command("sh", "-c", e.Run) //nolint:gosec // the command funcdctl.config.yaml names (ADR-0255)
		cmd.Dir, cmd.Stdout, cmd.Stderr = e.Dir, log, log
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
			_ = c.writef("%s finished\n", e.Name)
			return
		}
		if time.Since(began) >= devWatchMaxDelay {
			delay = time.Second
		}
		_ = c.writef("%s exited %d; restarting in %s\n%s", e.Name, commandExitCode(err), delay, lastLines(log.Name(), e.Name))
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, devWatchMaxDelay)
	}
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
// last 64 KiB.
func lastLines(path, name string) string {
	f, err := os.Open(path) //nolint:gosec // dev's own log file
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	if fi, err := f.Stat(); err == nil && fi.Size() > 64<<10 {
		_, _ = f.Seek(-64<<10, io.SeekEnd)
	}
	data, _ := io.ReadAll(f)
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

// buildMiss is a build a lookup did not find, which a watch command may still write (ADR-0255 Decision 2). It keeps the
// refusal's message and kind.
type buildMiss struct{ error }

func (m buildMiss) Unwrap() error { return m.error }

// contractMiss is err, sdk.LoadContract's refusal, marked a miss when dir holds neither contract file of name
// (sdk's .contract.yaml and .contract.json) yet.
func contractMiss(dir, name string, err error) error {
	for _, ext := range []string{".contract.yaml", ".contract.json"} {
		if _, serr := os.Stat(filepath.Join(dir, name+ext)); serr == nil {
			return err
		}
	}
	return buildMiss{err}
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
			if msg := err.Error(); msg != printed {
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
