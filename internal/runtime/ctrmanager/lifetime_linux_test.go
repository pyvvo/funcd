//go:build linux

package ctrmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"testing/synctest"
)

// Cancelling the context Ensure started containerd with (bench's Ctrl-C context) must not stop containerd: the
// callers' teardown still needs it, and Close stops it. synctest.Wait lets any reaction to the cancel finish first.
func TestIssue630_ContainerdOutlivesEnsureContextUntilClose(t *testing.T) {
	if isExecutable(filepath.Join(BinDir(), "containerd")) {
		t.Skip("a laid-down containerd in the bin dir would start in place of the fake one")
	}
	bin, root := t.TempDir(), shortRoot(t)
	ready, closed := filepath.Join(bin, "ready"), filepath.Join(bin, "closed")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake closes containerd's log pipe at once: the goroutine that copies it runs in the bubble, and
	// synctest.Wait would wait for it until the fake exits.
	fake := fmt.Sprintf("#!/bin/sh\nexec >/dev/null 2>&1\ntrap 'kill $!; : > %q; exit 0' INT\n: > %q\nsleep 30 & wait\n", closed, ready)
	if err := os.WriteFile(filepath.Join(bin, "containerd"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := &privateManager{cfg: Config{DataRoot: root}, socket: filepath.Join(root, "containerd.sock")}
	listenOn(t, m.socket)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		if err := m.startContainerd(ctx, "test"); err != nil {
			t.Fatalf("startContainerd: %v", err)
		}
		if _, err := os.ReadFile(ready); err != nil { // returns once the fake has set its INT trap
			t.Fatal(err)
		}
		cancel()
		synctest.Wait()
		if err := m.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if _, err := os.Stat(closed); err != nil {
			t.Errorf("containerd was gone before Close stopped it: cancelling the Ensure context killed it (%v)", err)
		}
	})
}
