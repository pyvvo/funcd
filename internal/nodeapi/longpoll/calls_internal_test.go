package longpoll

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/nodeapi"
)

func TestCallsFailAtOnceForAnOfflineNode(t *testing.T) {
	calls := newCalls(time.Now)
	_, err := calls.do(context.Background(), "vm1", nodeapi.Call{Kind: nodeapi.CallLogs})
	require.Equal(t, fault.Unavailable, fault.KindOf(err))
	go func() {
		for {
			for _, c := range calls.poll(context.Background(), "vm1", time.Second) {
				_ = calls.answer("vm2", c.ID, nodeapi.Answer{Error: "wrong sender"})
				_ = calls.answer("vm1", c.ID, nodeapi.Answer{})
				return
			}
		}
	}()
	require.Eventually(t, func() bool { return calls.online("vm1") }, 2*time.Second, 10*time.Millisecond)
	_, err = calls.do(context.Background(), "vm1", nodeapi.Call{Kind: nodeapi.CallLogs})
	require.NoError(t, err, "only the node the call went to may answer it")
}
