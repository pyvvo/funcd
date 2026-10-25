package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/backup/plan"
	"github.com/pyvvo/funcd/internal/backup/verify"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/units"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// backupCmd groups the platform backup verbs (ADR-0205): the status the daemon reports, the verification the
// operator runs off the box with the verify credential and an identity, and the plan that turns recovery objectives
// into settings (ADR-0223).
func (a *cli) backupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Platform backup: status (the daemon's report), verify (off the box) and plan (objectives into settings)",
	}
	cmd.AddCommand(a.backupStatusCmd(), a.backupVerifyCmd(), a.backupPlanCmd())
	return cmd
}

func (a *cli) backupStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print the platform backup status as YAML: restore points, rpoRisk, failures (admin only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printStatus(cmd.Context(), a, "funcdctl backup status", "encode the status", (*sdk.Client).PlatformBackup)
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
			targetURL, err := backup.TargetURL(backup.Config{Target: target, CredentialsFile: credentials})
			if err != nil {
				return err
			}
			b, err := gocloud.OpenWith(ctx, targetURL, gocloud.OpenOptions{CredentialsFile: credentials})
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

const planOp = "funcdctl backup plan"

// planFlags are backup plan's flags (ADR-0223 Decision 1, ADR-0286).
type planFlags struct {
	rpo, keep, rto, size, speed, config, file string
}

// backupPlanCmd prints the settings recovery objectives give, offline (ADR-0223, ADR-0286): it builds no SDK client
// and writes no file.
func (a *cli) backupPlanCmd() *cobra.Command {
	var f planFlags
	cmd := &cobra.Command{
		Use:   "plan <platform|kv|blob|lake|schedule>",
		Short: "Print the backup settings recovery objectives give, as an example the daemon accepts (offline)",
		Long: "Print the backup settings recovery objectives give, as an example: --rpo (the most data a loss may take) " +
			"and --keep (how far back a restore point must reach) give the platform, kv or blob keys of " +
			"funcdconfig.yaml, or the schedule and ttl of a BackupSchedule (-f); lake says why a Lake needs no key. " +
			"The example passes the daemon's own loader merged into the located file, then is printed; no file is " +
			"written. --rto with --size and --speed checks the download time, a lower bound: only a restore drill " +
			"proves an RTO. Exit 1: refused, nothing printed; 3: the RTO is below the download time.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := plan.Component(args[0])
			o, err := f.objectives(c, cmd.Flags().Changed)
			if err != nil {
				return err
			}
			if c == plan.Schedule {
				return a.planSchedule(cmd.ErrOrStderr(), o, f)
			}
			return a.planConfig(cmd.ErrOrStderr(), c, o, f)
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.rpo, "rpo", "", "the most data a loss may take, a duration (1h)")
	fl.StringVar(&f.keep, "keep", "", "how far back a restore point must reach, a duration (720h)")
	fl.StringVar(&f.rto, "rto", "", "the target recovery time, a duration; needs --size and --speed")
	fl.StringVar(&f.size, "size", "", "the bytes a restore downloads: an integer with an optional Ki, Mi, Gi or Ti suffix")
	fl.StringVar(&f.speed, "speed", "", "the link's bytes per second, in the grammar of --size")
	fl.StringVar(&f.config, "config", "", "platform, kv, blob, lake: path to funcdconfig.yaml (else $FUNCD_CONFIG, "+
		"./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml)")
	fl.StringVarP(&f.file, "file", "f", "", "schedule: a manifest holding one BackupSchedule; - for stdin")
	return cmd
}

// objectives applies Decision 1's flag rules and parses the given values.
func (f planFlags) objectives(c plan.Component, changed func(string) bool) (plan.Objectives, error) {
	var o plan.Objectives
	switch c {
	case plan.Platform, plan.KV, plan.Blob, plan.Lake:
		if changed("file") {
			return o, fault.Invalidf(planOp, "-f applies to schedule; %s reads --config", c)
		}
	case plan.Schedule:
		switch {
		case changed("config"):
			return o, fault.Invalidf(planOp, "--config applies to platform, kv, blob and lake; schedule reads -f")
		case f.file == "":
			return o, fault.Invalidf(planOp, "schedule needs -f <manifest>")
		}
	default:
		return o, fault.Invalidf(planOp, "component %q: want platform, kv, blob, lake or schedule", c)
	}
	if c == plan.Lake {
		if changed("keep") {
			return o, fault.Invalidf(planOp, "--keep does not apply to lake: a Lake keeps no backups of its own; an App's "+
				"are a BackupSchedule's (scope.lakes)")
		}
		if !changed("rpo") {
			return o, fault.Invalidf(planOp, "lake needs --rpo")
		}
	}
	if !changed("rpo") && !changed("keep") {
		return o, fault.Invalidf(planOp, "give --rpo, --keep or both")
	}
	if changed("rto") != changed("size") || changed("rto") != changed("speed") {
		return o, fault.Invalidf(planOp, "--rto, --size and --speed go together: give all three or none")
	}
	for _, d := range []struct {
		name, value string
		dst         *time.Duration
	}{{"rpo", f.rpo, &o.RPO}, {"keep", f.keep, &o.Keep}, {"rto", f.rto, &o.RTO}} {
		if !changed(d.name) {
			continue
		}
		v, err := v1.ParseDuration(d.value)
		if err != nil {
			return o, fault.Wrapf(err, fault.Invalid, planOp, "--%s", d.name)
		}
		if v <= 0 {
			return o, fault.Invalidf(planOp, "--%s %s: want a duration above zero", d.name, v)
		}
		*d.dst = time.Duration(v)
	}
	for _, b := range []struct {
		name, value string
		dst         *int64
	}{{"size", f.size, &o.Size}, {"speed", f.speed, &o.Speed}} {
		if !changed(b.name) {
			continue
		}
		n, ok := units.ParseBytes(b.value)
		if !ok || n <= 0 {
			return o, fault.Invalidf(planOp, "--%s %q: want an integer above zero with an optional Ki, Mi, Gi or Ti "+
				"suffix", b.name, b.value)
		}
		*b.dst = n
	}
	return o, nil
}

// planConfig plans the platform, kv, blob or lake keys of the located config file (Decisions 2-4, ADR-0286): the
// keys are checked merged into the file's bytes in memory, then printed as an example.
func (a *cli) planConfig(stderr io.Writer, c plan.Component, o plan.Objectives, f planFlags) error {
	path, err := config.Locate(f.config)
	if err != nil {
		return err
	}
	var data []byte
	if path != "" {
		if data, err = os.ReadFile(path); err != nil { //nolint:gosec // the operator's config file
			return fault.Invalidf(planOp, "read config %q: %v", path, err)
		}
	}
	cur, err := config.Parse(path, data, config.Flags{})
	if err != nil {
		return err
	}
	settings, err := plan.Derive(c, o, cur)
	if err != nil {
		return err
	}
	merged, err := plan.Apply(data, "", settings)
	if err != nil {
		return err
	}
	warns, err := plan.CheckConfig(path, merged)
	if err != nil {
		return err
	}
	notes := make([]string, 0, len(warns))
	for _, w := range warns {
		notes = append(notes, "warning: "+w.Key+": "+w.Message)
	}
	printed, err := plan.Apply(nil, "", settings)
	if err != nil {
		return err
	}
	switch {
	case c == plan.Platform && o.RPO > 0:
		printed = append(printed, verifyJob(o.RPO)...)
	case c == plan.Lake:
		printed = append(printed, lakeNote(o.RPO, cur.Lake.SaveInterval)...)
	}
	return a.planFinish(stderr, o, notes, printed)
}

// verifyJob is the example's comment naming ADR-0205 Decision 6's verify job, run every (rpo − interval) ÷ 2 for
// Derive's interval of rpo ÷ 2 (ADR-0286 Decision 2).
func verifyJob(rpo time.Duration) string {
	interval := (rpo / 2).Truncate(time.Millisecond)
	return fmt.Sprintf("# run funcdctl backup verify every %s, off the funcd host: (backup.objectives.rpo − "+
		"backup.interval) / 2 (ADR-0205)\n", v1.Duration(((rpo - interval) / 2).Truncate(time.Millisecond)))
}

// lakeNote is the lake example, comments only (ADR-0286 Decision 3); set is lake.saveInterval as the loaded config
// holds it, "" when unset.
func lakeNote(rpo time.Duration, set string) string {
	first := "# lake: no key to set: a Lake saves its catalog before it answers a commit (ADR-0279).\n"
	if set != "" {
		first = fmt.Sprintf("# lake: lake.saveInterval %s answers a commit before its save: a crash can lose up to %s "+
			"and the two saves around\n# it (ADR-0274). Without the key a Lake saves its catalog before it answers a "+
			"commit (ADR-0279).\n", set, set)
	}
	r := v1.Duration(rpo.Truncate(time.Millisecond))
	return first + fmt.Sprintf("# If the blob store is lost, a Lake comes back from the blob mirror: funcdctl backup "+
		"plan blob --rpo %s\n# An App's own backups of its Lakes are a BackupSchedule's (scope.lakes): funcdctl "+
		"backup plan schedule -f <manifest> --rpo %s\n", r, r)
}

// planSchedule plans the schedule and ttl of the manifest's one BackupSchedule, checked by apply's pre-flight before
// and after the edit (Decision 3), and prints the edited manifest for `funcdctl apply -f -`.
func (a *cli) planSchedule(stderr io.Writer, o plan.Objectives, f planFlags) error {
	data, err := readManifest(f.file)
	if err != nil {
		return err
	}
	docs, err := sdk.DecodeManifestDocuments(data)
	if err != nil {
		return err
	}
	if err := preflight(docs); err != nil {
		return err
	}
	n := 0
	for _, d := range docs {
		if d.Object.GroupVersionKind().Kind == v1.KindBackupSchedule {
			n++
		}
	}
	if n != 1 {
		return fault.Invalidf(planOp, "-f %s holds %d BackupSchedule documents: want one", f.file, n)
	}
	settings, err := plan.Derive(plan.Schedule, o, config.Config{})
	if err != nil {
		return err
	}
	edited, err := plan.Apply(data, v1.KindBackupSchedule, settings)
	if err != nil {
		return err
	}
	if docs, err = sdk.DecodeManifestDocuments(edited); err != nil {
		return err
	}
	if err := preflight(docs); err != nil {
		return err
	}
	return a.planFinish(stderr, o, nil, edited)
}

// planFinish writes the notes to stderr, checks the RTO (Decision 5), then prints the example (ADR-0286 Decision 1).
func (a *cli) planFinish(stderr io.Writer, o plan.Objectives, notes []string, printed []byte) error {
	for _, n := range notes {
		if err := fprintf(stderr, "%s\n", n); err != nil {
			return err
		}
	}
	bound, err := plan.CheckRTO(o)
	if err != nil {
		return err
	}
	if o.RTO > 0 {
		if err := fprintf(stderr, "RTO %s: the download takes %s; only a restore drill (ADR-0206) proves the target.\n",
			v1.Duration(o.RTO), v1.Duration(bound)); err != nil {
			return err
		}
	}
	return a.writef("%s", printed)
}
