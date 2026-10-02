package local

import (
	"bytes"
	"context"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

// TestIssue127_InvokeCapsTheTargetResponse: the target's response is held in daemon memory, so the
// local API's invoke cap bounds it as it bounds the input — a response up to maxInvokeBytes is
// returned whole, a longer one fails without the daemon buffering it.
func TestIssue127_InvokeCapsTheTargetResponse(t *testing.T) {
	answering := func(size int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			chunk := make([]byte, 32<<10)
			for left := size; left > 0; left -= len(chunk) {
				_, _ = w.Write(chunk[:min(len(chunk), left)])
			}
		})
	}
	target := Ref{Namespace: "team-a", Function: "b"}

	out, err := NewInvoker(answering(maxInvokeBytes)).Invoke(context.Background(), target, []byte(`{}`), time.Second)
	require.NoError(t, err, "a response at the cap is returned")
	require.True(t, bytes.Equal(make([]byte, maxInvokeBytes), out))

	const size = 32 << 20
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = NewInvoker(answering(size)).Invoke(context.Background(), target, []byte(`{}`), time.Second)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	require.Less(t, allocated, uint64(8<<20), "invoking a target that answers %d bytes allocated %d bytes", size, allocated)
	require.Error(t, err, "a response past the cap must fail")
	require.Equal(t, fault.PayloadTooLarge, fault.KindOf(err))
}
