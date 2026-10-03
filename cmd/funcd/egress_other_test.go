//go:build !linux

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Issue 513: with server.network.egress on, the network-isolation (F80) and egress-gateway (F81) warnings
// go through the logger built from log.format/level, not the global slog default.
func TestIssue513_EgressWarningsUseConfiguredLogger(t *testing.T) {
	var leaked bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&leaked, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	t.Setenv("FUNCD_RUNTIME", "")

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	dir := shortDataDir(t)
	path := filepath.Join(dir, "funcdconfig.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"server:\n  listenAddr: \"127.0.0.1:0\"\n  dataPlaneAddr: \""+busy.Addr().String()+"\"\n"+
			"  network:\n    egress: true\n    egressGatewayPort: 15001\n    dnsForwarderPort: 15053\n"+
			"storage:\n  mode: memory\n  dataDir: \""+dir+"\"\n"+
			"log:\n  format: json\n  level: warn\n"), 0o600))

	var out bytes.Buffer
	cmd := newRootCmd(&out)
	cmd.SetArgs([]string{"--config", path})
	require.ErrorContains(t, cmd.Execute(), "bind data-plane listener")

	require.Empty(t, leaked.String(), "a warning bypassed the configured logger")
	msgs := map[string]bool{}
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		var line struct{ Msg string }
		require.NoError(t, json.Unmarshal(sc.Bytes(), &line), "want JSON lines only, got %q", sc.Text())
		msgs[line.Msg] = true
	}
	require.True(t, msgs["network isolation (F80) is enabled but unavailable on this platform; egress stays open"], "got %v", msgs)
	require.True(t, msgs["egress gateway (F81) is enabled but unavailable on this platform; egress stays open"], "got %v", msgs)
}
