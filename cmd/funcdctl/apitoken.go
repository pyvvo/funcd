package main

import (
	"fmt"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// apiTokenCmd groups the API-token verbs (FEAT-0002 F248).
func (a *cli) apiTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apitoken",
		Short: "Manage the API tokens kept in the metastore",
	}
	cmd.AddCommand(a.apiTokenMintCmd())
	return cmd
}

func (a *cli) apiTokenMintCmd() *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   "mint <name>",
		Short: "Mint the token of a declared APIToken",
		Long: "Mint the token of a declared APIToken, for the role and the namespaces its spec names. It prints the " +
			"token alone, so a script can capture it; funcd keeps only its hash, and the token minted before stops " +
			"working. An admin action.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			tok, err := c.MintAPIToken(cmd.Context(), v1.ObjectName(args[0]), ttl)
			if err != nil {
				return fmt.Errorf("funcdctl apitoken mint: %w", err)
			}
			if _, err := fmt.Fprintln(cmd.OutOrStdout(), tok.Token); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "expires at %s\n", tok.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"))
			return err
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "how long the token works, from 1s to 8760h, such as 720h (default 720h)")
	return cmd
}
