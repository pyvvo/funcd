package main

import (
	"context"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// holdCmd groups the platform hold verbs (ADR-0206 Decision 1): `funcdctl hold status` prints the hold's evidence,
// `funcdctl hold release` lifts it. Both are admin-only.
func (a *cli) holdCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hold",
		Short: "The platform hold a restore or safe mode leaves (status|release)",
	}
	cmd.AddCommand(a.holdStatusCmd(), a.holdReleaseCmd())
	return cmd
}

func (a *cli) holdStatusCmd() *cobra.Command {
	var node string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the hold: marker, restore report, counts, paused runs, dead letters, pending blob keys, orphan data, namespace markers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printStatus(cmd.Context(), a, "funcdctl hold status", "encode", func(c *sdk.Client, ctx context.Context) (hold.Evidence, error) {
				return c.HoldStatus(ctx, onNode(node)...)
			})
		},
	}
	nodeFlag(cmd, &node)
	return cmd
}

// nodeFlag adds --node, the worker node a command is about.
func nodeFlag(cmd *cobra.Command, node *string) {
	cmd.Flags().StringVar(node, "node", "", "a worker node, instead of the funcd at --server")
}

func onNode(node string) []sdk.NodeOption {
	if node == "" {
		return nil
	}
	return []sdk.NodeOption{sdk.OnNode(v1.ObjectName(node))}
}

func (a *cli) holdReleaseCmd() *cobra.Command {
	var advance, namespaces []string
	var node string
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Lift the hold: timers, blob sources, Sensors, runs, rollouts, backups and sweeps act again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if len(namespaces) > 0 {
				err = c.ReleaseNamespaceHold(cmd.Context(), namespaces)
			} else {
				err = c.ReleaseHold(cmd.Context(), advance, onNode(node)...)
			}
			if err != nil {
				return err
			}
			return a.writef("released\n")
		},
	}
	cmd.Flags().StringArrayVar(&advance, "advance", nil,
		"a blob EventSource <namespace>/<name> whose listed objects are marked seen instead of fired (repeatable)")
	cmd.Flags().StringArrayVar(&namespaces, "namespace", nil,
		"a namespace whose hold a restore left (restore run's data, an ended in-place Restore's load) is released instead of the platform's hold (repeatable)")
	nodeFlag(cmd, &node)
	cmd.MarkFlagsMutuallyExclusive("advance", "namespace")
	cmd.MarkFlagsMutuallyExclusive("node", "namespace")
	return cmd
}
