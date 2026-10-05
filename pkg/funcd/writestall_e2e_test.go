//go:build e2e

package funcd_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/pkg/funcd"
)

const answers32MiB = "export function handle() { return 'x'.repeat(32 * 1024 * 1024); }\n"

// scenario: stalled-reader-is-cut on the real listener chain — maxInFlight 1 and a Function answering 32 MiB;
// client A reads nothing and B calls 62 s later ⇒ B gets the whole response. The kernel can still take some of A's
// response a few seconds after A's last read, and the 60 s count from there, so B retries a 503 for up to 30 s.
func TestScenarioStalledReaderIsCutRealChain(t *testing.T) {
	t.Parallel()
	h := newShimRig(t, "", funcd.WithLimits(limit.Config{MaxInFlight: 1}))
	h.deploy(t, "big", nodeFn(answers32MiB))
	waitReady(t, h.c, "big")

	a, err := net.Dial("tcp", strings.TrimPrefix(h.dp, "http://"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = a.Close() })
	require.NoError(t, a.(*net.TCPConn).SetReadBuffer(64<<10))
	_, err = io.WriteString(a, "POST /function/big HTTP/1.1\r\nHost: funcd.test\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}")
	require.NoError(t, err)

	time.Sleep(62 * time.Second)
	r := h.postWithin("big", `{}`, 2*time.Minute)
	for retry := time.Now().Add(30 * time.Second); r.status == http.StatusServiceUnavailable && time.Now().Before(retry); {
		time.Sleep(time.Second)
		r = h.postWithin("big", `{}`, 2*time.Minute)
	}
	require.Equal(t, http.StatusOK, r.status, r.body)
	require.GreaterOrEqual(t, len(r.body), 32<<20)

	require.NoError(t, a.SetReadDeadline(time.Now().Add(10*time.Second)))
	n, err := io.Copy(io.Discard, a)
	var ne net.Error
	require.False(t, errors.As(err, &ne) && ne.Timeout(), "A's connection is still open: %v", err)
	require.Less(t, n, int64(32<<20), "A got the whole response")
}
