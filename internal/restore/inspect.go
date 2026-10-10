package restore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/escrow"
	"github.com/pyvvo/funcd/internal/eventing/eventstore"
	"github.com/pyvvo/funcd/internal/secrets/aesgcm"
	"github.com/pyvvo/funcd/internal/snapshot"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	runbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// redacted replaces each Secret value Export does not reveal.
const redacted = "REDACTED"

// View is one generation loaded into memory engines (Decision 5): Meta decodes Secrets only with a secrets key, and
// Secrets lists them by key either way.
type View struct {
	Meta    store.Store
	Runs    runstate.Store
	Events  *eventstore.Store
	Secrets []v1.ObjectRef
}

// Close releases the memory engines.
func (v *View) Close() error {
	var errs []error
	if v.Meta != nil {
		errs = append(errs, v.Meta.Close())
	}
	return errors.Join(append(errs, v.Runs.Close(), v.Events.Close())...)
}

// Inspect reads g into memory engines and writes nothing (Decision 5). With o.SecretsKey (the escrowed key file,
// matched against the manifest's fingerprint) Meta decodes Secrets; without, View.Secrets lists them by key only.
func Inspect(ctx context.Context, g Generation, o Options) (*View, error) {
	const op = "restore.Inspect"
	if err := readable(op, g); err != nil {
		return nil, err
	}
	m := g.Manifest
	unseal, err := envelope.Opener(o.Identities)(m.Recipients)
	if err != nil {
		return nil, err
	}
	var opts []store.Option
	if o.SecretsKey != nil {
		if err := escrow.CheckSecretsKey(m, o.SecretsKey, o.EscrowDir); err != nil {
			return nil, err
		}
		enc, err := aesgcm.NewAESEncryptor(o.SecretsKey)
		if err != nil {
			return nil, fault.Wrapf(err, fault.Invalid, op, "--secrets-key")
		}
		opts = append(opts, store.WithEncryptor([]v1.Kind{v1.KindSecret}, enc))
	}
	runs, err := runbadger.New(runbadger.Config{InMemory: true})
	if err != nil {
		return nil, err
	}
	events, err := eventstore.Open(eventstore.Config{InMemory: true})
	if err != nil {
		return nil, errors.Join(err, runs.Close())
	}
	v := &View{Runs: runs, Events: events}
	eng := memory.New()
	if err := load(ctx, o.Source, g, unseal, "", map[string]snapshot.Loader{"events": events, "metastore": eng, "runs": runs}); err != nil {
		return nil, errors.Join(err, eng.Close(), v.Close())
	}
	v.Meta = store.New(eng, opts...)
	if v.Secrets, err = secretRefs(ctx, eng); err != nil {
		return nil, errors.Join(err, v.Close())
	}
	return v, nil
}

// Export is obj as `funcdctl apply -f` recreates it (Q14): no uid, resourceVersion, creation time or status. A
// Secret's values are REDACTED unless reveal.
func Export(obj v1.Object, reveal bool) (v1.Object, error) {
	const op = "restore.Export"
	kind := obj.GroupVersionKind().Kind
	data, err := json.Marshal(obj)
	var doc map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(data, &doc)
	}
	gvk := obj.GroupVersionKind()
	if err == nil {
		delete(doc, "status")
		doc["apiVersion"], err = json.Marshal(gvk.APIVersion())
	}
	if err == nil {
		doc["kind"], err = json.Marshal(kind)
	}
	if err == nil {
		data, err = json.Marshal(doc)
	}
	out, ok := v1.NewObject(kind)
	if !ok {
		return nil, fault.Internalf(op, "unknown kind %q", kind)
	}
	if err == nil {
		err = json.Unmarshal(data, out)
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "copy %s %s/%s", kind, obj.GetNamespace(), obj.GetName())
	}
	meta := out.GetObjectMeta()
	meta.UID, meta.ResourceVersion, meta.CreationTime = "", "", v1.Timestamp{}
	if s, ok := out.(*v1.Secret); ok && !reveal {
		for k := range s.Spec.Data {
			s.Spec.Data[k] = []byte(redacted)
		}
	}
	return out, nil
}

// Diff lists per kind the objects to adds, from drops and both hold with differing Export forms. Secrets that do
// not decode in either view compare by key only.
func Diff(from, to *View) (added, removed, changed map[v1.Kind][]v1.ObjectRef, err error) {
	ctx := context.Background()
	added, removed, changed = map[v1.Kind][]v1.ObjectRef{}, map[v1.Kind][]v1.ObjectRef{}, map[v1.Kind][]v1.ObjectRef{}
	for _, k := range v1.AllKinds() {
		a, aDecoded, err := exports(ctx, from, k)
		if err != nil {
			return nil, nil, nil, err
		}
		b, bDecoded, err := exports(ctx, to, k)
		if err != nil {
			return nil, nil, nil, err
		}
		for ref := range b {
			ea, ok := a[ref]
			switch {
			case !ok:
				added[k] = append(added[k], ref)
			case aDecoded && bDecoded && ea != b[ref]:
				changed[k] = append(changed[k], ref)
			}
		}
		for ref := range a {
			if _, ok := b[ref]; !ok {
				removed[k] = append(removed[k], ref)
			}
		}
	}
	for _, m := range []map[v1.Kind][]v1.ObjectRef{added, removed, changed} {
		for _, refs := range m {
			sortRefs(refs)
		}
	}
	return added, removed, changed, nil
}

// exports maps each object of kind k in v to its revealed Export form; Secrets that do not decode map by key to ""
// with decoded false.
func exports(ctx context.Context, v *View, k v1.Kind) (map[v1.ObjectRef]string, bool, error) {
	out := map[v1.ObjectRef]string{}
	l, err := v.Meta.List(ctx, k.GVK(), store.ListOptions{})
	if err != nil {
		if k != v1.KindSecret {
			return nil, false, err
		}
		for _, ref := range v.Secrets {
			out[ref] = ""
		}
		return out, false, nil
	}
	for _, obj := range l.Items {
		e, err := Export(obj, true)
		if err != nil {
			return nil, false, err
		}
		data, err := json.Marshal(e)
		if err != nil {
			return nil, false, fault.Wrapf(err, fault.Internal, "restore.Diff", "encode %s %s/%s", k, obj.GetNamespace(), obj.GetName())
		}
		out[v1.ObjectRef{Kind: k, Namespace: obj.GetNamespace(), Name: obj.GetName()}] = string(data)
	}
	return out, true, nil
}
