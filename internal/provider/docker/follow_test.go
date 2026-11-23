package docker

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// followEngine is an Engine whose Follow serves set output; a follow calls nothing else.
type followEngine struct {
	Engine
	stdout, stderr string
}

func (f followEngine) Follow(context.Context, string) (io.ReadCloser, io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.stdout)), io.NopCloser(strings.NewReader(f.stderr)), nil
}

// ADR-0342 Decision 5: dev's runtime writes each engine line to the object's log file and hands it to FollowLines, a
// line over workerpipe.DefaultLineBytes split; the file's path is printed once per session, and a replacing engine's
// lines are appended.
func TestFollowKeepsEachLineInTheLogFileAndTheStore(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", workerpipe.DefaultLineBytes+10)
	r := &Runtime{eng: followEngine{stdout: "first\n" + long + "\n", stderr: "warn\n"}, logger: slog.Default(),
		logged: map[string]bool{}}
	r.follows, r.unfollow = context.WithCancel(context.Background())
	t.Cleanup(r.unfollow)
	dir := t.TempDir()
	var mu sync.Mutex
	var printed []string
	got := map[string][]string{}
	r.SetLogs(dir, func(format string, args ...any) error {
		mu.Lock()
		defer mu.Unlock()
		printed = append(printed, fmt.Sprintf(format, args...))
		return nil
	})
	r.FollowLines(func(_ provider.ProviderSpec, stream, line string) {
		mu.Lock()
		defer mu.Unlock()
		got[stream] = append(got[stream], line)
	})
	spec := provider.ProviderSpec{Ref: provider.ProviderRef{Namespace: "default", Name: "login"}, ServedKind: "IdentityProvider"}
	path := filepath.Join(dir, "default", "identityproviders.login.log")
	lines := func() int {
		b, _ := os.ReadFile(path) //nolint:gosec // the test's own temp dir
		return strings.Count(string(b), "\n")
	}

	r.follow(spec, "first")
	require.Eventually(t, func() bool { return lines() == 4 }, 5*time.Second, 10*time.Millisecond, "the file holds every line")
	mu.Lock()
	require.Equal(t, []string{"first", long[:workerpipe.DefaultLineBytes], long[workerpipe.DefaultLineBytes:]}, got["stdout"])
	require.Equal(t, []string{"warn"}, got["stderr"])
	require.Equal(t, []string{"engine identityprovider/login: " + path + "\n"}, printed)
	mu.Unlock()

	r.follow(spec, "second")
	require.Eventually(t, func() bool { return lines() == 8 }, 5*time.Second, 10*time.Millisecond,
		"a replacing engine's lines are appended")
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, printed, 1, "the path is printed once per session")
}

// The engine multiplexes a container's two streams in frames: demuxStream splits them and skips another stream's.
func TestDemuxStream(t *testing.T) {
	t.Parallel()
	frame := func(stream byte, s string) []byte {
		h := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(h[4:], uint32(len(s))) //nolint:gosec // the test's own short frames
		return append(h, s...)
	}
	in := bytes.Join([][]byte{frame(1, "out1\n"), frame(2, "err1\n"), frame(3, "other"), frame(1, "out2\n")}, nil)
	var out, errs bytes.Buffer
	require.NoError(t, demuxStream(bytes.NewReader(in), &out, &errs))
	require.Equal(t, "out1\nout2\n", out.String())
	require.Equal(t, "err1\n", errs.String())
	require.Error(t, demuxStream(bytes.NewReader(in[:len(in)-2]), io.Discard, io.Discard), "a cut frame")
}
