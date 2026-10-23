package main

import (
	"strings"
	"testing"

	"github.com/pyvvo/funcd/internal/platform/config"
)

// TestVolumeCapacityForms (ADR-0266 Decision 3): the daemon refuses a config that sets both volumes.capacity and
// volumes.capacityBytes, and a capacity outside the size grammar; with neither, volumes stay off.
func TestVolumeCapacityForms(t *testing.T) {
	var cfg config.Config
	cfg.Volumes.Capacity, cfg.Volumes.CapacityBytes = "64Gi", 1
	if _, _, err := functionVolumes(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("both forms = %v, want a refusal", err)
	}
	cfg.Volumes.Capacity, cfg.Volumes.CapacityBytes = "64G", 0
	if _, _, err := functionVolumes(t.Context(), cfg); err == nil || !strings.Contains(err.Error(), "volumes.capacity") {
		t.Fatalf("a decimal capacity = %v, want a refusal", err)
	}
	cfg.Volumes.Capacity = ""
	opts, closer, err := functionVolumes(t.Context(), cfg)
	if err != nil || len(opts) != 0 || closer() != nil {
		t.Fatalf("neither form = %d options, %v; want volumes off", len(opts), err)
	}
}
