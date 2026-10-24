//go:build linux

package containerd

import (
	"context"
	"sync"
	"syscall"
	"testing"
	"time"

	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/leases"
	ptypes "github.com/containerd/containerd/v2/pkg/protobuf/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/pyvvo/funcd/internal/runtime"
)

// stubbornTasks is the fake task service of workers that ignore SIGTERM: only SIGKILL stops a task. It records when
// each task got each signal.
type stubbornTasks struct {
	*createdTasks
	mu   sync.Mutex
	sent map[string]map[syscall.Signal]time.Time
}

func (s *stubbornTasks) Kill(ctx context.Context, req *tasksapi.KillRequest, opts ...grpc.CallOption) (*ptypes.Empty, error) {
	sig := syscall.Signal(req.Signal)
	s.mu.Lock()
	if s.sent == nil {
		s.sent = map[string]map[syscall.Signal]time.Time{}
	}
	if s.sent[req.ContainerID] == nil {
		s.sent[req.ContainerID] = map[syscall.Signal]time.Time{}
	}
	s.sent[req.ContainerID][sig] = time.Now()
	s.mu.Unlock()
	if sig == syscall.SIGTERM {
		return &ptypes.Empty{}, nil
	}
	return s.createdTasks.Kill(ctx, req, opts...)
}

// sentAt returns when task ctrID got sig, zero if never.
func (s *stubbornTasks) sentAt(ctrID string, sig syscall.Signal) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent[ctrID][sig]
}

// stopOne runs one worker on d and stops it with a single Stop, or with Close, the daemon stop, and returns its
// container id and how long the stop took.
func stopOne(t *testing.T, f *closeFixture, d *driver, daemonStop bool) (string, time.Duration) {
	t.Helper()
	ctrID, _ := f.create(t, d, "default", "stubborn")
	began := time.Now()
	if daemonStop {
		require.NoError(t, d.Close())
	} else {
		ctx := leases.WithLease(context.Background(), "stopgrace")
		require.NoError(t, d.Stop(ctx, runtime.NewInstanceID("default", "stubborn", "stubborn-1", 0)))
	}
	return ctrID, time.Since(began)
}

// scenario: one-grace-for-both-stops — with runtime.containerd.stopGrace set, a worker that ignores SIGTERM gets
// SIGKILL that grace after SIGTERM, at a single stop and at the daemon stop (ADR-0277).
func TestScenarioOneGraceForBothStops(t *testing.T) {
	const grace = 300 * time.Millisecond
	for name, daemonStop := range map[string]bool{"Stop": false, "Close": true} {
		t.Run(name, func(t *testing.T) {
			tasks := &stubbornTasks{createdTasks: &createdTasks{}}
			f := closeFixtureWith(t, containerd.WithTaskClient(tasks))
			d := f.driver(false)
			d.cfg.StopGrace = grace
			ctrID, _ := stopOne(t, f, d, daemonStop)
			term, kill := tasks.sentAt(ctrID, syscall.SIGTERM), tasks.sentAt(ctrID, syscall.SIGKILL)
			require.False(t, term.IsZero(), "the stop sends SIGTERM first")
			require.False(t, kill.IsZero(), "a worker that ignores SIGTERM gets SIGKILL")
			require.GreaterOrEqual(t, kill.Sub(term), grace, "SIGKILL waits the configured grace")
			require.Less(t, kill.Sub(term), grace+time.Second, "SIGKILL follows the configured grace, not another one")
		})
	}
}

// scenario: default-grace — with no key both stops use DefaultStopGrace, 10 s (TestScenarioOneGraceForBothStops shows
// that both wait the driver's grace), and a worker that exits on SIGTERM ends either stop at once, with no SIGKILL.
func TestScenarioDefaultGrace(t *testing.T) {
	require.Equal(t, 10*time.Second, DefaultStopGrace)
	for name, daemonStop := range map[string]bool{"Stop": false, "Close": true} {
		t.Run(name, func(t *testing.T) {
			f := closeFixtureWith(t)
			d := f.driver(false)
			require.Equal(t, DefaultStopGrace, d.StopGrace(), "no key: the default grace")
			ctrID, took := stopOne(t, f, d, daemonStop)
			require.Less(t, took, outputWait+time.Second, "a worker that exits on SIGTERM ends its stop without its grace")
			tasks := f.client.TaskService().(*createdTasks)
			require.Equal(t, uint32(syscall.SIGTERM), tasks.signal[ctrID], "the stop sends SIGTERM")
			require.Equal(t, []string{ctrID}, tasks.killed, "no SIGKILL after the worker exited on SIGTERM")
		})
	}
}
