package longpoll

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/nodeapi"
)

// oracle: docs/design/multinode/calls.fizz#NoCallWaitsForAnOfflineNode
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

// oracle: docs/design/multinode/calls.fizz#NoCallWaitsPastItsTimeout
// oracle: docs/design/multinode/calls.fizz#NoAnswerAfterItsCallEnded
func TestACallThatTimesOutRefusesItsLateAnswer(t *testing.T) {
	calls := newCalls(time.Now)
	calls.timeout = 50 * time.Millisecond
	taken := make(chan nodeapi.Call, 1)
	go func() {
		for {
			if cs := calls.poll(context.Background(), "vm1", time.Second); len(cs) > 0 {
				taken <- cs[0]
				return
			}
		}
	}()
	require.Eventually(t, func() bool { return calls.online("vm1") }, 2*time.Second, 10*time.Millisecond)
	_, err := calls.do(context.Background(), "vm1", nodeapi.Call{Kind: nodeapi.CallLogs})
	require.Equal(t, fault.Unavailable, fault.KindOf(err), "a node that took the call and did not answer")
	require.ErrorContains(t, err, "did not answer within")
	late := <-taken
	require.Equal(t, fault.NotFound, fault.KindOf(calls.answer("vm1", late.ID, nodeapi.Answer{})), "an answer after the call ended")
}
