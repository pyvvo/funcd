package app_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// paris is the fixture's todo-settings data (ADR-0213 Scenarios).
func paris() v1.ConfigMapSpec { return v1.ConfigMapSpec{Data: map[string]string{"TZ": "Europe/Paris"}} }

// configApp is ADR-0213's fixture: todoApp plus configMaps todo-settings and secrets todo-stripe-key (key
// STRIPE_API_KEY), both named by todo-api.
func configApp(mutate func(*v1.App)) *v1.App {
	return todoApp(func(a *v1.App) {
		a.Spec.ConfigMaps = []v1.AppConfigMap{{Name: "todo-settings", ConfigMapSpec: paris()}}
		a.Spec.Secrets = []v1.AppSecret{{Name: "todo-stripe-key", Description: "Stripe API access", Keys: []string{"STRIPE_API_KEY"}}}
		a.Spec.Functions[0].Config = []v1.ObjectName{"todo-settings"}
		a.Spec.Functions[0].Secrets = []v1.ObjectName{"todo-stripe-key"}
		if mutate != nil {
			mutate(a)
		}
	})
}

// putSecret creates or replaces the Secret todo-stripe-key with data.
func (h *harness) putSecret(data map[string][]byte) {
	h.t.Helper()
	if cur := h.get(v1.KindSecret, "todo-stripe-key"); cur != nil {
		cur.(*v1.Secret).Spec.Data = data
		h.update(cur)
		return
	}
	h.create(&v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
		ObjectMeta: v1.ObjectMeta{Name: "todo-stripe-key", Namespace: ns, ResourceGroup: "rg1"},
		Spec:       v1.SecretSpec{Type: v1.SecretTypeOpaque, Data: data}})
}

func stripeKey(v string) map[string][]byte { return map[string][]byte{"STRIPE_API_KEY": []byte(v)} }

// partCount counts the stored objects of every App section kind.
func (h *harness) partCount() int {
	h.t.Helper()
	n := 0
	for _, k := range []v1.Kind{v1.KindConfigMap, v1.KindKVStore, v1.KindBucket, v1.KindFunction, v1.KindWorkflow, v1.KindRoute} {
		res, err := h.st.List(h.ctx, k.GVK(), store.ListOptions{Namespace: ns})
		require.NoError(h.t, err)
		n += len(res.Items)
	}
	return n
}

// storedSettings is the stored name of todo-settings with data.
func storedSettings(data v1.ConfigMapSpec) v1.ObjectName {
	return v1.AppConfigMapName("todo-settings", data)
}

// Decision 8: a declared Secret missing, then lacking its key, stops the pass before any write; once complete the parts
// are written and todo-1 becomes current. A Secret has no child line.
func TestAppSecretCheckStopsBeforeAnyWrite(t *testing.T) {
	h := newHarness(t, nil)
	h.create(configApp(nil))
	require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter)
	require.Zero(t, h.partCount(), "no part is written")
	requireCond(t, h.ready(), v1.ConditionFalse, "SecretNotFound", "Secret/todo-stripe-key: no such Secret in the namespace")
	require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase)
	requireCond(t, revCond(t, h.rev(1), "Applied"), v1.ConditionFalse, "SecretNotFound", "Secret/todo-stripe-key: no such Secret in the namespace")
	for _, c := range h.app().Status.Children {
		require.NotEqual(t, v1.KindSecret, c.Kind, "a Secret is not a part")
	}
	require.Len(t, h.app().Status.Children, 8)

	h.putSecret(map[string][]byte{"OTHER": []byte("x")})
	h.reconcile()
	require.Zero(t, h.partCount())
	requireCond(t, h.ready(), v1.ConditionFalse, "SecretKeyMissing", "Secret/todo-stripe-key: lacks the declared key STRIPE_API_KEY")
	requireCond(t, revCond(t, h.rev(1), "Applied"), v1.ConditionFalse, "SecretKeyMissing", "Secret/todo-stripe-key: lacks the declared key STRIPE_API_KEY")

	h.putSecret(stripeKey("v1"))
	h.reconcile()
	require.Equal(t, 8, h.partCount())
	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-1"), h.app().Status.CurrentRevision)
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
	require.Nil(t, h.get(v1.KindSecret, "todo-stripe-key").GetObjectMeta().OwnerReferences, "the App never owns a Secret")
}

// Decision 8: the Secret check runs after the ownership check, in declaration order, and the first key missing is named.
func TestAppSecretCheckOrder(t *testing.T) {
	t.Run("after ChildNotOwned", func(t *testing.T) {
		h := newHarness(t, nil)
		h.create(&v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
			ObjectMeta: v1.ObjectMeta{Name: "todo-api", Namespace: ns, ResourceGroup: "rg1"}, Spec: apiSpec()})
		h.create(configApp(nil))
		h.reconcile()
		require.Equal(t, "ChildNotOwned", h.ready().Reason)
	})
	t.Run("declaration order and the first missing key", func(t *testing.T) {
		h := newHarness(t, nil)
		h.create(configApp(func(a *v1.App) {
			a.Spec.Secrets[0].Keys = []string{"STRIPE_API_KEY", "STRIPE_WEBHOOK_SECRET", "STRIPE_ACCOUNT"}
			a.Spec.Secrets = append(a.Spec.Secrets, v1.AppSecret{Name: "todo-mail", Keys: []string{"SMTP_URL"}})
		}))
		h.putSecret(stripeKey("v1"))
		h.reconcile()
		requireCond(t, h.ready(), v1.ConditionFalse, "SecretKeyMissing", "Secret/todo-stripe-key: lacks the declared key STRIPE_WEBHOOK_SECRET")
		h.putSecret(map[string][]byte{"STRIPE_API_KEY": []byte("a"), "STRIPE_WEBHOOK_SECRET": []byte("b"), "STRIPE_ACCOUNT": []byte("c")})
		h.reconcile()
		requireCond(t, h.ready(), v1.ConditionFalse, "SecretNotFound", "Secret/todo-mail: no such Secret in the namespace")
		require.Zero(t, h.partCount(), "a declared Secret no part names is checked too")
	})
}

// Decision 8: a Secret deleted under a Ready App degrades it; the stopped pass writes and prunes nothing.
func TestAppSecretCheckDegradesAfterReady(t *testing.T) {
	h := newHarness(t, nil)
	h.putSecret(stripeKey("v1"))
	h.install(configApp(nil))
	h.edit(func(a *v1.App) { a.Spec.Routes = nil })
	before := h.versionsAll()
	require.NoError(t, h.st.Delete(h.ctx, v1.KindSecret.GVK(), ns, "todo-stripe-key", ""))
	require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter)
	after := h.versionsAll()
	for k, rv := range before {
		if k.Kind != v1.KindApp && k.Kind != v1.KindAppRevision {
			require.Equal(t, rv, after[k], "%s/%s is not written", k.Kind, k.Name)
		}
	}
	require.NotNil(t, h.get(v1.KindRoute, "todo-api"), "a stopped pass prunes nothing")
	require.Equal(t, v1.PhaseDeploying, h.app().Status.Phase, "todo-2 is stamped and not current")
	require.Equal(t, "SecretNotFound", h.ready().Reason)

	h.edit(func(a *v1.App) { a.Spec.Routes = todoApp(nil).Spec.Routes })
	h.reconcile()
	h.markAllReady()
	h.putSecret(stripeKey("v1"))
	h.reconcile()
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-3"), h.app().Status.CurrentRevision)
	require.NoError(t, h.st.Delete(h.ctx, v1.KindSecret.GVK(), ns, "todo-stripe-key", ""))
	h.reconcile()
	require.Equal(t, v1.PhaseDegraded, h.app().Status.Phase)
	requireCond(t, h.ready(), v1.ConditionFalse, "SecretNotFound", "Secret/todo-stripe-key: no such Secret in the namespace")
}

// scenario: app-secret-declared (the reconciler half of the deadline) — the revision turns Failed at its deadline with
// the stop as its message; the Secret provided afterwards lets the parts be written while the revision and the App
// stay Failed, and a spec change stamps todo-2, which becomes current.
func TestAppSecretCheckFailsAtDeadline(t *testing.T) {
	h := newHarness(t, nil, func(d *app.Deps) { d.UpgradeTimeout = 20 * time.Second })
	h.create(configApp(versioned("1.0.0")))
	h.reconcile()
	h.clk.Advance(20 * time.Second)
	h.reconcile()
	one := h.rev(1)
	require.Equal(t, v1.PhaseFailed, one.Status.Phase)
	requireCond(t, revCond(t, one, "ChildrenReady"), v1.ConditionFalse, "ChildNotReady",
		"Secret/todo-stripe-key: SecretNotFound: no such Secret in the namespace")
	require.Equal(t, v1.PhaseFailed, h.app().Status.Phase)
	requireCond(t, h.ready(), v1.ConditionFalse, "SecretNotFound", "Secret/todo-stripe-key: no such Secret in the namespace")

	h.putSecret(stripeKey("v1"))
	h.reconcile()
	require.Equal(t, 8, h.partCount(), "the parts are written")
	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.PhaseFailed, h.rev(1).Status.Phase)
	require.Equal(t, v1.PhaseFailed, h.app().Status.Phase)
	requireCond(t, h.ready(), v1.ConditionFalse, "ChildNotReady", "Secret/todo-stripe-key: SecretNotFound: no such Secret in the namespace")

	h.edit(versioned("1.0.1"))
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
	require.Equal(t, v1.PhaseReady, h.app().Status.Phase)
}

// Decision 8 and the FEAT-0010 exit criterion: no Secret value reaches a log line, a status, an AppRevision or an
// error, and a changed value writes nothing.
func TestAppSecretValueNeverLeaks(t *testing.T) {
	const sentinel = "sentinel-0213-value"
	logs := &records{}
	h := newHarness(t, nil, logs.to, func(d *app.Deps) { d.UpgradeTimeout = 20 * time.Second })
	h.create(configApp(nil))
	h.putSecret(map[string][]byte{"OTHER": []byte(sentinel)})
	var errs []error
	pass := func() {
		_, err := h.reconcileErr()
		errs = append(errs, err)
	}
	pass()
	h.clk.Advance(20 * time.Second)
	pass()
	h.putSecret(stripeKey(sentinel))
	pass()
	h.markAllReady()
	pass()
	h.edit(versioned("2.0.0"))
	pass()
	h.markAllReady()
	pass()
	before := h.versionsAll()
	h.putSecret(stripeKey(sentinel + "-rotated"))
	pass()
	require.Equal(t, before, h.versionsAll(), "a changed value writes nothing")

	var seen []string
	for _, l := range logs.lines {
		seen = append(seen, l.msg)
		for _, v := range l.attrs {
			seen = append(seen, v)
		}
	}
	for _, err := range errs {
		if err != nil {
			seen = append(seen, err.Error())
		}
	}
	for _, o := range []v1.Object{h.app(), h.rev(1), h.rev(2)} {
		b, err := json.Marshal(o)
		require.NoError(t, err)
		seen = append(seen, string(b))
	}
	for _, s := range seen {
		require.NotContains(t, s, sentinel)
	}
}

// Decision 8: MapSecret requeues each App of the Secret's namespace that declares its name.
func TestMapSecret(t *testing.T) {
	h := newHarness(t, nil)
	h.create(configApp(nil))
	h.create(configApp(func(a *v1.App) {
		a.Name = "other"
		a.Spec = v1.AppSpec{Secrets: []v1.AppSecret{{Name: "other-key", Keys: []string{"K"}}}}
	}))
	elsewhere := configApp(nil)
	elsewhere.Namespace = "staging"
	h.create(elsewhere)
	secret := func(name v1.ObjectName) *v1.Secret {
		return &v1.Secret{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSecret.GVK().APIVersion(), Kind: v1.KindSecret},
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns}}
	}
	require.Equal(t, []controller.Request{{GVK: v1.KindApp.GVK(), Namespace: ns, Name: "todo"}}, h.r.MapSecret(h.ctx, secret("todo-stripe-key")))
	require.Equal(t, []controller.Request{{GVK: v1.KindApp.GVK(), Namespace: ns, Name: "other"}}, h.r.MapSecret(h.ctx, secret("other-key")))
	require.Empty(t, h.r.MapSecret(h.ctx, secret("unrelated")))
}

// Decision 9: an App stored before ADR-0213, whose part names a Secret it does not declare, keeps reconciling and
// writing its status.
func TestAppOlderAppWritesStatus(t *testing.T) {
	h := newHarness(t, nil)
	h.install(todoApp(func(a *v1.App) { a.Spec.Functions[0].Secrets = []v1.ObjectName{"todo-billing"} }))
	h.edit(func(a *v1.App) { a.Spec.Routes[0].Rules[0].Path = "/v2" })
	h.reconcile()
	h.markAllReady()
	h.reconcile()
	require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
	require.Equal(t, v1.ConditionTrue, h.ready().Status)
}

// Decisions 2, 4 and 5: the ConfigMap is written first, under its stored name, with the App's controller reference
// and Ready once it exists; todo-api names the stored name.
func TestAppConfigMapsWrittenFirst(t *testing.T) {
	h := newHarness(t, nil)
	h.putSecret(stripeKey("v1"))
	a := h.create(configApp(nil)).(*v1.App)
	h.reconcile()
	name := storedSettings(paris())
	cm := h.get(v1.KindConfigMap, name)
	require.NotNil(t, cm)
	require.Equal(t, paris(), cm.(*v1.ConfigMap).Spec)
	require.Equal(t, []v1.OwnerReference{{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Namespace: ns, Name: "todo"}, UID: a.UID,
		Controller: true, BlockOwnerDeletion: true}}, refsOf(cm))
	rv := func(o v1.Object) uint64 {
		v, err := store.ParseVersion(o.GetObjectMeta().ResourceVersion)
		require.NoError(t, err)
		return v.N
	}
	for _, c := range h.app().Status.Children[1:] {
		require.Less(t, rv(cm), rv(h.get(c.Kind, c.Name)), "the ConfigMap is written before %s/%s", c.Kind, c.Name)
	}
	require.Equal(t, v1.AppChild{Kind: v1.KindConfigMap, Name: name, State: v1.AppChildReady}, h.app().Status.Children[0])
	require.Equal(t, []v1.ObjectName{name}, h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Config)
	require.Equal(t, []v1.ObjectName{"todo-settings"}, h.app().Spec.Functions[0].Config, "the App's spec stays as declared")
	require.Equal(t, []v1.ObjectName{"todo-settings"}, h.rev(1).Spec.Spec.Functions[0].Config)
}

// scenario: app-config-change-rolls (the reconciler half) — a data change writes a new ConfigMap and repoints
// todo-api; the old one is pruned only after the switch and kept while a Function names it; a rollback creates it
// again; a ConfigMap the App does not define rolls nothing.
func TestAppConfigChangeRolls(t *testing.T) {
	utc := v1.ConfigMapSpec{Data: map[string]string{"TZ": "UTC"}}
	setTZ := func(spec v1.ConfigMapSpec) func(*v1.App) {
		return func(a *v1.App) { a.Spec.ConfigMaps[0].ConfigMapSpec = spec }
	}
	t.Run("pruned after the switch, created again by a rollback", func(t *testing.T) {
		h := newHarness(t, nil)
		h.putSecret(stripeKey("v1"))
		h.install(configApp(nil))
		h.edit(setTZ(utc))
		h.reconcile()
		require.NotNil(t, h.get(v1.KindConfigMap, storedSettings(utc)))
		require.Equal(t, []v1.ObjectName{storedSettings(utc)}, h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Config)
		require.NotNil(t, h.get(v1.KindConfigMap, storedSettings(paris())), "kept until the switch")
		require.Equal(t, v1.AppChild{Kind: v1.KindConfigMap, Name: storedSettings(paris()), State: v1.AppChildPruning, Reason: "NotCurrent"},
			h.child(v1.KindConfigMap, storedSettings(paris())))

		h.markReady(v1.KindFunction, "todo-api")
		require.Zero(t, h.reconcile())
		require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
		require.Nil(t, h.get(v1.KindConfigMap, storedSettings(paris())), "pruned in the switch pass")

		h.edit(setTZ(paris()))
		h.reconcile()
		require.NotNil(t, h.get(v1.KindConfigMap, storedSettings(paris())), "the rollback creates it again")
		require.Equal(t, []v1.ObjectName{storedSettings(paris())}, h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Config)
		h.markReady(v1.KindFunction, "todo-api")
		h.reconcile()
		require.Equal(t, v1.ObjectName("todo-3"), h.app().Status.CurrentRevision)
		require.Nil(t, h.get(v1.KindConfigMap, storedSettings(utc)))
	})
	t.Run("kept while a Function names it", func(t *testing.T) {
		h := newHarness(t, nil)
		h.putSecret(stripeKey("v1"))
		h.install(configApp(nil))
		audit := apiSpec()
		audit.Blob, audit.Config = nil, []v1.ObjectName{storedSettings(paris())}
		h.create(&v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction},
			ObjectMeta: v1.ObjectMeta{Name: "audit", Namespace: ns, ResourceGroup: "rg1"}, Spec: audit})
		h.edit(setTZ(utc))
		h.reconcile()
		h.markReady(v1.KindFunction, "todo-api")
		require.Equal(t, controller.SupervisionPeriod, h.reconcile().RequeueAfter)
		require.Equal(t, v1.ObjectName("todo-2"), h.app().Status.CurrentRevision)
		require.Equal(t, v1.AppChild{Kind: v1.KindConfigMap, Name: storedSettings(paris()), State: v1.AppChildPruning, Reason: "InUse: Function/audit"},
			h.child(v1.KindConfigMap, storedSettings(paris())))
		require.NoError(t, h.st.Delete(h.ctx, v1.KindFunction.GVK(), ns, "audit", ""))
		h.reconcile()
		require.Nil(t, h.get(v1.KindConfigMap, storedSettings(paris())))
	})
	t.Run("a ConfigMap the App does not define", func(t *testing.T) {
		h := newHarness(t, nil)
		h.putSecret(stripeKey("v1"))
		shared := h.create(&v1.ConfigMap{TypeMeta: v1.TypeMeta{APIVersion: v1.KindConfigMap.GVK().APIVersion(), Kind: v1.KindConfigMap},
			ObjectMeta: v1.ObjectMeta{Name: "shared", Namespace: ns, ResourceGroup: "rg1"}, Spec: paris()})
		h.install(configApp(func(a *v1.App) { a.Spec.Functions[0].Config = append(a.Spec.Functions[0].Config, "shared") }))
		require.Equal(t, []v1.ObjectName{storedSettings(paris()), "shared"}, h.get(v1.KindFunction, "todo-api").(*v1.Function).Spec.Config)
		shared.(*v1.ConfigMap).Spec = v1.ConfigMapSpec{Data: map[string]string{"TZ": "UTC"}}
		h.update(shared)
		h.quiet()
		require.Empty(t, refsOf(h.get(v1.KindConfigMap, "shared")))
	})
}

// Decisions 7 and 9: app-parts refuses a Secret a Function, a CatalogService or an image step names and spec.secrets
// does not declare, on create and on update; a ref step is exempt.
func TestAdmissionRefusesUndeclaredSecret(t *testing.T) {
	st := store.New(memory.New())
	billing := []v1.ObjectName{"todo-billing"}
	lake := func(secrets []v1.ObjectName) func(*v1.App) {
		return func(a *v1.App) {
			a.Spec.Catalogs = []v1.AppCatalog{{Name: "todo-lake", CatalogServiceSpec: v1.CatalogServiceSpec{
				Blob:    []v1.FunctionBlob{{Alias: "lake", Bucket: "todo-files", Prefix: "lake"}},
				Catalog: v1.CatalogRef{Bucket: "todo-files", Prefix: "lake"},
				Secrets: secrets,
			}}}
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*v1.App)
		want   string
	}{
		{"a Function", func(a *v1.App) { a.Spec.Functions[0].Secrets = append(a.Spec.Functions[0].Secrets, "todo-billing") },
			`spec.functions[0].secrets[1]: Function/todo-api names Secret "todo-billing", which spec.secrets does not declare`},
		{"an image step", func(a *v1.App) { a.Spec.Workflows[0].Steps[0].Function.Secrets = billing },
			`spec.workflows[0].steps[0].function.secrets[0]: Workflow/todo-plan names Secret "todo-billing", which spec.secrets does not declare`},
		{"a CatalogService", lake(billing),
			`spec.catalogs[0].secrets[0]: CatalogService/todo-lake names Secret "todo-billing", which spec.secrets does not declare`},
		{"an App without the section", func(a *v1.App) { a.Spec.Secrets = nil },
			`spec.functions[0].secrets[0]: Function/todo-api names Secret "todo-stripe-key", which spec.secrets does not declare`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := configApp(tc.mutate)
			for _, old := range []*v1.App{nil, configApp(nil)} {
				err := admit(t, st, partAdmissions(0), a, old)
				require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
	for name, mutate := range map[string]func(*v1.App){
		"the fixture": nil,
		"a ref step": func(a *v1.App) {
			a.Spec.Workflows[0].Steps = append(a.Spec.Workflows[0].Steps, v1.WorkflowStep{Name: "notify",
				Function: &v1.FunctionStep{Ref: "todo-api", Secrets: billing}})
		},
		"a declared CatalogService Secret": lake([]v1.ObjectName{"todo-stripe-key"}),
	} {
		require.NoError(t, admit(t, st, partAdmissions(0), configApp(mutate), nil), name)
	}
}
