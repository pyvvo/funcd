package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// A command that runs beside another, as a worker's log follower runs beside its inspect, sees the whole state: every
// command rewrites state.json, and an inspect that read it half written found no container.
func TestConcurrentCommandsSeeTheWholeState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "port"), []byte("40000"), 0o600))
	require.Equal(t, 0, run(dir, []string{"run", "-d", "--name", "w", "-p", "127.0.0.1::8080", "img"}, strings.NewReader(""), io.Discard, io.Discard))
	var wg sync.WaitGroup
	failed := make(chan string, 400)
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				var out, errOut bytes.Buffer
				if run(dir, []string{"container", "inspect", "w"}, strings.NewReader(""), &out, &errOut) != 0 {
					failed <- errOut.String()
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				run(dir, []string{"logs", "--follow", "w"}, strings.NewReader(""), io.Discard, io.Discard)
			}
		}()
	}
	wg.Wait()
	close(failed)
	var msgs []string
	for m := range failed {
		msgs = append(msgs, m)
	}
	require.Empty(t, msgs, "an inspect beside other commands finds the container")
}
