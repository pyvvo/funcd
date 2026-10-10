package function

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/httpx"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// defaultLivenessTimeout is runtime.livenessTimeout's floor; the default is three supervision periods when larger
// (ADR-0215 Contracts).
const defaultLivenessTimeout = 30 * time.Second

// reasonDependencyNotReady is the Ready and RevisionReady reason of a replica not ready on a dependency report
// (ADR-0215 Decision 5).
const reasonDependencyNotReady = "DependencyNotReady"

// livenessRestartMessage is Ready's message while a replica hung on its liveness is replaced (ADR-0215 Decision 2).
const livenessRestartMessage = "a replica stopped answering /health/liveness and is being replaced"

// liveness is what this process knows of one worker's liveness: the CreatedAt it was seen with, its first probe and
// its last answer.
type liveness struct {
	created, first, last time.Time
}

// probeLiveness sends GET /health/liveness to in, a running worker that has listened, when it has an address, records
// an answer and reports whether in is hung (ADR-0215 Decision 1).
func (r *Reconciler) probeLiveness(ctx context.Context, in runtime.Instance) bool {
	now := r.clock.Now()
	r.seen(in, now)
	if r.listening(in) && r.probeOK(ctx, in.IP, in.Port, livenessPath) {
		r.markLive(in.ID, now)
	}
	return r.hung(in.ID, now)
}

// seen records in's first probe at now, afresh when in was created again under its id.
func (r *Reconciler) seen(in runtime.Instance, now time.Time) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	if l, ok := r.live[in.ID]; ok && l.created.Equal(in.CreatedAt) {
		return
	}
	r.live[in.ID] = liveness{created: in.CreatedAt, first: now}
}

// markLive records that worker id answered its liveness at `at`.
func (r *Reconciler) markLive(id runtime.InstanceID, at time.Time) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	l := r.live[id]
	if l.first.IsZero() {
		l.first = at
	}
	l.last = at
	r.live[id] = l
}

// hung reports whether livenessTimeout has passed at now since the latest of worker id's last answer, its creation and
// this process's first probe of it.
func (r *Reconciler) hung(id runtime.InstanceID, now time.Time) bool {
	r.liveMu.Lock()
	l, ok := r.live[id]
	r.liveMu.Unlock()
	if !ok {
		return false
	}
	latest := l.first
	for _, t := range []time.Time{l.created, l.last} {
		if t.After(latest) {
			latest = t
		}
	}
	return now.Sub(latest) >= r.livenessTimeout
}

// forgetLive drops the liveness of every worker named name in ns.
func (r *Reconciler) forgetLive(ns v1.NamespaceName, name v1.ObjectName) {
	prefix := string(ns) + "/" + string(name) + "/"
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	for id := range r.live {
		if strings.HasPrefix(string(id), prefix) {
			delete(r.live, id)
		}
	}
}

// forgetWorker drops worker id's liveness.
func (r *Reconciler) forgetWorker(id runtime.InstanceID) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	delete(r.live, id)
}

// restartHung retires each running, listening replica of revision rev below `below` that is hung on its liveness,
// with a Warn line, so the converge that follows creates the same replica index at once, outside the boot backoff
// (ADR-0215 Decision 2). It reports whether it retired one.
func (r *Reconciler) restartHung(ctx context.Context, fn *v1.Function, rev v1.ObjectName, below int) (bool, error) {
	if r.materializer == nil {
		return false, nil
	}
	insts, err := r.namedInstances(ctx, fn.Namespace, fn.Name)
	if err != nil {
		return false, err
	}
	restarted := false
	for _, in := range insts {
		if in.Revision != rev || in.Replica >= below || !r.listening(in) || !r.probeLiveness(ctx, in) {
			continue
		}
		r.logger.Warn("restarting a replica silent on its liveness", "namespace", fn.Namespace, "function", fn.Name, "replica", in.Replica)
		if err := r.retire(ctx, in); err != nil {
			return restarted, err
		}
		restarted = true
	}
	return restarted, nil
}

// replacingHung reports whether fn's Ready, as the previous pass left it, says a replica hung on its liveness is being
// replaced: the message holds until a replica is ready (ADR-0215 Decision 2).
func replacingHung(fn *v1.Function) bool {
	c, ok := fn.Status.Conditions.Get(condReady)
	return ok && c.Status == v1.ConditionFalse && c.Message == livenessRestartMessage
}

// probeOK issues GET path against a worker and reports a 200.
func (r *Reconciler) probeOK(ctx context.Context, ip string, port int, path string) bool {
	resp, err := r.probe(ctx, ip, port, path)
	if err != nil {
		return false
	}
	defer httpx.CloseBody(resp.Body)
	return resp.StatusCode == http.StatusOK
}

// probeReadiness issues GET /health/readiness against a shim (ADR-0030 §4b): ready on a 200; a 503 whose JSON body
// names a kind is a dependency report (ADR-0215 Decision 5); any other answer is not ready with no report.
func (r *Reconciler) probeReadiness(ctx context.Context, ip string, port int) (bool, *local.DependencyReport) {
	resp, err := r.probe(ctx, ip, port, readinessPath)
	if err != nil {
		return false, nil
	}
	defer httpx.CloseBody(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusServiceUnavailable:
		var rep local.DependencyReport
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&rep) == nil && rep.Kind != "" {
			return false, &rep
		}
	}
	return false, nil
}

func (r *Reconciler) probe(ctx context.Context, ip string, port int, path string) (*http.Response, error) {
	host := ip
	if host == "" {
		host = "127.0.0.1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:%d%s", host, port, path), nil)
	if err != nil {
		return nil, err
	}
	return r.httpClient.Do(req)
}

// reportMessage is a dependency report as Ready and RevisionReady carry it: `<kind> binding "<binding>": <message>`, or
// `socket: <message>` for the shim's own socket failure, which names no binding.
func reportMessage(rep *local.DependencyReport) string {
	if rep.Kind == local.DependencySocket {
		return fmt.Sprintf("%s: %s", rep.Kind, rep.Message)
	}
	return fmt.Sprintf("%s binding %q: %s", rep.Kind, rep.Binding, rep.Message)
}
