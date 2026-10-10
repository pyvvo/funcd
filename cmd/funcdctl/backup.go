package main

import (
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/verify"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// backupCmd groups the platform backup verbs (ADR-0205): the status the daemon reports, and the verification the
// operator runs off the box with the verify credential and an identity.
func (a *cli) backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Platform backup: status (the daemon's report) and verify (off the box)",
	}
	cmd.AddCommand(a.backupStatusCmd(), a.backupVerifyCmd())
	return cmd
}

func (a *cli) backupStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the platform backup status as YAML: restore points, rpoRisk, failures (admin only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			st, err := c.PlatformBackup(cmd.Context())
			if err != nil {
				return err
			}
			out, err := yaml.Marshal(st)
			if err != nil {
				return fault.Internalf("funcdctl backup status", "encode the status: %v", err)
			}
			return a.writef("%s", out)
		},
	}
}

func (a *cli) backupVerifyCmd() *cobra.Command {
	var target, credentials, escrowDir string
	var identities []string
	var generation uint64
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check a generation against its manifest, decrypt it, and pin it under gen/verified/",
		Long: "Check a generation against its manifest, decrypt it to its end and parse its records; with --escrow, also " +
			"find the keys it names. Then copy it under gen/verified/, manifest last. A generation already pinned is " +
			"checked again and nothing is written. Run it every (backup.objectives.rpo − backup.interval) / 2.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			ids, err := envelope.ReadIdentities(identities)
			if err != nil {
				return err
			}
			b, err := gocloud.OpenWith(ctx, target, gocloud.OpenOptions{CredentialsFile: credentials})
			if err != nil {
				return err
			}
			defer func() { _ = b.Close() }()
			res, err := verify.Verify(ctx, verify.Options{Bucket: b, Identities: ids, EscrowDir: escrowDir, Generation: generation})
			if err != nil {
				return err
			}
			keys := "keys not checked (no --escrow)"
			if res.KeysChecked {
				keys = "keys found in the escrow set"
			}
			pinned := "pinned under gen/verified/"
			if !res.Pinned {
				pinned = "already pinned under gen/verified/: nothing written"
			}
			return a.writef("generation %d verified: %d records, %s; %s\n", res.Generation, res.Records, keys, pinned)
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "backup.target: s3://… or file:///<absolute dir>")
	cmd.Flags().StringVar(&credentials, "credentials-file", "", "s3:// only: the verify credential's AWS shared credentials file")
	cmd.Flags().StringArrayVar(&identities, "identity", nil, "an age identity file (repeatable)")
	cmd.Flags().StringVar(&escrowDir, "escrow", "", "the escrow set's directory: find the keys the generation names")
	cmd.Flags().Uint64Var(&generation, "generation", 0, "the generation to verify; default the newest complete hourly, daily or weekly one")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}
