package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// workflowCmd groups the workflow-run verbs (ADR-0094): run|runs|pause|resume|cancel|describe.
// run/runs/pause/resume/describe are sugar over the WorkflowRun CRUD surface; cancel calls the
// imperative control-plane cancel endpoint.
func (a *cli) workflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage workflow runs (run|runs|pause|resume|cancel|describe)",
	}
	cmd.AddCommand(
		a.workflowRunCmd(),
		a.workflowRunsCmd(),
		a.workflowPauseCmd("pause", true),
		a.workflowPauseCmd("resume", false),
		a.workflowCancelCmd(),
		a.workflowDescribeCmd(),
	)
	return cmd
}

// workflowRunCmd creates a WorkflowRun that starts <workflow> (a run of the named Workflow).
func (a *cli) workflowRunCmd() *cobra.Command {
	var ns, rg, input string
	cmd := &cobra.Command{
		Use:   "run <workflow> <run-name>",
		Short: "Start a workflow run (creates a WorkflowRun)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readInput(input)
			if err != nil {
				return err
			}
			run := &v1.WorkflowRun{
				TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
				ObjectMeta: v1.ObjectMeta{
					Name: v1.ObjectName(args[1]), Namespace: v1.NamespaceName(nsOrDefault(ns)), ResourceGroup: v1.ResourceGroupName(rg),
				},
				Spec: v1.WorkflowRunSpec{Workflow: v1.ObjectName(args[0]), Input: raw},
			}
			if verr := run.Validate(); verr != nil {
				return fault.Wrapf(verr, fault.KindOf(verr), "funcdctl workflow run", "invalid run")
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			applied, err := c.Apply(cmd.Context(), run)
			if err != nil {
				return err
			}
			return a.writef("started %s/%s\n", applied.GroupVersionKind().Kind, applied.GetName())
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&rg, "resource-group", "default", "resource group")
	cmd.Flags().StringVar(&input, "input", "", "run input: inline JSON or @file (default: empty)")
	return cmd
}

// workflowRunsCmd lists WorkflowRuns newest-first, optionally filtered by phase.
func (a *cli) workflowRunsCmd() *cobra.Command {
	var ns, phase, output string
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "List workflow runs (newest first; --phase to filter)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			objs, err := c.List(cmd.Context(), v1.KindWorkflowRun, v1.NamespaceName(nsOrDefault(ns)))
			if err != nil {
				return err
			}
			runs := make([]*v1.WorkflowRun, 0, len(objs))
			for _, o := range objs {
				r := o.(*v1.WorkflowRun)
				if phase != "" && !strings.EqualFold(string(r.Status.Phase), phase) {
					continue
				}
				runs = append(runs, r)
			}
			// newest first by creation timestamp.
			sort.SliceStable(runs, func(i, j int) bool {
				return runs[i].CreationTime.After(runs[j].CreationTime)
			})
			out := make([]v1.Object, len(runs))
			for i, r := range runs {
				out[i] = r
			}
			return a.renderList(out, output == "json")
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&phase, "phase", "", "filter by run phase (Pending|Running|Paused|Succeeded|Failed|Cancelled)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

// workflowPauseCmd builds the pause or resume verb: a declarative patch of spec.paused.
func (a *cli) workflowPauseCmd(verb string, paused bool) *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   verb + " <run>",
		Short: strings.ToUpper(verb[:1]) + verb[1:] + " a workflow run (patches spec.paused)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			obj, err := c.Get(cmd.Context(), v1.KindWorkflowRun, v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]))
			if err != nil {
				return err
			}
			run := obj.(*v1.WorkflowRun)
			run.Spec.Paused = paused
			if _, err := c.Apply(cmd.Context(), run); err != nil {
				return err
			}
			return a.writef("%sd %s\n", verb, run.GetName())
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

// workflowCancelCmd requests cancellation declaratively (patches spec.cancel), the same shape
// as pause/resume: the run reconciler observes it on the controller workqueue and abandons
// in-flight work, terminating the run Cancelled.
func (a *cli) workflowCancelCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "cancel <run>",
		Short: "Cancel a workflow run (requests cancellation via spec.cancel)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			obj, err := c.Get(cmd.Context(), v1.KindWorkflowRun, v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]))
			if err != nil {
				return err
			}
			run := obj.(*v1.WorkflowRun)
			run.Spec.Cancel = true
			if _, err := c.Apply(cmd.Context(), run); err != nil {
				return err
			}
			return a.writef("cancel requested for %s\n", run.GetName())
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

// workflowDescribeCmd reads a run's engine state via the API server (WorkflowRun.status mirror).
func (a *cli) workflowDescribeCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "describe <run>",
		Short: "Show a workflow run's full state (status mirrors the engine)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			obj, err := c.Get(cmd.Context(), v1.KindWorkflowRun, v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]))
			if err != nil {
				return err
			}
			return a.renderObject(obj, true)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

// nsOrDefault resolves an empty namespace flag to "default" (the funcdctl convention).
func nsOrDefault(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// readInput resolves a run-input flag: empty ⇒ nil, @file ⇒ the file's bytes, else the literal JSON.
func readInput(in string) (json.RawMessage, error) {
	if in == "" {
		return nil, nil
	}
	if strings.HasPrefix(in, "@") {
		data, err := os.ReadFile(in[1:]) //nolint:gosec // path is a user-supplied CLI argument
		if err != nil {
			return nil, fault.Invalidf("funcdctl workflow run", "read input file %q: %v", in[1:], err)
		}
		return data, nil
	}
	return json.RawMessage(in), nil
}
