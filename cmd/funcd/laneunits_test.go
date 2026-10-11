package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The lanes started funcd as a bare transient unit (`systemd-run --unit funcd-demo`), which takes systemd's
// KillMode=control-group and 90 s stop timeout. A stop then sent SIGTERM to funcd, its private containerd and every
// shim at once: funcd's runtime Close dialed the exited containerd, failed after the client's 10 s connect timeout,
// and funcd exited 1 with its workers, their mounts, IP leases, iptables lines and the egress nftables tables left
// behind. Every funcd unit a lane or a suite starts must stop like the installed funcd.service.
func TestLaneDaemonUnitsStopLikeTheInstalledUnit(t *testing.T) {
	unit, err := renderUnit()
	require.NoError(t, err)
	var props []*regexp.Regexp
	for _, key := range []string{"KillMode", "TimeoutStopSec"} {
		m := regexp.MustCompile(`(?m)^` + key + `=(\S+)$`).FindStringSubmatch(unit)
		require.NotNil(t, m, "the installed unit sets %s", key)
		props = append(props, regexp.MustCompile(`(^|\s)-p\s+`+key+`=`+regexp.QuoteMeta(m[1])+`(\s|$)`))
	}

	root := repoRoot(t)
	call := regexp.MustCompile(`\bsystemd-run\b.*\s--unit[= ]funcd-`)
	var files []string
	for _, glob := range []string{"scripts/*.yaml", "scripts/*.sh", "e2e/*.venom.yml"} {
		m, err := filepath.Glob(filepath.Join(root, glob))
		require.NoError(t, err)
		files = append(files, m...)
	}
	calls := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		rel, err := filepath.Rel(root, file)
		require.NoError(t, err)
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") || !call.MatchString(line) {
				continue
			}
			calls++
			for _, p := range props {
				require.Regexp(t, p, line, "%s:%d starts a funcd unit that stops unlike funcd.service", rel, i+1)
			}
		}
	}
	require.NotZero(t, calls, "the lanes start funcd with systemd-run")
}
