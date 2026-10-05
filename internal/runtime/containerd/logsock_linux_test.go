//go:build linux

package containerd

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/runtime"
)

func openLogSocket(t *testing.T, spec runtime.WorkerSpec, capture runtime.LogCaptureFunc) string {
	t.Helper()
	_, _, ln, dir, err := setupLogChannel("t", spec, capture)
	if err != nil {
		t.Fatalf("setupLogChannel: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = os.RemoveAll(dir)
	})
	return filepath.Join(dir, "log.sock")
}

func pollUntil(cond func() bool) bool {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return true
		}
	}
	return cond()
}

// A sandbox can dial its log socket at will, and every connection the daemon captures holds a reader of up to
// funclog.MaxLineBytes in the daemon. The connections one instance keeps open must stay within what its shim needs
// (one per process, plus one per member of a pool, whatever the pool limit), the daemon must close the rest at once,
// and a connection that ends must free its place for the next one.
func TestLogSocketBoundsLiveConnectionsPerInstance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		spec     runtime.WorkerSpec
		min, max int64
	}{
		{name: "solo", spec: runtime.WorkerSpec{Namespace: "n", Name: "f"}, min: 1, max: 4},
		{name: "pool above 32 members", spec: runtime.WorkerSpec{Namespace: "n", Name: "__pool__nodejs22__k", Members: 40}, min: 40, max: 44},
	} {
		t.Run(tc.name, func(t *testing.T) { checkLogConnBound(t, tc.spec, tc.min, tc.max) })
	}
}

func checkLogConnBound(t *testing.T, spec runtime.WorkerSpec, minLive, maxLive int64) {
	var live, peak atomic.Int64
	sock := openLogSocket(t, spec, func(_ runtime.WorkerSpec, r io.ReadCloser) {
		n := live.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		go func() {
			_, _ = io.Copy(io.Discard, r)
			_ = r.Close()
			live.Add(-1)
		}()
	})

	const dials = 256
	var refused atomic.Int64
	conns := make([]net.Conn, 0, dials)
	t.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	for range dials {
		c, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatalf("dial %d: %v", len(conns), err)
		}
		conns = append(conns, c)
		go func() {
			if _, err := c.Read(make([]byte, 1)); err != nil {
				refused.Add(1)
			}
		}()
	}
	settled := pollUntil(func() bool { return peak.Load() > maxLive || refused.Load()+live.Load() == dials })
	if p := peak.Load(); p > maxLive {
		t.Fatalf("the daemon held %d live log readers for one instance (%d dialed, at most %d needed): the log socket's connection bound is too loose", p, dials, maxLive)
	}
	if !settled {
		t.Fatalf("after %d dials: %d captured, %d closed by the daemon", dials, live.Load(), refused.Load())
	}
	if p := peak.Load(); p < minLive {
		t.Fatalf("the daemon captured only %d log connections (%d dialed, %d needed): it closes connections the shim needs", p, dials, minLive)
	}

	for _, c := range conns {
		_ = c.Close()
	}
	if !pollUntil(func() bool { return live.Load() == 0 }) {
		t.Fatalf("%d log readers still live after every connection closed", live.Load())
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial after close: %v", err)
	}
	conns = append(conns, c)
	if !pollUntil(func() bool { return live.Load() == 1 }) {
		t.Fatalf("a connection made after the others closed was not captured (live=%d): closed connections kept their places", live.Load())
	}
}

// One failed Accept (EMFILE while the daemon is out of file descriptors) must not end log capture for the instance.
func TestLogSocketKeepsAcceptingAfterTransientAcceptError(t *testing.T) {
	var calls atomic.Int64
	sock := openLogSocket(t, runtime.WorkerSpec{Namespace: "n", Name: "f"}, func(_ runtime.WorkerSpec, r io.ReadCloser) {
		calls.Add(1)
		go func() {
			_, _ = io.Copy(io.Discard, r)
			_ = r.Close()
		}()
	})

	var orig syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &orig); err != nil {
		t.Fatalf("getrlimit: %v", err)
	}
	low := orig
	low.Cur = 256
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &low); err != nil {
		t.Fatalf("setrlimit: %v", err)
	}
	var fillers []*os.File
	restore := func() {
		for _, f := range fillers {
			_ = f.Close()
		}
		fillers = nil
		_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &orig)
	}
	t.Cleanup(restore)
	for {
		f, err := os.Open(os.DevNull)
		if err != nil {
			if !errors.Is(err, syscall.EMFILE) {
				t.Fatalf("fill the fd table: %v", err)
			}
			break
		}
		fillers = append(fillers, f)
	}
	_ = fillers[len(fillers)-1].Close()
	fillers = fillers[:len(fillers)-1]
	// The client takes the last free fd, so the daemon's Accept of this connection fails with EMFILE.
	pressured, err := net.Dial("unix", sock)
	if err != nil {
		restore()
		t.Fatalf("dial under fd pressure: %v", err)
	}
	defer func() { _ = pressured.Close() }()
	time.Sleep(300 * time.Millisecond)
	restore()

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial after fd pressure: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte(`{"body":"after"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !pollUntil(func() bool { return calls.Load() > 0 }) {
		t.Fatal("no connection was captured after one transient Accept error: the accept loop ended for good")
	}
}
