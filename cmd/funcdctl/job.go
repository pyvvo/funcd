package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// jobCmd groups the run verbs of a Job (ADR-0347 Decision 10), as workflowCmd does a Workflow's: run, runs, logs,
// describe and cancel, over the JobRun surface; cancel sets spec.cancel. approve and reject set a person's decision on
// a run's preview (ADR-0348 Decision 7).
func (a *cli) jobCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Run Jobs and read their runs (run|runs|logs|describe|cancel|approve|reject)",
	}
	cmd.AddCommand(a.jobRunCmd(), a.jobRunsCmd(), a.jobLogsCmd(), a.jobDescribeCmd(), a.jobCancelCmd(),
		a.jobApproveCmd(), a.jobRejectCmd())
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
	cmd.Flags().StringVar(&phase, "phase", "", "filter by run phase (Pending|Previewing|AwaitingApproval|Running|Succeeded|Failed|Cancelled|Rejected)")
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

// jobDescribeCmd shows a run: its phase and reason, the revision and digest it ran, who started it, its preview and who
// approved or rejected it, its times and its result. `-o json` prints the object.
func (a *cli) jobDescribeCmd() *cobra.Command {
	return a.runDescribeCmd("job", v1.KindJobRun, "Show a run: phase, reason, revision, digest, who started it, its preview, who approved or rejected it, its times and its result",
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
	if p := run.Status.Preview; p != nil {
		lines = append(lines, "preview: "+p.Digest+"   previewed: "+p.PreviewedAt.String(), "  "+termSafe(string(p.Document)))
	}
	if run.Status.ApprovedBy != "" || !run.Status.ApprovedAt.IsZero() {
		lines = append(lines, "approved by: "+termSafe(run.Status.ApprovedBy)+"   approved: "+run.Status.ApprovedAt.String()+
			"   digest: "+run.Spec.Approve)
	}
	if run.Status.RejectedBy != "" {
		lines = append(lines, "rejected by: "+termSafe(run.Status.RejectedBy))
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

// jobApproveCmd approves a run's preview (ADR-0348 Decision 7): it prints the preview's document and digest and, on a
// terminal, asks to confirm, then sets spec.approve to that digest; --digest approves the digest it names with no
// question, for scripts. The server takes the approval only while it names the run's current preview.
func (a *cli) jobApproveCmd() *cobra.Command {
	var ns, digest string
	cmd := &cobra.Command{
		Use:   "approve <run>",
		Short: "Approve a run's preview: show its document and digest and ask to confirm, or approve --digest with no question",
		Long: "Approve a run's preview, so the run runs exactly what it previewed.\n\n" +
			"Without --digest, the command prints the preview's document and digest and, on a terminal, asks to confirm. " +
			"With --digest, it approves that digest with no question, for scripts. funcd refuses an approval that names " +
			"another preview than the run's current one with 409, and one from a caller who may not update the run's Job with 403.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			const op = "funcdctl job approve"
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			name, space := v1.ObjectName(args[0]), v1.NamespaceName(nsOrDefault(ns))
			if digest == "" {
				if digest, err = a.confirmPreview(cmd, c, space, name); err != nil || digest == "" {
					return err
				}
			}
			if _, err := applyRead(cmd.Context(), c, v1.KindJobRun, space, name, func(obj v1.Object) (bool, error) {
				run := obj.(*v1.JobRun)
				if run.Spec.Approve == digest && !run.Status.ApprovedAt.IsZero() {
					return false, nil
				}
				run.Spec.Approve = digest
				return true, nil
			}); err != nil {
				return fault.Wrapf(err, fault.KindOf(err), op, "approve %s", name)
			}
			return a.writef("approved %s (%s)\n", name, digest)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&digest, "digest", "", "approve this preview digest, sha256:<hex>, with no question (default: show the preview and ask)")
	return cmd
}

// confirmPreview prints run's preview and asks a person to confirm it: the digest to approve once they answer yes, ""
// when they answer anything else. With no terminal to ask on, it fails and names --digest.
func (a *cli) confirmPreview(cmd *cobra.Command, c *sdk.Client, ns v1.NamespaceName, name v1.ObjectName) (string, error) {
	const op = "funcdctl job approve"
	obj, err := c.Get(cmd.Context(), v1.KindJobRun, ns, name)
	if err != nil {
		return "", err
	}
	run := obj.(*v1.JobRun)
	if run.Status.Phase != v1.JobRunAwaitingApproval || run.Status.Preview == nil {
		return "", fault.Conflictf(op, "run %s is %s, and only a run AwaitingApproval shows a preview to approve", name,
			cmp.Or(run.Status.Phase, v1.JobRunPending))
	}
	p := run.Status.Preview
	doc, err := json.MarshalIndent(p.Document, "", "  ")
	if err != nil {
		doc = p.Document
	}
	if err := a.writef("preview of %s, made %s:\n%s\ndigest: %s\n", name, p.PreviewedAt.String(), termSafeLines(string(doc)), p.Digest); err != nil {
		return "", err
	}
	in, ok := answerSource(cmd)
	if !ok {
		return "", fault.Invalidf(op, "no terminal to confirm on: pass --digest %s to approve this preview", p.Digest)
	}
	if err := a.writef("approve this preview? [y/N] "); err != nil {
		return "", err
	}
	answer, _ := bufio.NewReader(in).ReadString('\n')
	if !strings.EqualFold(strings.TrimSpace(answer), "y") && !strings.EqualFold(strings.TrimSpace(answer), "yes") {
		return "", a.writef("not approved\n")
	}
	return p.Digest, nil
}

// answerSource is where approve reads a person's answer: stdin when it is a terminal, or a reader a test set; none for
// a pipe, a file or /dev/null.
func answerSource(cmd *cobra.Command) (io.Reader, bool) {
	in := cmd.InOrStdin()
	f, isFile := in.(*os.File)
	if !isFile {
		return in, true
	}
	if !isatty.IsTerminal(f.Fd()) && !isatty.IsCygwinTerminal(f.Fd()) {
		return nil, false
	}
	return f, true
}

// termSafeLines is termSafe for each line of s, so a document keeps its lines.
func termSafeLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = termSafe(l)
	}
	return strings.Join(lines, "\n")
}

// jobRejectCmd rejects a run that has not started running (ADR-0348 Decision 7): it sets spec.reject and
// spec.rejectReason, and the run ends Rejected without running its handler.
func (a *cli) jobRejectCmd() *cobra.Command {
	var ns, reason string
	cmd := &cobra.Command{
		Use:   "reject <run>",
		Short: "Reject a run that has not started running, so it ends Rejected and its handler never runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			const op = "funcdctl job reject"
			if len(reason) > v1.MaxJobRunRejectReason {
				return fault.Invalidf(op, "--reason is %d bytes, more than %d", len(reason), v1.MaxJobRunRejectReason)
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			name := v1.ObjectName(args[0])
			if _, err := applyRead(cmd.Context(), c, v1.KindJobRun, v1.NamespaceName(nsOrDefault(ns)), name,
				func(obj v1.Object) (bool, error) {
					run := obj.(*v1.JobRun)
					if run.Spec.Reject {
						return false, nil
					}
					run.Spec.Reject, run.Spec.RejectReason = true, reason
					return true, nil
				}); err != nil {
				return err
			}
			return a.writef("rejected %s\n", name)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVar(&reason, "reason", "", "why the run is rejected, at most 512 bytes: the message of its Ready condition")
	return cmd
}
