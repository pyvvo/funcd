package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The PR watcher reports an open PR whatever its branch prefix, so the feat/ and impl/ PRs of the ADR pipeline
// are reported like the fix/ ones (issue #852).
func TestIssue852_WatchPRsReportsEveryBranchPrefix(t *testing.T) {
	const prs = `{"data":{"repository":{"pullRequests":{"nodes":[
{"number":842,"title":"feat pr","headRefName":"feat/adr-0197-log-durations-in-ms","headRefOid":"aaaaaaaaaaaaaaaa",
 "mergeStateStatus":"CLEAN","isInMergeQueue":false,
 "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[{"name":"ci","conclusion":"SUCCESS","status":"COMPLETED"}]}}}}]}},
{"number":851,"title":"impl pr","headRefName":"impl/adr-0199-app","headRefOid":"bbbbbbbbbbbbbbbb",
 "mergeStateStatus":"BLOCKED","isInMergeQueue":false,
 "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[{"name":"ci","conclusion":"FAILURE","status":"COMPLETED"}]}}}}]}},
{"number":900,"title":"fix pr","headRefName":"fix/900-thing","headRefOid":"cccccccccccccccc",
 "mergeStateStatus":"DIRTY","isInMergeQueue":false,"commits":{"nodes":[]}},
{"number":901,"title":"other pr","headRefName":"spike-no-prefix","headRefOid":"dddddddddddddddd",
 "mergeStateStatus":"DIRTY","isInMergeQueue":false,"commits":{"nodes":[]}}
]}}}}`
	bin := t.TempDir()
	snapshot := filepath.Join(bin, "prs.json")
	require.NoError(t, os.WriteFile(snapshot, []byte(prs), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\ncat '"+snapshot+"'\n"), 0o755))

	cmd := exec.Command("python3", filepath.Join("agent", "watch-prs.py"), filepath.Join(t.TempDir(), "handled.json"), "0")
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	code, out := exitCode(t, cmd)
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "GREEN    #842 [feat/adr-0197-log-durations-in-ms]")
	require.Contains(t, out, "RED      #851 [impl/adr-0199-app]")
	require.Contains(t, out, "CONFLICT #900 [fix/900-thing]")
	require.Contains(t, out, "CONFLICT #901 [spike-no-prefix]")
}
