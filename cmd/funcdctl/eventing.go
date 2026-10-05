package main

import (
	"encoding/json"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/eventing/deadletter"
)

// renderDeadLetters pretty-prints DLQ records as JSON (the --output json path; a DeadLetter is not a
// v1.Object, so it can't use renderList/renderObject).
func (a *cli) renderDeadLetters(items []deadletter.DeadLetter) error {
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fault.Internalf("funcdctl", "marshal json: %v", err)
	}
	return a.writef("%s\n", string(b))
}

// renderDeadLetter pretty-prints a single DLQ record as JSON.
func (a *cli) renderDeadLetter(dl deadletter.DeadLetter) error {
	b, err := json.MarshalIndent(dl, "", "  ")
	if err != nil {
		return fault.Internalf("funcdctl", "marshal json: %v", err)
	}
	return a.writef("%s\n", string(b))
}

// eventingCmd groups the eventing operator verbs (ADR-0118). Today it exposes the dead-letter queue:
// `funcdctl eventing dlq {list|describe|replay|discard}` over the control-plane DLQ read + replay/discard
// surface — inspect a terminally-undeliverable Sensor action, re-inject it once the target is fixed, or drop it.
func (a *cli) eventingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eventing",
		Short: "Eventing operations (dlq: inspect/replay/discard dead-lettered Sensor actions)",
	}
	cmd.AddCommand(a.dlqCmd())
	return cmd
}

// dlqCmd groups the dead-letter-queue verbs (ADR-0118, F85).
func (a *cli) dlqCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dlq",
		Short: "Manage the eventing dead-letter queue (list|describe|replay|discard)",
	}
	cmd.AddCommand(a.dlqListCmd(), a.dlqDescribeCmd(), a.dlqReplayCmd(), a.dlqDiscardCmd())
	return cmd
}

// dlqListCmd lists a namespace's dead letters, newest first, one row each. A reason carries the start of
// the failed function's answer, so every field passes through termSafe.
func (a *cli) dlqListCmd() *cobra.Command {
	var ns, output string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List dead-lettered Sensor actions (newest first)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkOutput("funcdctl eventing dlq list", output, "json"); err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			items, err := c.DeadLetters(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)))
			if err != nil {
				return err
			}
			if output == "json" {
				return a.renderDeadLetters(items)
			}
			if len(items) == 0 {
				return a.writef("no dead letters\n")
			}
			for _, dl := range items {
				if werr := a.writef("%s\t%s\t%s/%s\taction=%s\tattempts=%s\treason=%s\n",
					termSafe(dl.ID), termSafe(string(dl.Sensor)), termSafe(string(dl.Source)), termSafe(string(dl.Event)),
					termSafe(dl.Action), strconv.Itoa(dl.Attempts), termSafe(dl.Reason)); werr != nil {
					return werr
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

// dlqDescribeCmd shows one dead letter in full (including its parked CloudEvent payload).
func (a *cli) dlqDescribeCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "describe <id>",
		Short: "Show a dead letter in full (provenance, attempts, reason, the parked CloudEvent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			dl, err := c.DeadLetter(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), args[0])
			if err != nil {
				return err
			}
			return a.renderDeadLetter(dl)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

// dlqReplayCmd re-injects a dead letter through the LIVE Sensor action path (one synchronous attempt).
func (a *cli) dlqReplayCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "replay <id>",
		Short: "Replay a dead letter against the live Sensor spec (removed on success; re-parked if the delivery fails)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if err := c.ReplayDeadLetter(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), args[0]); err != nil {
				return fault.Wrapf(err, fault.KindOf(err), "funcdctl eventing dlq replay", "replay %s failed", args[0])
			}
			return a.writef("replayed %s (delivered; entry removed)\n", args[0])
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}

// dlqDiscardCmd deletes a dead letter without replaying it.
func (a *cli) dlqDiscardCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "discard <id>",
		Short: "Discard a dead letter (delete without replaying)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if err := c.DiscardDeadLetter(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), args[0]); err != nil {
				return err
			}
			return a.writef("discarded %s\n", args[0])
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}
