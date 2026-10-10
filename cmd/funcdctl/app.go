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

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// appCmd groups the App verbs of ADR-0200 Decision 9 and ADR-0212 Decision 9: history reads an App's AppRevisions,
// rollback re-applies an earlier revision's spec, and pause and resume set spec.paused, each as an ordinary apply, so
// the App admission runs again with the caller's rights.
func (a *cli) appCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage Apps (history|rollback|pause|resume)",
	}
	cmd.AddCommand(a.appHistoryCmd(), a.appRollbackCmd(), a.appPauseCmd("pause", true), a.appPauseCmd("resume", false))
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
