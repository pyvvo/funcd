package containerd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// conflistName is the funcd CNI conflist file the driver self-provisions (ADR-0056). It
// reproduces the EXACT ADR-0011/0032 bridge funcd0 + firewall form the driver has always loaded
// (the validated Lima/homebox conflist) — it does not add or alter the netns/lateral-deny model.
const conflistName = "10-funcd.conflist"

// The conflist schema, as typed structs (no map[string]any — keeps the marshalled JSON exactly
// the ADR-0011/0032 form while staying within the typed-surface lint rules). omitempty on the
// bridge-only fields lets the firewall plugin marshal to just {"type":"firewall"}.
type (
	cniConflist struct {
		CNIVersion string      `json:"cniVersion"`
		Name       string      `json:"name"`
		Plugins    []cniPlugin `json:"plugins"`
	}
	cniPlugin struct {
		Type      string   `json:"type"`
		Bridge    string   `json:"bridge,omitempty"`
		IsGateway bool     `json:"isGateway,omitempty"`
		IPMasq    bool     `json:"ipMasq,omitempty"`
		IPAM      *cniIPAM `json:"ipam,omitempty"`
	}
	cniIPAM struct {
		Type   string     `json:"type"`
		Ranges [][]cniSub `json:"ranges"`
		Routes []cniRoute `json:"routes"`
	}
	cniSub struct {
		Subnet string `json:"subnet"`
	}
	cniRoute struct {
		Dst string `json:"dst"`
	}
)

// renderConflist renders funcd's CNI conflist over subnetCIDR — the exact ADR-0011/0032 form:
// cniVersion 1.0.0, name "funcd", the bridge plugin (bridge funcd0, isGateway, ipMasq, host-local
// IPAM over subnetCIDR with a default route) then the firewall plugin. Pure + cross-platform.
func renderConflist(subnetCIDR string) []byte {
	conf := cniConflist{
		CNIVersion: "1.0.0",
		Name:       "funcd",
		Plugins: []cniPlugin{
			{
				Type: "bridge", Bridge: "funcd0", IsGateway: true, IPMasq: true,
				IPAM: &cniIPAM{
					Type:   "host-local",
					Ranges: [][]cniSub{{{Subnet: subnetCIDR}}},
					Routes: []cniRoute{{Dst: "0.0.0.0/0"}},
				},
			},
			{Type: "firewall"},
		},
	}
	// MarshalIndent over a fixed literal struct is deterministic and cannot fail.
	b, _ := json.MarshalIndent(conf, "", "  ")
	return append(b, '\n')
}

// ensureConflist writes funcd's conflist into confDir as 10-funcd.conflist ONLY if absent — it
// never clobbers an operator's existing conflist (left byte-for-byte untouched). Idempotent.
func ensureConflist(confDir, subnetCIDR string) error {
	if confDir == "" {
		return fmt.Errorf("ensureConflist: empty CNI conf dir")
	}
	if err := os.MkdirAll(confDir, 0o755); err != nil { //nolint:gosec // CNI conf dir is read by the CNI plugins
		return fmt.Errorf("create cni conf dir %q: %w", confDir, err)
	}
	path := filepath.Join(confDir, conflistName)
	if _, err := os.Stat(path); err == nil {
		return nil // an existing conflist is never clobbered
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %q: %w", path, err)
	}
	if err := os.WriteFile(path, renderConflist(subnetCIDR), 0o644); err != nil { //nolint:gosec // a CNI conflist is non-secret config
		return fmt.Errorf("write conflist %q: %w", path, err)
	}
	return nil
}

// setIPForward enables IPv4 forwarding by writing "1\n" to path (the linux caller passes
// /proc/sys/net/ipv4/ip_forward). Behind a writeFile seam so it is unit-checkable cross-platform.
// Idempotent — a harmless host-wide enable the in-process gateway needs to route to the bridge
// subnet (ADR-0056).
func setIPForward(path string) error {
	if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil { //nolint:gosec // a sysctl proc file is not a secret
		return fmt.Errorf("enable ip_forward via %q: %w", path, err)
	}
	return nil
}
