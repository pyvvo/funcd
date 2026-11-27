package agent

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/nodeapi"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/store"
)

// report writes back what the node's reconcilers change (ADR-0366 Decision 4): the status of each control-plane object,
// and the whole of each child the node owns, as the local store's watch shows them. A pass every resyncEvery catches
// what a failed write missed.
func (a *Agent) report(ctx context.Context) {
	events := make(chan store.Event, 256)
	for _, k := range v1.AllKinds() {
		if k.Namespaced() {
			go a.watch(ctx, k, events)
		}
	}
	a.reportSelf(ctx)
	dropped := a.dropChildren(ctx)
	t, self := time.NewTicker(resyncEvery), time.NewTicker(selfEvery)
	defer t.Stop()
	defer self.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			_ = a.push(ctx, ev.Type, ev.Object)
		case <-a.resynced:
			a.resyncReports(ctx)
			dropped = a.dropChildren(ctx)
		case <-t.C:
			a.resendDeletes(ctx)
			a.resyncReports(ctx)
			dropped = dropped || a.dropChildren(ctx)
		case <-self.C:
			a.reportSelf(ctx)
		}
	}
}

// reportAgain makes the node report every status and child it holds again: a control plane it takes a snapshot of may
// have been restored to a point before the last reports.
func (a *Agent) reportAgain() {
	a.mu.Lock()
	a.pushed = map[string]string{}
	a.mu.Unlock()
	select {
	case a.resynced <- struct{}{}:
	default:
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
				_ = a.push(ctx, store.Modified, obj)
			}
		}
	}
}

// pushMade writes back the objects the node made with no owner that the control plane does not hold yet, and returns the
// keys of every one it holds. Each went through, or the control plane refused it (400, 403 or 409); any other failure is
// the error, and the node takes no snapshot, so the prune after a snapshot never drops one (ADR-0366 Decision 5).
func (a *Agent) pushMade(ctx context.Context) (map[string]bool, error) {
	a.mu.Lock()
	namespaces := slices.Collect(maps.Keys(a.mine))
	a.mu.Unlock()
	made := map[string]bool{}
	for _, ns := range namespaces {
		for _, k := range v1.AllKinds() {
			if !k.Namespaced() || !multinode.WrittenBack(k, false) {
				continue
			}
			res, err := a.cfg.Store.List(ctx, k.GVK(), store.ListOptions{Namespace: ns})
			if err != nil {
				return nil, err
			}
			for _, obj := range res.Items {
				m := obj.GetObjectMeta()
				if len(m.OwnerReferences) > 0 {
					continue
				}
				made[key(k, ns, m.Name)] = true
				if err := a.push(ctx, store.Modified, obj); err != nil && !slices.Contains(refusals, fault.KindOf(err)) {
					return nil, err
				}
			}
		}
	}
	return made, nil
}

// refusals are the control plane's answers that refuse a node's write for good: the write goes no further.
var refusals = []fault.Kind{fault.Invalid, fault.Forbidden, fault.Conflict} //nolint:gochecknoglobals // a constant list

// push writes one local change back, once: the pushed map keeps what the control plane already has. The error is the
// control plane's answer, or the link's.
func (a *Agent) push(ctx context.Context, typ store.EventType, obj v1.Object) error {
	m := obj.GetObjectMeta()
	if !a.isMine(m.Namespace) {
		return nil
	}
	kind := obj.GroupVersionKind().Kind
	k := key(kind, m.Namespace, m.Name)
	whole := multinode.WrittenBack(kind, len(m.OwnerReferences) > 0)
	if typ == store.Deleted {
		a.forget(k)
		if whole || m.DeletionTime != nil { // the node ended the deletion the control plane holds
			a.sendDelete(ctx, k, nodeapi.ObjectRef{Kind: kind, Namespace: m.Namespace, Name: m.Name, UID: m.UID})
		}
		return nil
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	if whole {
		fp := fingerprint(fields)
		if a.wasPushed(k, fp) {
			return nil
		}
		if err := a.client().PutObject(ctx, nodeapi.ObjectWrite{Kind: kind, Object: raw}); err != nil {
			a.log.WarnContext(ctx, "node agent: write a child back", "kind", kind, "name", m.Name, "error", err)
			return err
		}
		a.remember(k, fp)
		return nil
	}
	status := fields["status"]
	if len(status) == 0 || string(status) == "{}" || a.wasPushed(k, string(status)) {
		return nil
	}
	w := nodeapi.StatusWrite{Kind: kind, Namespace: m.Namespace, Name: m.Name, UID: m.UID, Status: status}
	if err := a.client().Status(ctx, w); err != nil {
		a.log.WarnContext(ctx, "node agent: write a status back", "kind", kind, "name", m.Name, "error", err)
		return err
	}
	a.remember(k, string(status))
	return nil
}

// sendDelete deletes on the control plane the object ref names, which the node deleted; one it did not take is sent again
// at the next pass.
func (a *Agent) sendDelete(ctx context.Context, k string, ref nodeapi.ObjectRef) {
	if err := a.client().DeleteObject(ctx, ref); err != nil {
		a.unsent[k+"/"+string(ref.UID)] = ref
		a.log.WarnContext(ctx, "node agent: delete a child", "kind", ref.Kind, "name", ref.Name, "error", err)
	}
}

func (a *Agent) resendDeletes(ctx context.Context) {
	for k, ref := range a.unsent {
		if err := a.client().DeleteObject(ctx, ref); err == nil {
			delete(a.unsent, k)
		}
	}
}

// fingerprint is a child's content without the version its own store mints.
func fingerprint(fields map[string]json.RawMessage) string {
	var meta map[string]json.RawMessage
	if json.Unmarshal(fields["metadata"], &meta) == nil {
		delete(meta, "resourceVersion")
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
		calls, err := a.client().Calls(ctx, pollWait)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			sleep(ctx, backoff)
			backoff = min(backoff*2, retryMax)
			continue
		}
		backoff = retryMin
		for _, c := range calls {
			go a.answer(ctx, c)
		}
	}
}

func (a *Agent) answer(ctx context.Context, c nodeapi.Call) {
	var ans nodeapi.Answer
	var err error
	if !nodeapi.OfTheNode(c.Kind) && !a.isMine(c.Namespace) {
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
	if perr := a.client().Answer(ctx, c.ID, ans); perr != nil {
		a.log.WarnContext(ctx, "node agent: answer a call", "call", c.ID, "error", perr)
	}
}

// run runs call c on the node's local parts.
func (a *Agent) run(ctx context.Context, c nodeapi.Call) (nodeapi.Answer, error) {
	var ans nodeapi.Answer
	var err error
	cmd := a.cfg.Commands
	body := func(v any) { //nolint:forbidigo // the JSON body of a command's answer
		if err == nil {
			ans.Body, err = json.Marshal(v)
		}
	}
	switch {
	case c.Kind == nodeapi.CallLogs && a.cfg.Logs != nil:
		ans.Lines, err = a.cfg.Logs.Read(ctx, c.Query)
	case c.Kind == nodeapi.CallRunLogs && a.cfg.RunLogs != nil:
		ans.Lines, err = a.cfg.RunLogs.RunLogs(ctx, c.Namespace, c.Run, c.Query)
	case c.Kind == nodeapi.CallDeadLetters && cmd.DeadLetters != nil:
		var dls []deadletter.DeadLetter
		dls, err = cmd.DeadLetters.List(ctx, c.Namespace)
		body(dls)
	case c.Kind == nodeapi.CallDeadLetter && cmd.DeadLetters != nil:
		var dl deadletter.DeadLetter
		dl, err = cmd.DeadLetters.Get(ctx, c.Namespace, c.Name)
		body(dl)
	case c.Kind == nodeapi.CallDiscardDeadLetter && cmd.DeadLetters != nil:
		err = cmd.DeadLetters.Delete(ctx, c.Namespace, c.Name)
	case c.Kind == nodeapi.CallReplayDeadLetter && cmd.Replayer != nil:
		err = cmd.Replayer.Replay(ctx, c.Namespace, c.Name)
	case c.Kind == nodeapi.CallAppRetry && cmd.Apps != nil:
		err = cmd.Apps.Retry(ctx, c.Namespace, v1.ObjectName(c.Name))
	case c.Kind == nodeapi.CallVolumeDelete && cmd.Volumes != nil:
		err = cmd.Volumes.DeleteVolume(ctx, c.Namespace, v1.ObjectName(c.Name), c.Volume)
	case c.Kind == nodeapi.CallReleaseNamespaces && cmd.Hold != nil:
		var nss []v1.NamespaceName
		if err = json.Unmarshal(c.Body, &nss); err == nil {
			err = a.releaseNamespaces(ctx, nss)
		}
	case c.Kind == nodeapi.CallReturnQuarantined && cmd.Hold != nil:
		var q hold.QuarantineReturn
		if err = json.Unmarshal(c.Body, &q); err == nil {
			if !a.isMine(q.Namespace) {
				err = fault.Forbiddenf("agent.answer", "namespace %s does not run on this node", q.Namespace)
			} else {
				err = cmd.Hold.ReturnQuarantined(ctx, q)
			}
		}
	case c.Kind == nodeapi.CallHoldStatus && cmd.Hold != nil:
		var ev hold.Evidence
		ev, err = cmd.Hold.Status(ctx)
		body(ev)
	case c.Kind == nodeapi.CallReleaseHold && cmd.Hold != nil:
		var advance []string
		if err = json.Unmarshal(c.Body, &advance); err == nil {
			err = cmd.Hold.Release(ctx, advance)
		}
	case c.Kind == nodeapi.CallBackupStatus:
		var st runner.Status
		if cmd.Backup != nil {
			st, err = cmd.Backup.Status(ctx)
		}
		body(st)
	case c.Kind == nodeapi.CallAdmit:
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
func (a *Agent) admit(ctx context.Context, c nodeapi.Call) (v1.Object, error) {
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

// reportSelf sends what the node reports of itself when it changed, so the control plane's WorkerNode shows it: the address
// of its blob store and its platform backup's status (F251).
func (a *Agent) reportSelf(ctx context.Context) {
	report := nodeapi.NodeReport{BlobStore: a.cfg.Commands.BlobStore}
	if b := a.cfg.Commands.Backup; b != nil {
		st, err := b.Status(ctx)
		if err != nil {
			a.log.WarnContext(ctx, "node agent: read the backup status", "error", err)
			return
		}
		nb := multinode.NodeBackup(st)
		report.Backup = &nb
	}
	b, err := json.Marshal(report)
	if err != nil || string(b) == a.reported {
		return
	}
	if err := a.client().Report(ctx, report); err != nil {
		a.log.WarnContext(ctx, "node agent: report the node", "error", err)
		return
	}
	a.reported = string(b)
}

// releaseNamespaces releases the holds of nss, which must all run on this node.
func (a *Agent) releaseNamespaces(ctx context.Context, nss []v1.NamespaceName) error {
	for _, ns := range nss {
		if !a.isMine(ns) {
			return fault.Forbiddenf("agent.answer", "namespace %s does not run on this node", ns)
		}
	}
	return a.cfg.Commands.Hold.ReleaseNamespaces(ctx, nss)
}
