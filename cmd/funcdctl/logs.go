package main

import (
	"time"

	"github.com/spf13/cobra"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// logsCmd prints a function's logs from the control-plane logs route (ADR-0084), tenant-scoped to the
// caller's identity. Renders a subset (time · severity · replica · body); the JSON DTO carries more.
func (a *cli) logsCmd() *cobra.Command {
	var ns, since, severity string
	var limit int
	cmd := &cobra.Command{
		Use:   "logs <function>",
		Short: "Print a function's logs (tenant-scoped to your identity)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			lines, err := c.Logs(cmd.Context(), v1.NamespaceName(ns), v1.ObjectName(args[0]), sdk.LogsOptions{
				Since: since, Severity: severity, Limit: limit,
			})
			if err != nil {
				return err
			}
			for _, l := range lines {
				if werr := a.writef("%s [%s] %s %s\n", l.Time.UTC().Format(time.RFC3339), l.Severity, l.Replica, l.Body); werr != nil {
					return werr
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace")
	cmd.Flags().StringVar(&since, "since", "", "only logs since (RFC3339 time or a duration like 15m)")
	cmd.Flags().StringVar(&severity, "severity", "", "minimum level: trace|debug|info|warn|error|fatal")
	cmd.Flags().IntVar(&limit, "limit", 0, "max records to return, most-recent (default 1000)")
	return cmd
}
