package main

import (
	"github.com/spf13/cobra"

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
	return &cobra.Command{
		Use:   "status",
		Short: "Show the hold: marker, restore report, counts, paused runs, dead letters, pending blob keys, orphan data",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printStatus(cmd.Context(), a, "funcdctl hold status", "encode", (*sdk.Client).HoldStatus)
		},
	}
}

func (a *cli) holdReleaseCmd() *cobra.Command {
	var advance []string
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Lift the hold: timers, blob sources, Sensors, runs, rollouts, backups and sweeps act again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if err := c.ReleaseHold(cmd.Context(), advance); err != nil {
				return err
			}
			return a.writef("released\n")
		},
	}
	cmd.Flags().StringArrayVar(&advance, "advance", nil,
		"a blob EventSource <namespace>/<name> whose listed objects are marked seen instead of fired (repeatable)")
	return cmd
}
