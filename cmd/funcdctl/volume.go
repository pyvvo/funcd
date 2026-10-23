package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// volumeCmd is `funcdctl volume delete <ns>/<function>/<name>` (ADR-0262 Decision 8): it removes a retained Function
// volume, which no other command frees.
func (a *cli) volumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Function volume operations (delete a retained volume)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "delete <namespace>/<function>/<volume>",
		Short: "Delete a retained Function volume and free its reserved space",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parts := strings.Split(args[0], "/")
			if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
				return fault.Invalidf("funcdctl.volume", "%q: want <namespace>/<function>/<volume>", args[0])
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if err := c.DeleteVolume(cmd.Context(), v1.NamespaceName(parts[0]), v1.ObjectName(parts[1]), v1.ObjectName(parts[2])); err != nil {
				return err
			}
			return a.writef("deleted volume %s\n", args[0])
		},
	})
	return cmd
}
