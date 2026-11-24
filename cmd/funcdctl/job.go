package main

import (
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// jobCmd groups the run verbs of a Job (ADR-0347 Decision 10), as workflowCmd does a Workflow's: run, runs, logs,
// describe and cancel, over the JobRun surface; cancel sets spec.cancel.
func (a *cli) jobCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Run Jobs and read their runs (run|runs|logs|describe|cancel)",
	}
	cmd.AddCommand(a.jobRunCmd(), a.jobRunsCmd(), a.jobLogsCmd(), a.jobDescribeCmd(), a.jobCancelCmd())
	return cmd
}

// jobRunCmd creates a JobRun of <job> and prints its name.
func (a *cli) jobRunCmd() *cobra.Command {
	var ns, rg, input string
	cmd := &cobra.Command{
		Use:   "run <job> [run-name]",
		Short: "Start a run of a Job and print its name (omit run-name for a server-generated <job>-<id>)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			const op = "funcdctl job run"
			raw, err := readInput(op, input)
			if err != nil {
				return err
			}
			meta := v1.ObjectMeta{Namespace: v1.NamespaceName(nsOrDefault(ns)), ResourceGroup: v1.ResourceGroupName(rg)}
			if len(args) == 2 {
				meta.Name = v1.ObjectName(args[1])
			} else {
				meta.GenerateName = args[0] + "-"
			}
			run := &v1.JobRun{TypeMeta: v1.TypeMeta{APIVersion: v1.KindJobRun.GVK().APIVersion(), Kind: v1.KindJobRun},
				ObjectMeta: meta, Spec: v1.JobRunSpec{Job: v1.ObjectName(args[0]), Input: raw}}
			if meta.Name != "" { // a generated name is checked by the server once it is assigned
				if verr := run.Validate(); verr != nil {
					return fault.Wrapf(verr, fault.KindOf(verr), op, "invalid run")
				}
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			created, err := c.Create(cmd.Context(), run)
			if err != nil {
				return err
			}
			return a.writef("%s\n", created.GetName())
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&rg, "resource-group", "default", "resource group")
	cmd.Flags().StringVar(&input, "input", "", "run input: inline JSON or @file (default: none)")
	return cmd
}

// jobRunsCmd lists a Job's runs newest first, optionally of one phase.
func (a *cli) jobRunsCmd() *cobra.Command {
	var ns, phase, output string
	cmd := &cobra.Command{
		Use:   "runs <job>",
		Short: "List a Job's runs (newest first; --phase to filter)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput("funcdctl job runs", output, "json"); err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			objs, err := c.List(cmd.Context(), v1.KindJobRun, v1.NamespaceName(nsOrDefault(ns)))
			if err != nil {
				return err
			}
			runs := make([]*v1.JobRun, 0, len(objs))
			for _, o := range objs {
				r := o.(*v1.JobRun)
				if r.Spec.Job != v1.ObjectName(args[0]) || (phase != "" && !strings.EqualFold(string(r.Status.Phase), phase)) {
					continue
				}
				runs = append(runs, r)
			}
			sort.SliceStable(runs, func(i, j int) bool {
				return time.Time(runs[i].CreationTime).After(time.Time(runs[j].CreationTime))
			})
			out := make([]v1.Object, len(runs))
			for i, r := range runs {
				out[i] = r
			}
			return a.renderList(out, output == "json")
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&phase, "phase", "", "filter by run phase (Pending|Running|Succeeded|Failed|Cancelled)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

// jobLogsCmd reads a run's logs (ADR-0347 Decision 8): its worker's lines, raw output included, and by the run's trace
// the lines of the calls it made.
func (a *cli) jobLogsCmd() *cobra.Command {
	var ns, since, severity, output string
	var limit int
	cmd := &cobra.Command{
		Use:   "logs <run>",
		Short: "Print a run's logs: its worker's lines and those of the calls it made",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput("funcdctl job logs", output, "wide", "json"); err != nil {
				return err
			}
			wireSince, err := sinceParam("funcdctl job logs", since)
			if err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			lines, err := c.JobRunLogs(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]),
				sdk.LogsOptions{Since: wireSince, Severity: severity, Limit: limit})
			if err != nil {
				return err
			}
			return a.renderLogLines(lines, output)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&since, "since", "", "only logs since a duration ago (15m, 1h30m) or an RFC3339 time (2026-10-07T22:00:00Z)")
	cmd.Flags().StringVar(&severity, "severity", "", "minimum level: trace|debug|info|warn|error|fatal")
	cmd.Flags().IntVar(&limit, "limit", 0, "max records to return, most-recent (default 1000)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: wide (append source/inv/attrs inline) | json")
	return cmd
}

// jobDescribeCmd shows a run: its phase and reason, the revision and digest it ran, who started it, its times and its
// result. `-o json` prints the object.
func (a *cli) jobDescribeCmd() *cobra.Command {
	return a.runDescribeCmd("job", v1.KindJobRun, "Show a run: phase, reason, revision, digest, who started it, its times and its result",
		func(obj v1.Object) error { return a.renderJobRunDescribe(obj.(*v1.JobRun)) })
}

// renderJobRunDescribe prints a run's view. A message and a result can carry a handler's text, so they pass through
// termSafe.
func (a *cli) renderJobRunDescribe(run *v1.JobRun) error {
	lines := []string{"RUN " + string(run.Name) + "   job: " + string(run.Spec.Job) + "   phase: " + string(run.Status.Phase)}
	if c, ok := run.Status.Conditions.Get("Ready"); ok && (c.Reason != "" || c.Message != "") {
		lines = append(lines, "reason: "+termSafe(c.Reason)+"   message: "+termSafe(c.Message))
	}
	if run.Status.JobRevision != "" {
		lines = append(lines, "revision: "+string(run.Status.JobRevision)+"   digest: "+run.Status.ImageDigest)
	}
	if run.Status.StartedBy != "" {
		lines = append(lines, "started by: "+termSafe(run.Status.StartedBy))
	}
	if start := time.Time(run.Status.StartedAt); !start.IsZero() {
		times := "started: " + run.Status.StartedAt.String()
		if end := time.Time(run.Status.EndedAt); !end.IsZero() && !end.Before(start) {
			times += "   ended: " + run.Status.EndedAt.String() + "   duration: " + v1.Duration(end.Sub(start)).String()
		}
		lines = append(lines, times)
	} else if !run.Status.EndedAt.IsZero() {
		lines = append(lines, "ended: "+run.Status.EndedAt.String())
	}
	if len(run.Status.Result) > 0 {
		lines = append(lines, "result: "+termSafe(string(run.Status.Result)))
	}
	if run.Status.TraceID != "" {
		lines = append(lines, "trace: "+run.Status.TraceID)
	}
	lines = append(lines, "full logs: funcdctl job logs "+string(run.Name))
	return a.writef("%s\n", strings.Join(lines, "\n"))
}

// jobCancelCmd cancels a run: it sets spec.cancel, which the run reconciler acts on (ADR-0347 Decision 6).
func (a *cli) jobCancelCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "cancel <run>",
		Short: "Cancel a run (sets spec.cancel; a running run's worker is stopped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			name := v1.ObjectName(args[0])
			if _, err := applyRead(cmd.Context(), c, v1.KindJobRun, v1.NamespaceName(nsOrDefault(ns)), name,
				func(obj v1.Object) (bool, error) {
					run := obj.(*v1.JobRun)
					if run.Spec.Cancel {
						return false, nil
					}
					run.Spec.Cancel = true
					return true, nil
				}); err != nil {
				return err
			}
			return a.writef("cancel requested for %s\n", name)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}
