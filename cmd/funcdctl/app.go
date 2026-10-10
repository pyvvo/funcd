package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// appCmd groups the App verbs of ADR-0200 Decision 9, ADR-0212 Decision 9, ADR-0214 Decision 7 and ADR-0217: history
// reads an App's AppRevisions, rollback re-applies an earlier revision's spec, and pause and resume set spec.paused,
// each as an ordinary apply, so the App admission runs again with the caller's rights; retry calls a failed hook
// again; render, deploy and delete work from a template directory on the client.
func (a *cli) appCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage Apps (render|deploy|delete|history|rollback|pause|resume|retry)",
	}
	cmd.AddCommand(a.appRenderCmd(), a.appDeployCmd(), a.appDeleteCmd(), a.appHistoryCmd(), a.appRollbackCmd(),
		a.appPauseCmd("pause", true), a.appPauseCmd("resume", false), a.appRetryCmd())
	return cmd
}

// appRetryCmd starts again the failed hook of the App's latest revision (ADR-0214 Decision 7); it does not wait for
// the call, and the App's passes continue the rollout once it succeeds.
func (a *cli) appRetryCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "retry <app>",
		Short: "Call the App's failed hook again (the rollout continues once it succeeds)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			name := v1.ObjectName(args[0])
			if err := c.RetryApp(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), name); err != nil {
				return err
			}
			return a.writef("retrying the failed hook of %s\n", name)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

func (a *cli) appHistoryCmd() *cobra.Command {
	var ns, output string
	cmd := &cobra.Command{
		Use:   "history <app>",
		Short: "List an App's revisions by number (REVISION, VERSION, PHASE, STAMPED)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput("funcdctl app history", output, "json"); err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			revs, err := appHistory(cmd.Context(), c, v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]))
			if err != nil {
				return err
			}
			if output == "json" {
				objs := make([]v1.Object, len(revs))
				for i, r := range revs {
					objs[i] = r
				}
				return a.renderList(objs, true)
			}
			return a.renderAppHistory(revs)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

// appHistory returns app's AppRevisions by number: those whose spec.app.name is app and, while the App exists, whose
// controller is its UID, so the revisions of a deleted namesake that the GC has not collected yet are left out.
func appHistory(ctx context.Context, c *sdk.Client, ns v1.NamespaceName, app v1.ObjectName) ([]*v1.AppRevision, error) {
	var uid v1.UID
	switch obj, err := c.Get(ctx, v1.KindApp, ns, app); {
	case err == nil:
		uid = obj.GetObjectMeta().UID
	case fault.KindOf(err) != fault.NotFound:
		return nil, err
	}
	objs, err := c.List(ctx, v1.KindAppRevision, ns)
	if err != nil {
		return nil, err
	}
	revs := make([]*v1.AppRevision, 0, len(objs))
	for _, o := range objs {
		r := o.(*v1.AppRevision)
		if r.Spec.App.Name != app || (uid != "" && !v1.ControlledBy(r.OwnerReferences, v1.KindApp, uid)) {
			continue
		}
		revs = append(revs, r)
	}
	slices.SortFunc(revs, func(x, y *v1.AppRevision) int { return cmp.Compare(x.Spec.Number, y.Spec.Number) })
	return revs, nil
}

// renderAppHistory prints the history table. The version is the user's free label, so it passes through termSafe,
// which also escapes a tab that would shift the columns.
func (a *cli) renderAppHistory(revs []*v1.AppRevision) error {
	var rows strings.Builder
	rows.WriteString("REVISION\tVERSION\tPHASE\tSTAMPED\n")
	for _, r := range revs {
		rows.WriteString(strconv.FormatInt(r.Spec.Number, 10) + "\t" + orDash(termSafe(r.Spec.Spec.Version)) + "\t" +
			orDash(string(r.Status.Phase)) + "\t" + r.CreationTime.String() + "\n")
	}
	tw := tabwriter.NewWriter(a.out, 0, 0, 3, ' ', 0)
	if _, err := io.WriteString(tw, rows.String()); err != nil {
		return fault.Internalf("funcdctl app history", "write output: %v", err)
	}
	if err := tw.Flush(); err != nil {
		return fault.Internalf("funcdctl app history", "write output: %v", err)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *cli) appRollbackCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "rollback <app> <n>",
		Short: "Apply the spec of the App's revision n again (the App reconciler stamps it as a new revision)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			const op = "funcdctl app rollback"
			n, err := strconv.ParseInt(args[1], 10, 64)
			if err != nil || n < 1 {
				return fault.Invalidf(op, "revision %q is not a positive integer", args[1])
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			ctx, namespace, appName := cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0])
			name := v1.AppRevisionName(appName, n)
			revObj, err := c.Get(ctx, v1.KindAppRevision, namespace, name)
			if fault.KindOf(err) == fault.NotFound {
				return fault.Wrapf(err, fault.NotFound, op,
					"AppRevision %s does not exist: it was never stamped, or app.revisionHistory pruned it", name)
			}
			if err != nil {
				return err
			}
			rev := revObj.(*v1.AppRevision)
			applied, err := applyRead(ctx, c, v1.KindApp, namespace, appName, func(obj v1.Object) (bool, error) {
				app := obj.(*v1.App)
				if !v1.ControlledBy(rev.OwnerReferences, v1.KindApp, app.UID) {
					return false, fault.Conflictf(op,
						"AppRevision %s is not a revision of App %s (uid %s): its controller is another App", name, appName, app.UID)
				}
				same, err := sameAppSpec(app.Spec, rev.Spec.Spec)
				if same || err != nil {
					return false, err
				}
				paused := app.Spec.Paused
				app.Spec = rev.Spec.Spec
				app.Spec.Paused = paused
				return true, nil
			})
			if err != nil {
				return err
			}
			if !applied {
				return a.writef("no change: App %s already has the spec of %s\n", appName, name)
			}
			return a.writef("applied App %s with the spec of %s\n", appName, name)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

// sameAppSpec compares the two specs as the App reconciler's stamp does (ADR-0200 Decision 3, ADR-0212 Decision 3):
// json.Marshal of the typed spec without its pause, byte for byte.
func sameAppSpec(x, y v1.AppSpec) (bool, error) {
	bx, err := json.Marshal(x.WithoutPause())
	if err != nil {
		return false, fault.Internalf("funcdctl app rollback", "marshal the App spec: %v", err)
	}
	by, err := json.Marshal(y.WithoutPause())
	if err != nil {
		return false, fault.Internalf("funcdctl app rollback", "marshal the revision spec: %v", err)
	}
	return bytes.Equal(bx, by), nil
}

// deployPoll paces the deploy and delete wait loops (ADR-0217 Decisions 8 and 9).
const deployPoll = time.Second

// templateFlags are the flags render and deploy share (ADR-0217 Decisions 7 and 8).
type templateFlags struct {
	name, namespace, group string
	values                 []string
}

func (f *templateFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.name, "name", "", "the App's name (default: app.yaml's name)")
	cmd.Flags().StringVarP(&f.namespace, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&f.group, "resource-group", "", "resource group (default: the App's name, or the stored App's group)")
	cmd.Flags().StringArrayVarP(&f.values, "values", "f", nil, "a values file; repeat it to merge files in order")
}

// load reads the template directory and the values files.
func (f *templateFlags) load(dir string) (*template.Template, template.RenderInput, error) {
	t, err := template.Load(dir)
	if err != nil {
		return nil, template.RenderInput{}, err
	}
	in := template.RenderInput{
		Name:          cmp.Or(v1.ObjectName(f.name), t.Name),
		Namespace:     v1.NamespaceName(nsOrDefault(f.namespace)),
		ResourceGroup: v1.ResourceGroupName(f.group),
	}
	for _, path := range f.values {
		raw, err := template.ReadValues(path)
		if err != nil {
			return nil, template.RenderInput{}, err
		}
		in.Values = append(in.Values, raw)
	}
	return t, in, nil
}

func (a *cli) appRenderCmd() *cobra.Command {
	var f templateFlags
	var output string
	cmd := &cobra.Command{
		Use:   "render <dir>",
		Short: "Render an App template into the App deploy would apply, and print it (YAML; -o json)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := checkOutput("funcdctl app render", output, "json"); err != nil {
				return err
			}
			t, in, err := f.load(args[0])
			if err != nil {
				return err
			}
			app, err := template.Render(t, in)
			if err != nil {
				return err
			}
			return a.printApp(app, output == "json")
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

// printApp prints a rendered App without its empty status, as YAML or JSON.
func (a *cli) printApp(app *v1.App, asJSON bool) error {
	const op = "funcdctl app render"
	b, err := json.Marshal(app)
	if err != nil {
		return fault.Internalf(op, "marshal the App: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return fault.Internalf(op, "re-read the App: %v", err)
	}
	delete(fields, "status")
	if asJSON {
		b, err = json.MarshalIndent(fields, "", "  ")
		b = append(b, '\n')
	} else {
		b, err = yaml.Marshal(fields)
	}
	if err != nil {
		return fault.Internalf(op, "marshal the App: %v", err)
	}
	return a.writef("%s", b)
}

func (a *cli) appDeployCmd() *cobra.Command {
	var f templateFlags
	var noWait bool
	cmd := &cobra.Command{
		Use:   "deploy <dir>",
		Short: "Render an App template, apply the App and wait until its new revision is current or failed",
		Long: "Render an App template, apply the App and wait until its new revision is current or failed.\n\n" +
			"One command deploys, upgrades and downgrades. Deploy prints the revision it follows and each part whose " +
			"state changes, and exits 0 once the revision is current; 1 when it fails, when the App's spec changes " +
			"before it is stamped, or when the App is deleted. The server's app.upgradeTimeout bounds a rollout; an " +
			"interrupt stops the wait, not the rollout.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			t, in, err := f.load(args[0])
			if err != nil {
				return err
			}
			return a.deploy(cmd.Context(), c, t, in, noWait)
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the App is applied")
	return cmd
}

// deploy renders and applies the App unless the stored spec already equals it, retrying a conflict, then follows
// its revision (ADR-0217 Decision 8).
func (a *cli) deploy(ctx context.Context, c *sdk.Client, t *template.Template, in template.RenderInput, noWait bool) error {
	var (
		s   *deployStart
		err error
	)
	for attempt := 1; ; attempt++ {
		s, err = applyRendered(ctx, c, t, in)
		if err == nil || fault.KindOf(err) != fault.Conflict || attempt == applyAttempts {
			break
		}
	}
	if err != nil {
		return err
	}
	if s.same {
		held, err := revisionHolds(ctx, c, s.applied, s.n0)
		if err != nil {
			return err
		}
		if held {
			if err := a.writef("no change\n"); err != nil {
				return err
			}
		}
	}
	if noWait {
		return nil
	}
	return a.waitDeploy(ctx, c, s.applied, s.n0, s.ready)
}

// deployStart is what a deploy read and did before it waits: the App it rendered, the number n0 of the stored App's
// latestRevision, whether the stored spec already equaled the rendered one (so nothing was applied), and the stored
// App's Ready condition.
type deployStart struct {
	applied *v1.App
	n0      int64
	same    bool
	ready   string
}

// applyRendered reads the App, renders the template for it (the stored App's group unless --resource-group names
// another, which is refused) and applies the result on the read resourceVersion unless the stored spec equals it. The
// applied spec keeps the stored spec.paused, so a deploy never resumes an App (ADR-0212 Decision 9).
func applyRendered(ctx context.Context, c *sdk.Client, t *template.Template, in template.RenderInput) (*deployStart, error) {
	stored, err := storedApp(ctx, c, in.Namespace, in.Name)
	if err != nil {
		return nil, err
	}
	s := &deployStart{}
	if stored != nil {
		if in.ResourceGroup != "" && in.ResourceGroup != stored.ResourceGroup {
			return nil, fault.Invalidf("funcdctl app deploy", "--resource-group %s names another group than App %s's, %s",
				in.ResourceGroup, in.Name, stored.ResourceGroup)
		}
		in.ResourceGroup, s.ready = stored.ResourceGroup, readyLine(stored)
		if s.n0, err = revisionNumber(stored); err != nil {
			return nil, err
		}
	}
	if s.applied, err = template.Render(t, in); err != nil {
		return nil, err
	}
	if stored != nil {
		s.applied.Spec.Paused = stored.Spec.Paused
		if s.same, err = sameAppSpec(stored.Spec, s.applied.Spec); err != nil || s.same {
			return s, err
		}
		s.applied.ResourceVersion = stored.ResourceVersion
	}
	_, err = c.Apply(ctx, s.applied)
	return s, err
}

// storedApp gets the App, nil when it does not exist.
func storedApp(ctx context.Context, c *sdk.Client, ns v1.NamespaceName, name v1.ObjectName) (*v1.App, error) {
	obj, err := c.Get(ctx, v1.KindApp, ns, name)
	if fault.KindOf(err) == fault.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return obj.(*v1.App), nil
}

// revisionNumber is the number of the App's status.latestRevision, 0 when it has none.
func revisionNumber(app *v1.App) (int64, error) {
	latest := app.Status.LatestRevision
	if latest == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(string(latest), string(app.Name)+"-"), 10, 64)
	if err != nil {
		return 0, fault.Internalf("funcdctl app deploy", "App %s's latestRevision %q is not <app>-<number>", app.Name, latest)
	}
	return n, nil
}

// revisionHolds reports whether AppRevision <app>-<n> exists, is the App's, and holds the applied spec.
func revisionHolds(ctx context.Context, c *sdk.Client, applied *v1.App, n int64) (bool, error) {
	revs, err := appHistory(ctx, c, applied.Namespace, applied.Name)
	if err != nil {
		return false, err
	}
	rev, err := followed(revs, applied.Spec, n, n)
	return rev != nil, err
}

// followed is the highest revision numbered from n0 to latest that holds spec, or nil.
func followed(revs []*v1.AppRevision, spec v1.AppSpec, n0, latest int64) (*v1.AppRevision, error) {
	for i := len(revs) - 1; i >= 0; i-- {
		r := revs[i]
		if r.Spec.Number < n0 || r.Spec.Number > latest {
			continue
		}
		same, err := sameAppSpec(r.Spec.Spec, spec)
		if err != nil || same {
			return r, err
		}
	}
	return nil, nil
}

// waitDeploy follows the revision that holds the applied spec until it is current or failed. Once it follows one,
// it prints the revision's name and each child whose state or reason changed; it prints the App's Ready condition
// whenever it changes from ready, the one read before the apply, so a wait for a stamp (a hold, a paused App) is
// visible too.
func (a *cli) waitDeploy(ctx context.Context, c *sdk.Client, applied *v1.App, n0 int64, ready string) error {
	const op = "funcdctl app deploy"
	w := &deployWatch{children: map[string]string{}, ready: ready}
	for first := true; ; first = false {
		if !first {
			if err := sleepCtx(ctx, deployPoll); err != nil {
				return err
			}
		}
		app, err := storedApp(ctx, c, applied.Namespace, applied.Name)
		if err != nil {
			return err
		}
		if app == nil {
			return fault.NotFoundf(op, "App %s was deleted while deploy waited", applied.Name)
		}
		latest, err := revisionNumber(app)
		if err != nil {
			return err
		}
		revs, err := appHistory(ctx, c, app.Namespace, app.Name)
		if err != nil {
			return err
		}
		rev, err := followed(revs, applied.Spec, n0, latest)
		if err != nil {
			return err
		}
		if rev == nil {
			same, err := sameAppSpec(app.Spec, applied.Spec)
			if err != nil {
				return err
			}
			if !same {
				return fault.Conflictf(op, "App %s's spec changed before a revision of this deploy was stamped", app.Name)
			}
		}
		if err := a.report(w, app, rev); err != nil {
			return err
		}
		switch {
		case rev == nil:
		case app.Status.CurrentRevision == rev.Name:
			return nil
		case rev.Status.Phase == v1.PhaseFailed:
			c := failedCondition(rev)
			return fault.Conflictf(op, "AppRevision %s failed: %s: %s", rev.Name, c.Reason, c.Message)
		}
	}
}

// deployWatch is what a deploy has printed: the revision it follows, each child's state and the App's Ready.
type deployWatch struct {
	following v1.ObjectName
	children  map[string]string
	ready     string
}

// report prints what changed since the last poll: the followed revision and its children, when rev is set, then the
// App's Ready condition.
func (a *cli) report(w *deployWatch, app *v1.App, rev *v1.AppRevision) error {
	if rev != nil {
		if rev.Name != w.following {
			w.following = rev.Name
			if err := a.writef("%s\n", rev.Name); err != nil {
				return err
			}
		}
		for _, ch := range app.Status.Children {
			id := string(ch.Kind) + "/" + string(ch.Name)
			state := strings.TrimSpace(string(ch.State) + " " + ch.Reason)
			if w.children[id] == state {
				continue
			}
			w.children[id] = state
			if err := a.writef("%s %s\n", id, termSafe(state)); err != nil {
				return err
			}
		}
	}
	ready := readyLine(app)
	if ready == "" || ready == w.ready {
		return nil
	}
	w.ready = ready
	return a.writef("App/%s %s\n", app.Name, termSafe(ready))
}

// readyLine is the App's Ready condition as deploy prints it, "" when it has none.
func readyLine(app *v1.App) string {
	c, ok := app.Status.Conditions.Get("Ready")
	if !ok {
		return ""
	}
	return strings.TrimSpace("Ready=" + string(c.Status) + " " + c.Reason)
}

// failedCondition is why a revision failed: its Current condition when that carries a message (Superseded), else
// its ChildrenReady condition when False (the deadline's ChildNotReady naming the part), else Current.
func failedCondition(r *v1.AppRevision) v1.Condition {
	cur, _ := r.Status.Conditions.Get("Current")
	if c, ok := r.Status.Conditions.Get("ChildrenReady"); ok && c.Status == v1.ConditionFalse && cur.Message == "" {
		return c
	}
	return cur
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *cli) appDeleteCmd() *cobra.Command {
	var ns string
	var noWait bool
	cmd := &cobra.Command{
		Use:   "delete <app>",
		Short: "Delete an App, wait until the GC has removed its tree, and print what went and which stores stayed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			return a.deleteApp(cmd.Context(), c, v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]), noWait)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return once the App is deleted")
	return cmd
}

// deleteApp notes the App's tree and its own stores, deletes the App, waits until the tree is gone and reports the
// stores left (ADR-0217 Decision 9).
func (a *cli) deleteApp(ctx context.Context, c *sdk.Client, ns v1.NamespaceName, name v1.ObjectName, noWait bool) error {
	app, err := storedApp(ctx, c, ns, name)
	if err != nil {
		return err
	}
	if app == nil {
		return fault.NotFoundf("funcdctl app delete", "App %s does not exist in namespace %s", name, ns)
	}
	stores := ownStores(app)
	tree := newAppTree(app)
	if _, err := tree.walk(ctx, c, ns); err != nil {
		return err
	}
	if err := c.Delete(ctx, v1.KindApp, ns, name); err != nil {
		return err
	}
	if noWait {
		return nil
	}
	if err := a.waitTreeGone(ctx, c, ns, tree); err != nil {
		return err
	}
	for _, s := range stores {
		_, err := c.Get(ctx, s.Kind, ns, s.Name)
		switch {
		case err == nil:
			if err := a.writef("kept %s/%s\n", s.Kind, s.Name); err != nil {
				return err
			}
		case fault.KindOf(err) != fault.NotFound:
			return err
		}
	}
	return nil
}

// ownStores are the App's kv and buckets entries that it declares by name; a ref store is not the App's.
func ownStores(app *v1.App) []v1.ObjectRef {
	var stores []v1.ObjectRef
	for _, e := range app.Spec.KV {
		if e.Ref == "" {
			stores = append(stores, v1.ObjectRef{Kind: v1.KindKVStore, Namespace: app.Namespace, Name: e.Name})
		}
	}
	for _, e := range app.Spec.Buckets {
		if e.Ref == "" {
			stores = append(stores, v1.ObjectRef{Kind: v1.KindBucket, Namespace: app.Namespace, Name: e.Name})
		}
	}
	return stores
}

// waitTreeGone walks the tree every deployPoll, noting new descendants, prints each noted object as it goes and,
// once each, what a poll that removed nothing still found, until none is left.
func (a *cli) waitTreeGone(ctx context.Context, c *sdk.Client, ns v1.NamespaceName, tree *appTree) error {
	waiting := map[treeKey]bool{}
	for len(tree.noted) > 0 {
		if err := sleepCtx(ctx, deployPoll); err != nil {
			return err
		}
		present, err := tree.walk(ctx, c, ns)
		if err != nil {
			return err
		}
		var left []treeKey
		for _, k := range tree.noted {
			if present[k.ref()] == k.uid {
				left = append(left, k)
			} else if err := a.writef("deleted %s/%s\n", k.kind, k.name); err != nil {
				return err
			}
		}
		removed := len(left) < len(tree.noted)
		tree.noted = left
		for _, k := range left {
			if removed || waiting[k] {
				continue
			}
			waiting[k] = true
			if err := a.writef("waiting %s/%s\n", k.kind, k.name); err != nil {
				return err
			}
		}
	}
	return nil
}

// treeKinds are the child kinds gc.Pairs reaches from App, Secret excluded, in the order the pairs list them.
func treeKinds() []v1.Kind {
	var kinds []v1.Kind
	seen := map[v1.Kind]bool{v1.KindApp: true}
	for frontier := []v1.Kind{v1.KindApp}; len(frontier) > 0; frontier = frontier[1:] {
		for _, p := range gc.Pairs() {
			if p.Owner == frontier[0] && !seen[p.Child] && p.Child != v1.KindSecret {
				seen[p.Child] = true
				kinds = append(kinds, p.Child)
				frontier = append(frontier, p.Child)
			}
		}
	}
	return kinds
}

// treeKey is one object of an App's tree.
type treeKey struct {
	kind v1.Kind
	name v1.ObjectName
	uid  v1.UID
}

func (k treeKey) ref() v1.ObjectRef { return v1.ObjectRef{Kind: k.kind, Name: k.name} }

// appTree is an App's tree: every object controlled by the App or, transitively, by an object of the tree.
type appTree struct {
	kinds  []v1.Kind
	owners map[v1.UID]v1.Kind
	noted  []treeKey
}

func newAppTree(app *v1.App) *appTree {
	return &appTree{kinds: treeKinds(), owners: map[v1.UID]v1.Kind{app.UID: v1.KindApp}}
}

// walk lists each kind of the tree in the namespace, notes every object a noted owner controls until no new owner
// appears, and returns the UID of each object present.
func (t *appTree) walk(ctx context.Context, c *sdk.Client, ns v1.NamespaceName) (map[v1.ObjectRef]v1.UID, error) {
	var objs []v1.Object
	present := map[v1.ObjectRef]v1.UID{}
	for _, k := range t.kinds {
		list, err := c.List(ctx, k, ns)
		if err != nil {
			return nil, err
		}
		for _, o := range list {
			m := o.GetObjectMeta()
			present[v1.ObjectRef{Kind: k, Name: m.Name}] = m.UID
		}
		objs = append(objs, list...)
	}
	for grew := true; grew; {
		grew = false
		for _, o := range objs {
			m := o.GetObjectMeta()
			if _, noted := t.owners[m.UID]; noted {
				continue
			}
			ref, ok := v1.ControllerOf(m.OwnerReferences)
			if kind, owned := t.owners[ref.UID]; !ok || !owned || kind != ref.Kind {
				continue
			}
			kind := o.GroupVersionKind().Kind
			t.owners[m.UID] = kind
			t.noted = append(t.noted, treeKey{kind: kind, name: m.Name, uid: m.UID})
			grew = true
		}
	}
	return present, nil
}

// appPauseCmd builds the pause or resume verb (ADR-0212 Decision 9): it sets the App's spec.paused, as
// workflowPauseCmd sets a run's, through applyRead.
func (a *cli) appPauseCmd(verb string, paused bool) *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   verb + " <app>",
		Short: strings.ToUpper(verb[:1]) + verb[1:] + " an App (sets spec.paused: a paused App writes no part)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			name := v1.ObjectName(args[0])
			_, err = applyRead(cmd.Context(), c, v1.KindApp, v1.NamespaceName(nsOrDefault(ns)), name,
				func(obj v1.Object) (bool, error) {
					obj.(*v1.App).Spec.Paused = paused
					return true, nil
				})
			if err != nil {
				return err
			}
			return a.writef("%sd %s\n", verb, name)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}
