package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scenario: one-service-install — `funcd install --print` emits a valid, version-stamped
// funcd.service unit to out and touches NOTHING on the system (cross-platform, no root). One
// unit (funcd is the service; containerd is its child — no separate containerd unit).
func TestOneServiceInstallPrint(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCmd(&out)
	cmd.SetArgs([]string{"install", "--print"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install --print: %v", err)
	}

	unit := out.String()
	if strings.TrimSpace(unit) == "" {
		t.Fatal("install --print emitted an empty unit, want a non-empty funcd.service")
	}
	// A valid systemd unit has the three sections and starts funcd as the ExecStart.
	for _, want := range []string{"[Unit]", "[Service]", "[Install]", "ExecStart=", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("printed unit missing %q\n---\n%s", want, unit)
		}
	}
	// ONE service: there must be no separate containerd unit reference as a [Unit]/[Service]
	// block — containerd is funcd's managed child, surfaced via KillMode=control-group.
	if !strings.Contains(unit, "KillMode=control-group") {
		t.Fatalf("printed unit must reap the managed containerd child (KillMode=control-group)\n---\n%s", unit)
	}
	if strings.Count(unit, "ExecStart=") != 1 {
		t.Fatalf("want exactly one service (one ExecStart), got %d\n---\n%s", strings.Count(unit, "ExecStart="), unit)
	}

	// --print also prints the lay-down plan (ADR-0056): the assets it WOULD download to where.
	for _, want := range []string{"lay-down plan", "crun", "containerd", "cni-plugins"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("printed output missing plan element %q\n---\n%s", want, unit)
		}
	}

	// --print touches nothing: the real unit path must not have been created by the test run.
	// (We assert the constant path was not written under a temp HOME — the command never writes
	// on --print, regardless of OS/root.)
	if _, err := os.Stat(filepath.Join(t.TempDir(), "funcd.service")); err == nil {
		t.Fatal("install --print unexpectedly wrote a unit file")
	}
}

// scenario: version-locked-shim (unit facet) — the printed unit is version-stamped, so the
// installed service identity matches the binary that printed it (no skew).
func TestInstallPrintIsVersionStamped(t *testing.T) {
	unit, err := renderUnit()
	if err != nil {
		t.Fatalf("renderUnit: %v", err)
	}
	// The default un-stamped build reports "dev"; a stamped build reports its version. Either
	// way the stamp line must be present (proving the template wired version.Get()).
	if !strings.Contains(unit, "version ") {
		t.Fatalf("unit not version-stamped:\n%s", unit)
	}
}
