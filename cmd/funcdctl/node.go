package main

import (
	"fmt"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// nodeCmd groups the worker-node verbs (FEAT-0002).
func (a *cli) nodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage worker nodes",
	}
	cmd.AddCommand(a.nodeTokenCmd())
	return cmd
}

func (a *cli) nodeTokenCmd() *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   "token <name>",
		Short: "Mint a one-time join token for a declared WorkerNode",
		Long: "Mint a one-time join token for a declared WorkerNode, which funcd node join swaps for the node's " +
			"certificate. It prints the token alone, so a script can capture it; funcd keeps only its hash, and an " +
			"earlier unused token of the node stops working. An admin action.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			tok, err := c.MintJoinToken(cmd.Context(), v1.ObjectName(args[0]), ttl)
			if err != nil {
				return fmt.Errorf("funcdctl node token: %w", err)
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), tok.Token); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "expires at %s\n", tok.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"))
			return err
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "how long the token works, from 1s to 24h, such as 30m (default 1h)")
	return cmd
}
