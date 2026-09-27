//go:build !dev

package main

import (
	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
)

// devCmd is the release-build stub of `funcdctl dev` (ADR-0125). The real command embeds the whole
// funcd platform (`funcd.InMemory()`) and is compiled ONLY under `-tags dev` (dev.go), keeping the
// release `funcdctl` thin (`pkg/sdk`+`api` only). Here it exists so the verb is discoverable but
// fails closed with a clear rebuild hint — the release binary never pulls in the platform.
func (a *cli) devCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dev [path]",
		Short: "Run a function locally from source (requires a -tags dev build)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, _ []string) error {
			return fault.Invalidf("funcdctl dev",
				"funcdctl dev requires a -tags dev build (the release client omits the embedded platform); "+
					"rebuild with `go build -tags dev ./cmd/funcdctl`")
		},
	}
}
