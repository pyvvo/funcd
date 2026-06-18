package containerd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// scenario: funcd-writes-conflist-and-ipforward (renderer facet) — renderConflist emits valid
// JSON in the exact ADR-0011/0032 form: bridge funcd0 + the subnet + a firewall plugin.
func TestRenderConflist(t *testing.T) {
	const subnet = "10.63.0.0/16"
	raw := renderConflist(subnet)

	var conf struct {
		CNIVersion string `json:"cniVersion"`
		Name       string `json:"name"`
		Plugins    []struct {
			Type   string `json:"type"`
			Bridge string `json:"bridge"`
			IPAM   struct {
				Ranges [][]struct {
					Subnet string `json:"subnet"`
				} `json:"ranges"`
			} `json:"ipam"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatalf("conflist is not valid JSON: %v\n%s", err, raw)
	}
	if conf.CNIVersion != "1.0.0" || conf.Name != "funcd" {
		t.Fatalf("cniVersion/name = %q/%q, want 1.0.0/funcd", conf.CNIVersion, conf.Name)
	}
	if len(conf.Plugins) != 2 {
		t.Fatalf("want 2 plugins (bridge, firewall), got %d", len(conf.Plugins))
	}
	if conf.Plugins[0].Type != "bridge" || conf.Plugins[0].Bridge != "funcd0" {
		t.Errorf("plugin[0] = %q/%q, want bridge/funcd0", conf.Plugins[0].Type, conf.Plugins[0].Bridge)
	}
	if conf.Plugins[1].Type != "firewall" {
		t.Errorf("plugin[1] = %q, want firewall", conf.Plugins[1].Type)
	}
	if got := conf.Plugins[0].IPAM.Ranges[0][0].Subnet; got != subnet {
		t.Errorf("ipam subnet = %q, want %q", got, subnet)
	}
}

// scenario: funcd-writes-conflist-and-ipforward (write facet) — ensureConflist writes when
// absent, and leaves an existing conflist BYTE-FOR-BYTE untouched (never clobbers).
func TestEnsureConflistWritesIfAbsentLeavesExistingUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, conflistName)

	// Absent → written.
	if err := ensureConflist(dir, "10.63.0.0/16"); err != nil {
		t.Fatalf("ensureConflist (absent): %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("conflist not written: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("conflist written but empty")
	}

	// Present (operator's own) → left byte-for-byte untouched.
	const operator = `{"cniVersion":"1.0.0","name":"operator-custom"}`
	if err := os.WriteFile(path, []byte(operator), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureConflist(dir, "192.168.0.0/16"); err != nil {
		t.Fatalf("ensureConflist (present): %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != operator {
		t.Errorf("existing conflist was clobbered:\ngot:  %s\nwant: %s", after, operator)
	}
}

// scenario: funcd-writes-conflist-and-ipforward (ip_forward facet) — setIPForward writes "1" to
// its target path (the linux caller passes /proc/sys/net/ipv4/ip_forward).
func TestSetIPForwardWritesOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ip_forward")
	if err := setIPForward(path); err != nil {
		t.Fatalf("setIPForward: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "1\n" {
		t.Errorf("ip_forward content = %q, want %q", got, "1\n")
	}
}
