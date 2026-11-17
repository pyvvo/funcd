package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/store"
)

// report writes back what the node's reconcilers change (FEAT-0002 decision 4): the status of each control-plane object,
// and the whole of each child the node owns, as the local store's watch shows them. A pass every resyncEvery catches
// what a failed write missed.
func (a *Agent) report(ctx context.Context) {
	events := make(chan store.Event, 256)
	for _, k := range v1.AllKinds() {
		if k.Namespaced() {
			go a.watch(ctx, k, events)
		}
	}
	a.reportBackup(ctx)
	t := time.NewTicker(resyncEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			a.push(ctx, ev.Type, ev.Object)
		case <-t.C:
			a.resyncReports(ctx)
			a.reportBackup(ctx)
		}
	}
}

// watch forwards kind k's local events; a closed watch opens again, listing every object as Added.
func (a *Agent) watch(ctx context.Context, k v1.Kind, out chan<- store.Event) {
	for ctx.Err() == nil {
		w, err := a.cfg.Store.Watch(ctx, k.GVK(), store.WatchOptions{})
		if err != nil {
			sleep(ctx, retryMax)
			continue
		}
		for ev := range w.ResultChan() {
			select {
			case out <- ev:
			case <-ctx.Done():
				w.Stop()
				return
			}
		}
		w.Stop()
	}
}

func (a *Agent) resyncReports(ctx context.Context) {
	a.mu.Lock()
	namespaces := make([]v1.NamespaceName, 0, len(a.mine))
	for ns := range a.mine {
		namespaces = append(namespaces, ns)
	}
	a.mu.Unlock()
	for _, ns := range namespaces {
		for _, k := range v1.AllKinds() {
			if !k.Namespaced() {
				continue
			}
			res, err := a.cfg.Store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
			if err != nil {
				continue
			}
			for _, obj := range res.Items {
				a.push(ctx, store.Modified, obj)
			}
		}
	}
}

// push writes one local change back, once: the pushed map keeps what the control plane already has.
func (a *Agent) push(ctx context.Context, typ store.EventType, obj v1.Object) {
	m := obj.GetObjectMeta()
	if !a.isMine(m.Namespace) {
		return
	}
	kind := obj.GroupVersionKind().Kind
	k := key(kind, m.Namespace, m.Name)
	whole := multinode.WrittenBack(kind, len(m.OwnerReferences) > 0)
	if typ == store.Deleted {
		a.forget(k)
		if whole {
			q := url.Values{"kind": {string(kind)}, "namespace": {string(m.Namespace)}, "name": {string(m.Name)}}
			if _, err := a.do(ctx, http.MethodDelete, multinode.PathObjects+"?"+q.Encode(), nil, nil); err != nil {
				a.log.WarnContext(ctx, "node agent: delete a child", "kind", kind, "name", m.Name, "error", err)
			}
		}
		return
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return
	}
	if whole {
		fp := fingerprint(fields)
		if a.wasPushed(k, fp) {
			return
		}
		if _, err := a.do(ctx, http.MethodPut, multinode.PathObjects, multinode.ObjectWrite{Kind: kind, Object: raw}, nil); err != nil {
			a.log.WarnContext(ctx, "node agent: write a child back", "kind", kind, "name", m.Name, "error", err)
			return
		}
		a.remember(k, fp)
		return
	}
	status := fields["status"]
	if len(status) == 0 || string(status) == "{}" || a.wasPushed(k, string(status)) {
		return
	}
	w := multinode.StatusWrite{Kind: kind, Namespace: m.Namespace, Name: m.Name, Status: status}
	if _, err := a.do(ctx, http.MethodPut, multinode.PathStatus, w, nil); err != nil {
		a.log.WarnContext(ctx, "node agent: write a status back", "kind", kind, "name", m.Name, "error", err)
		return
	}
	a.remember(k, string(status))
}

// fingerprint is a child's content without the fields each store mints.
func fingerprint(fields map[string]json.RawMessage) string {
	var meta map[string]json.RawMessage
	if json.Unmarshal(fields["metadata"], &meta) == nil {
		delete(meta, "resourceVersion")
		delete(meta, "uid")
		delete(meta, "generation")
		if b, err := json.Marshal(meta); err == nil {
			fields["metadata"] = b
		}
	}
	b, _ := json.Marshal(fields)
	return string(b)
}

func (a *Agent) wasPushed(k, v string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pushed[k] == v
}

func (a *Agent) remember(k, v string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pushed[k] = v
}

func (a *Agent) forget(k string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.pushed, k)
}

// serveCalls long-polls the node's call queue and answers each call from the node's own reads.
func (a *Agent) serveCalls(ctx context.Context) {
	backoff := retryMin
	for ctx.Err() == nil {
		var calls []multinode.Call
		code, err := a.do(ctx, http.MethodGet, multinode.PathCalls+"?wait="+pollWait.String(), nil, &calls)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			sleep(ctx, backoff)
			backoff = min(backoff*2, retryMax)
			continue
		}
		backoff = retryMin
		if code == http.StatusNoContent {
			continue
		}
		for _, c := range calls {
			go a.answer(ctx, c)
		}
	}
}

func (a *Agent) answer(ctx context.Context, c multinode.Call) {
	var ans multinode.Answer
	var err error
	if !a.isMine(c.Namespace) {
		err = fault.Forbiddenf("agent.answer", "namespace %s does not run on this node", c.Namespace)
	} else {
		ans, err = a.run(ctx, c)
	}
	if ans.Lines == nil {
		ans.Lines = []logread.Line{}
	}
	if err != nil {
		ans.Error, ans.Fault = err.Error(), fault.KindOf(err)
	}
	if _, perr := a.do(ctx, http.MethodPost, multinode.PathCalls+"/"+url.PathEscape(c.ID), ans, nil); perr != nil {
		a.log.WarnContext(ctx, "node agent: answer a call", "call", c.ID, "error", perr)
	}
}

// run runs call c on the node's local parts.
func (a *Agent) run(ctx context.Context, c multinode.Call) (multinode.Answer, error) {
	var ans multinode.Answer
	var err error
	cmd := a.cfg.Commands
	body := func(v any) { //nolint:forbidigo // the JSON body of a command's answer
		if err == nil {
			ans.Body, err = json.Marshal(v)
		}
	}
	switch {
	case c.Kind == multinode.CallLogs && a.cfg.Logs != nil:
		ans.Lines, err = a.cfg.Logs.Read(ctx, c.Query)
	case c.Kind == multinode.CallRunLogs && a.cfg.RunLogs != nil:
		ans.Lines, err = a.cfg.RunLogs.RunLogs(ctx, c.Namespace, c.Run, c.Query)
	case c.Kind == multinode.CallDeadLetters && cmd.DeadLetters != nil:
		var dls []deadletter.DeadLetter
		dls, err = cmd.DeadLetters.List(ctx, c.Namespace)
		body(dls)
	case c.Kind == multinode.CallDeadLetter && cmd.DeadLetters != nil:
		var dl deadletter.DeadLetter
		dl, err = cmd.DeadLetters.Get(ctx, c.Namespace, c.Name)
		body(dl)
	case c.Kind == multinode.CallDiscardDeadLetter && cmd.DeadLetters != nil:
		err = cmd.DeadLetters.Delete(ctx, c.Namespace, c.Name)
	case c.Kind == multinode.CallReplayDeadLetter && cmd.Replayer != nil:
		err = cmd.Replayer.Replay(ctx, c.Namespace, c.Name)
	case c.Kind == multinode.CallAppRetry && cmd.Apps != nil:
		err = cmd.Apps.Retry(ctx, c.Namespace, v1.ObjectName(c.Name))
	case c.Kind == multinode.CallVolumeDelete && cmd.Volumes != nil:
		err = cmd.Volumes.DeleteVolume(ctx, c.Namespace, v1.ObjectName(c.Name), c.Volume)
	case c.Kind == multinode.CallAdmit:
		var obj v1.Object
		if obj, err = a.admit(ctx, c); err == nil && obj != nil {
			body(obj)
		}
	default:
		err = fault.Unavailablef("agent.answer", "this node does not answer %s calls", c.Kind)
	}
	return ans, err
}

// admit runs the node's own node-state check that call c names (F250).
func (a *Agent) admit(ctx context.Context, c multinode.Call) (v1.Object, error) {
	var in multinode.AdmitRequest
	if err := json.Unmarshal(c.Body, &in); err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, "agent.admit", "decode the request")
	}
	req, err := in.Request()
	if err != nil {
		return nil, err
	}
	for _, adm := range a.cfg.Commands.Admissions {
		if adm.Name() == c.Name {
			if !adm.Handles(req.GVK, req.Operation) {
				return req.Object, nil
			}
			return adm.Admit(ctx, req)
		}
	}
	return nil, fault.Unavailablef("agent.admit", "this node runs no %s check", c.Name)
}

// reportBackup sends the node's platform backup status when it changed (F251), so the control plane's WorkerNode shows it.
func (a *Agent) reportBackup(ctx context.Context) {
	if a.cfg.Commands.Backup == nil {
		return
	}
	st, err := a.cfg.Commands.Backup(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "node agent: read the backup status", "error", err)
		return
	}
	b, err := json.Marshal(st)
	if err != nil || string(b) == a.backupSent {
		return
	}
	if _, err := a.do(ctx, http.MethodPut, multinode.PathBackup, st, nil); err != nil {
		a.log.WarnContext(ctx, "node agent: report the backup status", "error", err)
		return
	}
	a.backupSent = string(b)
}
