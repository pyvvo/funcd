package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// nodeCmd groups the worker-node verbs (FEAT-0002).
func (a *cli) nodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage worker nodes",
	}
	cmd.AddCommand(a.mintCmd("token <name>", "Mint a one-time join token for a declared WorkerNode",
		"Mint a one-time join token for a declared WorkerNode, which funcd node join swaps for the node's certificate. "+
			"It prints the token alone, so a script can capture it; funcd keeps only its hash, and an earlier unused "+
			"token of the node stops working. An admin action.",
		"how long the token works, from 1s to 24h, such as 30m (default 1h)", "funcdctl node token",
		func(ctx context.Context, c *sdk.Client, name v1.ObjectName, ttl string) (sdk.JoinToken, error) {
			return c.MintJoinToken(ctx, name, ttl)
		}))
	return cmd
}

// mintCmd is a command that mints the token of a cluster-scoped object and prints the token alone, so a script can
// capture it, and its expiry on stderr.
func (a *cli) mintCmd(use, short, long, ttlHelp, what string,
	mint func(ctx context.Context, c *sdk.Client, name v1.ObjectName, ttl string) (sdk.JoinToken, error)) *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			tok, err := mint(cmd.Context(), c, v1.ObjectName(args[0]), ttl)
			if err != nil {
				return fmt.Errorf("%s: %w", what, err)
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), tok.Token); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "expires at %s\n", tok.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"))
			return err
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", ttlHelp)
	return cmd
}
