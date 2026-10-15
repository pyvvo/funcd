package site

import (
	"bytes"
	"context"
	"fmt"
	"maps"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
)

// The Ready reasons of a runtimeConfig that renders nothing new (ADR-0239 Decision 7).
const (
	reasonConfigMapNotFound     = "ConfigMapNotFound"
	reasonRuntimeConfigTooLarge = "RuntimeConfigTooLarge"
)

// runtimeConfigPrefix is the key prefix of a Site's generated config.json: <prefix>/_runtime/<site>/, one per Site so
// two Sites on one prefix never share it. "_runtime" is never a digest slug, so servingDigest never takes it for the
// bundle.
func runtimeConfigPrefix(s *v1.Site) string {
	return s.Spec.Prefix + "/_runtime/" + string(s.Name) + "/"
}

func runtimeConfigKey(s *v1.Site) string { return runtimeConfigPrefix(s) + v1.SiteRuntimeConfigFile }

// writeRuntimeConfig renders the Site's config.json from its ConfigMap's data and its values, values winning, and writes
// it when it differs from the stored object. A reason means nothing was written: the ConfigMap is missing, or the file
// is over the cap or over what the Bucket accepts.
func (r *Reconciler) writeRuntimeConfig(ctx context.Context, s *v1.Site, view blob.Bucket) (reason, message string, err error) {
	rc := s.Spec.RuntimeConfig
	members := make(map[string]string, len(rc.Values))
	if rc.ConfigMap != "" {
		obj, gerr := r.store.Get(ctx, v1.KindConfigMap.GVK(), s.Namespace, rc.ConfigMap)
		if fault.KindOf(gerr) == fault.NotFound {
			return reasonConfigMapNotFound, fmt.Sprintf("configMap %q of spec.runtimeConfig does not exist", rc.ConfigMap), nil
		}
		if gerr != nil {
			return "", "", fault.Wrapf(gerr, fault.KindOf(gerr), op, "get configMap %q", rc.ConfigMap)
		}
		maps.Copy(members, obj.(*v1.ConfigMap).Spec.Data)
	}
	maps.Copy(members, rc.Values)
	body := v1.RenderSiteRuntimeConfig(members)
	if len(body) > v1.SiteRuntimeConfigMaxBytes {
		return reasonRuntimeConfigTooLarge, fmt.Sprintf("%s renders %d bytes, over the %d-byte cap", v1.SiteRuntimeConfigFile, len(body), v1.SiteRuntimeConfigMaxBytes), nil
	}
	key := runtimeConfigKey(s)
	stored, gerr := view.Get(ctx, key)
	switch {
	case gerr == nil && bytes.Equal(stored, body):
		return "", "", nil
	case gerr != nil && fault.KindOf(gerr) != fault.NotFound:
		return "", "", fault.Wrapf(gerr, fault.KindOf(gerr), op, "get %q", key)
	}
	if perr := view.Put(ctx, key, body, blob.PutOptions{ContentType: "application/json"}); perr != nil {
		switch fault.KindOf(perr) {
		case fault.PayloadTooLarge:
			return reasonRuntimeConfigTooLarge, perr.Error(), nil
		case fault.Invalid:
			return "MaterializeFailed", perr.Error(), nil
		}
		return "", "", fault.Wrapf(perr, fault.KindOf(perr), op, "put %q", key)
	}
	return "", "", nil
}

// dropRuntimeConfig deletes the generated config.json; the caller then compiles the Route without its rule, so a crash
// in between leaves the rule, and the next pass deletes again.
func dropRuntimeConfig(ctx context.Context, s *v1.Site, view blob.Bucket) error {
	key := runtimeConfigKey(s)
	if err := view.Delete(ctx, key); err != nil && fault.KindOf(err) != fault.NotFound {
		return fault.Wrapf(err, fault.KindOf(err), op, "delete %q", key)
	}
	return nil
}

// servesRuntimeConfig reports whether the owned Route still carries the Site's config.json rule: the durable record
// that drives removal.
func servesRuntimeConfig(rt *v1.Route, s *v1.Site) bool {
	if rt == nil {
		return false
	}
	want := runtimeConfigPrefix(s)
	for i := range rt.Spec.Rules {
		if st := rt.Spec.Rules[i].Backend.Static; st != nil && st.Prefix == want {
			return true
		}
	}
	return false
}

// MapConfigMap is the controller.MapFunc that re-runs the reconcile of every Site whose runtimeConfig names a changed
// ConfigMap: its config.json is rendered from it, and no Site event follows a ConfigMap write.
func (r *Reconciler) MapConfigMap(ctx context.Context, obj v1.Object) []controller.Request {
	meta := obj.GetObjectMeta()
	list, err := r.store.List(ctx, v1.KindSite.GVK(), store.ListOptions{Namespace: meta.Namespace})
	if err != nil {
		r.logger.WarnContext(ctx, "list sites of a changed configMap", "namespace", string(meta.Namespace), "configMap", string(meta.Name), "error", err)
		return nil
	}
	var reqs []controller.Request
	for _, o := range list.Items {
		if s, ok := o.(*v1.Site); ok && s.Spec.RuntimeConfig != nil && s.Spec.RuntimeConfig.ConfigMap == meta.Name {
			reqs = append(reqs, controller.Request{GVK: v1.KindSite.GVK(), Namespace: s.Namespace, Name: s.Name})
		}
	}
	return reqs
}
