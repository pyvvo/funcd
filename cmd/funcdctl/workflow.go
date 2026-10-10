package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// workflowCmd groups the workflow-run verbs (ADR-0094): run|runs|pause|resume|cancel|describe|logs|replay.
// run/runs/pause/resume/cancel/describe are sugar over the WorkflowRun CRUD surface (cancel patches
// spec.cancel); logs reads a whole run's logs by trace-id (ADR-0106); replay re-runs a finished run from a
// chosen step (ADR-0107).
func (a *cli) workflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage workflow runs (run|runs|pause|resume|cancel|describe|logs|replay)",
	}
	cmd.AddCommand(
		a.workflowRunCmd(),
		a.workflowRunsCmd(),
		a.workflowPauseCmd("pause", true),
		a.workflowPauseCmd("resume", false),
		a.workflowCancelCmd(),
		a.workflowDescribeCmd(),
		a.workflowLogsCmd(),
		a.workflowReplayCmd(),
	)
	return cmd
}

// workflowReplayCmd re-runs a finished run from a chosen step (ADR-0107): it reads the source run for its
// workflow, then creates a NEW WorkflowRun carrying spec.replay = {run, from, allowDrift}. The engine
// seeds it from the source's checkpoint (reusing upstream outputs) and re-runs `from` + its descendants.
func (a *cli) workflowReplayCmd() *cobra.Command {
	var ns, from, name string
	var allowDrift bool
	cmd := &cobra.Command{
		Use:   "replay <source-run> --from <step>",
		Short: "Re-run a finished run from a chosen step (reuses upstream outputs; ADR-0107)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" {
				return fault.Invalidf("funcdctl workflow replay", "--from <step> is required")
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			namespace := v1.NamespaceName(nsOrDefault(ns))
			srcObj, err := c.Get(cmd.Context(), v1.KindWorkflowRun, namespace, v1.ObjectName(args[0]))
			if err != nil {
				return err
			}
			src := srcObj.(*v1.WorkflowRun)
			newName := name
			if newName == "" {
				newName = args[0] + "-r-" + randHex4()
			}
			replay := &v1.WorkflowRun{
				TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
				ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(newName), Namespace: namespace, ResourceGroup: src.ResourceGroup},
				Spec: v1.WorkflowRunSpec{
					Workflow: src.Spec.Workflow,
					Replay:   &v1.ReplaySeed{Run: v1.ObjectName(args[0]), From: v1.ObjectName(from), AllowDrift: allowDrift},
				},
			}
			if verr := replay.Validate(); verr != nil {
				return fault.Wrapf(verr, fault.KindOf(verr), "funcdctl workflow replay", "invalid replay")
			}
			if _, err := c.Create(cmd.Context(), replay); err != nil {
				return err
			}
			return a.writef("replay %s created (of %s from %s)\n", newName, args[0], from)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&from, "from", "", "the step to re-run from (required); it and its descendants re-execute")
	cmd.Flags().StringVar(&name, "name", "", "the new run's name (default: <source>-r-<hex>)")
	cmd.Flags().BoolVar(&allowDrift, "allow-drift", false, "re-run even if a step's artifact digest moved since the source run")
	return cmd
}

// workflowLogsCmd reads a whole run's logs from the run-scoped control-plane route (ADR-0106): the server
// resolves the run's status.traceId and returns every step function's lines (plus sub-workflow child steps
// — one composition = one trace). --step narrows to one step's function; --since/--severity/--limit filter.
func (a *cli) workflowLogsCmd() *cobra.Command {
	var ns, since, severity, step, output string
	var limit int
	cmd := &cobra.Command{
		Use:   "logs <run>",
		Short: "Print a whole workflow run's logs (the full error/logs by run, tenant-scoped)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput("funcdctl workflow logs", output, "wide", "json"); err != nil {
				return err
			}
			wireSince, err := sinceParam("funcdctl workflow logs", since)
			if err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			lines, err := c.RunLogs(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]), sdk.LogsOptions{
				Since: wireSince, Severity: severity, Limit: limit, Step: step,
			})
			if err != nil {
				return err
			}
			return a.renderLogLines(lines, output)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&since, "since", "", "only logs since a duration ago (15m, 1h30m) or an RFC3339 time (2026-10-07T22:00:00Z)")
	cmd.Flags().StringVar(&severity, "severity", "", "minimum level: trace|debug|info|warn|error|fatal")
	cmd.Flags().StringVar(&step, "step", "", "narrow to one step's function (the --step drill-down)")
	cmd.Flags().IntVar(&limit, "limit", 0, "max records to return, most-recent (default 1000)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: wide (append source/inv/attrs inline) | json")
	return cmd
}

// workflowRunCmd creates a WorkflowRun that starts <workflow> (a run of the named Workflow).
func (a *cli) workflowRunCmd() *cobra.Command {
	var ns, rg, input string
	cmd := &cobra.Command{
		Use:   "run <workflow> [run-name]",
		Short: "Start a workflow run (creates a WorkflowRun; omit run-name for a server-generated <workflow>-<id>)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readInput(input)
			if err != nil {
				return err
			}
			meta := v1.ObjectMeta{Namespace: v1.NamespaceName(nsOrDefault(ns)), ResourceGroup: v1.ResourceGroupName(rg)}
			if len(args) == 2 {
				meta.Name = v1.ObjectName(args[1])
			} else {
				meta.GenerateName = args[0] + "-" // server assigns <workflow>-<id> (ObjectMeta.GenerateName)
			}
			run := &v1.WorkflowRun{
				TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
				ObjectMeta: meta,
				Spec:       v1.WorkflowRunSpec{Workflow: v1.ObjectName(args[0]), Input: raw},
			}
			// Local pre-check only when we already have a name; a generateName run is validated server-side
			// after the name is assigned (ObjectMeta.Validate requires a non-empty Name).
			if meta.Name != "" {
				if verr := run.Validate(); verr != nil {
					return fault.Wrapf(verr, fault.KindOf(verr), "funcdctl workflow run", "invalid run")
				}
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			created, err := c.Create(cmd.Context(), run) // a run is started once: a taken name is a Conflict (ADR-0094)
			if err != nil {
				return err
			}
			return a.writef("started %s/%s\n", created.GroupVersionKind().Kind, created.GetName())
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
			if err := checkOutput("funcdctl workflow runs", output, "json"); err != nil {
				return err
			}
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
			name := v1.ObjectName(args[0])
			if _, err := applyRead(cmd.Context(), c, v1.KindWorkflowRun, v1.NamespaceName(nsOrDefault(ns)), name,
				func(obj v1.Object) (bool, error) {
					obj.(*v1.WorkflowRun).Spec.Paused = paused
					return true, nil
				}); err != nil {
				return err
			}
			return a.writef("%sd %s\n", verb, name)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

// devApplyAttempts bounds the re-apply of one resource that loses its update with a Conflict (funcdctl dev and
// applyRead).
const devApplyAttempts = 5

// applyRead reads the object, lets change edit it and applies it. The apply is conditional on the version read
// (ADR-0210), so a controller's status write in between answers fault.Conflict: it reads again, at most
// devApplyAttempts times. A change that returns false applies nothing; applyRead reports whether it applied.
func applyRead(ctx context.Context, c *sdk.Client, kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName,
	change func(v1.Object) (bool, error),
) (bool, error) {
	var err error
	for range devApplyAttempts {
		obj, gerr := c.Get(ctx, kind, ns, name)
		if gerr != nil {
			return false, gerr
		}
		apply, cerr := change(obj)
		if cerr != nil || !apply {
			return false, cerr
		}
		if _, err = c.Apply(ctx, obj); fault.KindOf(err) != fault.Conflict {
			return err == nil, err
		}
	}
	return false, err
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
			name := v1.ObjectName(args[0])
			if _, err := applyRead(cmd.Context(), c, v1.KindWorkflowRun, v1.NamespaceName(nsOrDefault(ns)), name,
				func(obj v1.Object) (bool, error) {
					obj.(*v1.WorkflowRun).Spec.Cancel = true
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

// workflowDescribeCmd reads a run's engine state via the API server (WorkflowRun.status mirror) and
// renders a troubleshooting view: per step its phase · attempts · duration · error, the run trace-id,
// and a pointer to the full logs (ADR-0100). `-o json` returns the raw object (the prior default).
func (a *cli) workflowDescribeCmd() *cobra.Command {
	var ns, output string
	cmd := &cobra.Command{
		Use:   "describe <run>",
		Short: "Show a workflow run's per-step troubleshooting state (phase, attempts, duration, error)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkOutput("funcdctl workflow describe", output, "json"); err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			obj, err := c.Get(cmd.Context(), v1.KindWorkflowRun, v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]))
			if err != nil {
				return err
			}
			if output == "json" {
				return a.renderObject(obj, true)
			}
			return a.renderRunDescribe(obj.(*v1.WorkflowRun))
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: rendered (default) or json")
	return cmd
}

// renderRunDescribe prints the ADR-0100 troubleshooting view of a run: the run phase + trace-id, a
// readable per-step line (phase · attempts · duration · error), and the full-logs pointer. A step error and
// a condition can carry a function's answer, so they pass through termSafe.
func (a *cli) renderRunDescribe(run *v1.WorkflowRun) error {
	if err := a.writef("RUN %s   phase: %s\n", run.GetName(), string(run.Status.Phase)); err != nil {
		return err
	}
	for _, c := range run.Status.Conditions { // a condition that is not True says why the run is not progressing
		if c.Status == v1.ConditionTrue {
			continue
		}
		line := "condition: " + string(c.Type) + "=" + string(c.Status)
		if c.Reason != "" {
			line += "   reason: " + termSafe(c.Reason)
		}
		if c.Message != "" {
			line += "   message: " + termSafe(c.Message)
		}
		if err := a.writef("%s\n", line); err != nil {
			return err
		}
	}
	for _, s := range run.Status.Steps {
		line := "  " + string(s.Name) + "   phase: " + string(s.Phase)
		if s.Attempts > 0 {
			line += "   attempts: " + strconv.Itoa(s.Attempts)
		}
		if start, end := time.Time(s.StartedAt), time.Time(s.EndedAt); !start.IsZero() && !end.IsZero() && !end.Before(start) {
			line += "   duration: " + v1.Duration(end.Sub(start)).String()
		}
		if s.Error != "" {
			line += "   error: " + termSafe(s.Error)
		}
		if err := a.writef("%s\n", line); err != nil {
			return err
		}
	}
	if r := run.Spec.Replay; r != nil { // ADR-0107 provenance
		if err := a.writef("replay of: %s (from %s)\n", r.Run, r.From); err != nil {
			return err
		}
	}
	if run.Status.TraceID != "" {
		if err := a.writef("trace: %s\n", run.Status.TraceID); err != nil {
			return err
		}
	}
	// A plain informational pointer (invokes nothing) to the run-scoped log read (ADR-0106).
	return a.writef("full logs: funcdctl workflow logs %s\n", run.GetName())
}

// randHex4 returns 4 random hex chars for a default replay-run suffix; on the near-impossible crypto/rand
// error it returns a fixed token (the caller can still pass --name).
func randHex4() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b[:])
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
