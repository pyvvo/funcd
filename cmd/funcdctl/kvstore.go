package main

import (
	"fmt"

	"github.com/spf13/cobra"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// kvstoreCmd groups the KVStore verbs (ADR-0178): handover.
func (a *cli) kvstoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kvstore",
		Short: "Manage KV stores (handover)",
	}
	cmd.AddCommand(a.kvstoreHandoverCmd())
	return cmd
}

// kvstoreHandoverCmd makes a Workflow the owner of a kept store, such as one a deleted namesake made
// (ADR-0178 Decision 6). The store's spec and data are untouched.
func (a *cli) kvstoreHandoverCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "handover <store> <workflow>",
		Short: "Make a workflow the owner of a kept KV store (needs KVStore update and delete)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if err := c.HandoverKVStore(cmd.Context(), v1.NamespaceName(nsOrDefault(ns)), v1.ObjectName(args[0]), v1.ObjectName(args[1])); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "kvstore/%s handed over to workflow/%s\n", args[0], args[1])
			return err
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace (default: default)")
	return cmd
}
