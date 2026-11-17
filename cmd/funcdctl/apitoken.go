package main

import (
	"context"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// apiTokenCmd groups the API-token verbs (FEAT-0002 F248).
func (a *cli) apiTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apitoken",
		Short: "Manage the API tokens kept in the metastore",
	}
	cmd.AddCommand(a.mintCmd("mint <name>", "Mint the token of a declared APIToken",
		"Mint the token of a declared APIToken, for the role and the namespaces its spec names. It prints the token "+
			"alone, so a script can capture it; funcd keeps only its hash, and the token minted before stops working. "+
			"An admin action.",
		"how long the token works, from 1s to 8760h, such as 720h (default 720h)", "funcdctl apitoken mint",
		func(ctx context.Context, c *sdk.Client, name v1.ObjectName, ttl string) (sdk.JoinToken, error) {
			return c.MintAPIToken(ctx, name, ttl)
		}))
	return cmd
}
