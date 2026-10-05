//go:build linux

package ctrmanager

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
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

// A containerd that died uncleanly leaves its socket file behind. startContainerd must not take it for the new containerd
// being ready, which listens only once its plugins are up: the ADR-0167 boot sweep's first call would be refused.
func TestIssue707_StartWaitsForAListeningSocketNotAStaleFile(t *testing.T) {
	if isExecutable(filepath.Join(BinDir(), "containerd")) {
		t.Skip("a laid-down containerd in the bin dir would start in place of the fake one")
	}
	bin, root := t.TempDir(), shortRoot(t)
	// The fake closes containerd's log pipe at once, so the goroutine that copies it leaves the bubble.
	if err := os.WriteFile(filepath.Join(bin, "containerd"), []byte("#!/bin/sh\nexec >/dev/null 2>&1\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := &privateManager{cfg: Config{DataRoot: root}, socket: filepath.Join(root, "containerd.sock")}
	stale, err := net.Listen("unix", m.socket)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()

	synctest.Test(t, func(t *testing.T) {
		defer func() { _ = m.Close() }()
		done := make(chan error, 1)
		go func() { done <- m.startContainerd(context.Background(), "test") }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("startContainerd returned (%v) while its socket was a stale file no containerd listened on", err)
		default:
		}
		if err := os.Remove(m.socket); err != nil {
			t.Fatal(err)
		}
		listenOn(t, m.socket)
		if err := <-done; err != nil {
			t.Fatalf("startContainerd once containerd listens: %v", err)
		}
	})
}

// shortRoot returns a data root whose containerd.sock fits the Unix socket path limit (#41), unlike a t.TempDir.
func shortRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctrd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// listenOn stands in for containerd listening on its socket.
func listenOn(t *testing.T, socket string) {
	t.Helper()
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

// startFakeContainerd starts a stand-in containerd, which sleeps, as the private one; the test listens on its socket.
func startFakeContainerd(t *testing.T) *privateManager {
	t.Helper()
	if isExecutable(filepath.Join(BinDir(), "containerd")) {
		t.Skip("a laid-down containerd in the bin dir would start in place of the fake one")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "containerd"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := shortRoot(t)
	m := &privateManager{cfg: Config{DataRoot: root}, socket: filepath.Join(root, "containerd.sock")}
	listenOn(t, m.socket)
	if err := m.startContainerd(context.Background(), "test"); err != nil {
		t.Fatalf("startContainerd: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
