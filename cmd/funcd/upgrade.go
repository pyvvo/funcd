package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/hold"
	"github.com/pyvvo/funcd/internal/safemode"
	"github.com/pyvvo/funcd/internal/upgrade"
)

// upgradeFlags are `funcd upgrade`'s flags (ADR-0207 Decision 1).
type upgradeFlags struct {
	config, unit string
	noSnapshot   bool
}

// newUpgradeCmd is `funcd upgrade <new-binary>` (ADR-0207 Decision 1), run with the installed binary.
func newUpgradeCmd(out io.Writer) *cobra.Command {
	var f upgradeFlags
	cmd := &cobra.Command{
		Use:   "upgrade <new-binary>",
		Short: "Install new-binary over this funcd: a pre-upgrade generation first, the old binary kept at <self>.previous",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			self, err := os.Executable()
			if err == nil {
				self, err = filepath.EvalSymlinks(self)
			}
			if err != nil {
				return fault.Wrapf(err, fault.Internal, "funcd upgrade", "resolve this binary")
			}
			return runUpgrade(cmd.Context(), out, f, args[0], self)
		},
	}
	cmd.Flags().StringVar(&f.config, "config", "", "path to funcdconfig.yaml (as the daemon locates it)")
	cmd.Flags().StringVar(&f.unit, "unit", "funcd.service",
		`the systemd unit to stop and start ("" when the operator stops and starts funcd)`)
	cmd.Flags().BoolVar(&f.noSnapshot, "no-snapshot", false,
		"upgrade without a pre-upgrade generation: no way back to this version")
	return cmd
}

// runUpgrade upgrades self, the installed binary, to newBinary.
func runUpgrade(ctx context.Context, out io.Writer, f upgradeFlags, newBinary, self string) error {
	path, err := config.Locate(f.config)
	if err != nil {
		return err
	}
	cfg, err := config.Load(path, config.Flags{})
	if err != nil {
		return err
	}
	if f.unit != "" {
		if err := requireLinuxRoot("funcd upgrade --unit"); err != nil {
			return err
		}
	}
	_, err = upgrade.Run(ctx, upgrade.Options{Config: cfg, NewBinary: newBinary, Self: self, Unit: f.unit,
		NoSnapshot: f.noSnapshot, Systemctl: systemctl, Out: out})
	return err
}

// newSafeModeCmd is `funcd safe-mode reset` (ADR-0207 Decision 5): offline, after a stopped start.
func newSafeModeCmd(out io.Writer) *cobra.Command {
	var configPath string
	reset := &cobra.Command{
		Use:   "reset",
		Short: "Clear the unclean-start count after safe mode stopped funcd; the next start is held",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			path, err := config.Locate(configPath)
			if err != nil {
				return err
			}
			cfg, err := config.Load(path, config.Flags{})
			if err != nil {
				return err
			}
			dataDir := cfg.Storage.DataDir
			if err := safemode.Reset(dataDir); err != nil {
				return err
			}
			msg := "safe mode reset: the unclean-start count is 0"
			if h, err := hold.Open(dataDir); err == nil && h.Held() {
				msg += "; the platform stays held: start funcd, read `funcdctl hold status`, then `funcdctl hold release`"
			}
			_, err = fmt.Fprintln(out, msg)
			return err
		},
	}
	reset.Flags().StringVar(&configPath, "config", "", "path to funcdconfig.yaml (as the daemon locates it)")
	cmd := &cobra.Command{
		Use:   "safe-mode",
		Short: "Leave safe mode (held after repeated unclean starts, stopped after twice as many)",
	}
	cmd.AddCommand(reset)
	return cmd
}
