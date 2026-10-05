//go:build linux

package ctrmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// The private containerd runs in its own process group: a signal to funcd's group (a terminal's Ctrl-C) must not stop
// it while funcd's runtime Close still needs it to stop the workers, or Close fails and the egress fence stays up.
func TestPrivateContainerdHasItsOwnProcessGroup(t *testing.T) {
	m := startFakeContainerd(t)
	pgid, err := syscall.Getpgid(m.cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid == syscall.Getpgrp() {
		t.Fatalf("the private containerd shares funcd's process group %d, so a signal to funcd stops it too", pgid)
	}
}

// The private containerd logs through a pipe that funcd copies, not straight to funcd's stderr, which may be a terminal:
// a terminal treats its process group as a background job, so under `stty tostop` its first write there would stop it.
func TestPrivateContainerdLogsThroughAPipe(t *testing.T) {
	m := startFakeContainerd(t)
	own, err := os.Readlink("/proc/self/fd/2")
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	for _, fd := range []int{1, 2} {
		got, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", m.cmd.Process.Pid, fd))
		if err != nil {
			t.Fatalf("readlink: %v", err)
		}
		if got == own || !strings.HasPrefix(got, "pipe:") {
			t.Fatalf("the private containerd's fd %d is %s, not a pipe of its own (funcd's stderr is %s)", fd, got, own)
		}
	}
}

// startFakeContainerd starts a stand-in containerd, which creates its socket and sleeps, as the private one.
func startFakeContainerd(t *testing.T) *privateManager {
	t.Helper()
	if isExecutable(filepath.Join(BinDir(), "containerd")) {
		t.Skip("a laid-down containerd in the bin dir would start in place of the fake one")
	}
	bin := t.TempDir()
	fake := "#!/bin/sh\nwhile [ $# -gt 1 ]; do [ \"$1\" = --address ] && : > \"$2\"; shift; done\nexec sleep 30\n"
	if err := os.WriteFile(filepath.Join(bin, "containerd"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	m := &privateManager{cfg: Config{DataRoot: root}, socket: filepath.Join(root, "containerd.sock")}
	if err := m.startContainerd(context.Background(), "test"); err != nil {
		t.Fatalf("startContainerd: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
