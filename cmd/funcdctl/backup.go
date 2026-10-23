package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
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

// planFlags are backup plan's flags (ADR-0223 Decision 1).
type planFlags struct {
	rpo, keep, rto, size, speed, config, file string
	write                                     bool
}

// backupPlanCmd turns recovery objectives into settings, offline (ADR-0223, ADR-0274): it builds no SDK client.
func (a *cli) backupPlanCmd() *cobra.Command {
	var f planFlags
	cmd := &cobra.Command{
		Use:   "plan <platform|kv|blob|schedule|lake>",
		Short: "Turn recovery objectives into backup settings the daemon accepts (offline)",
		Long: "Turn recovery objectives into backup settings: --rpo (the most data a loss may take) and --keep (how far " +
			"back a restore point must reach) set the platform, kv or blob keys of funcdconfig.yaml, or the schedule and " +
			"ttl of a BackupSchedule (-f); --rpo sets the catalog save interval of a Lake's ConfigMap (-f). The result " +
			"passes the daemon's own loader or apply's pre-flight, then is printed, or written in place with --write. " +
			"--rto with --size and --speed checks the download time, a lower bound: only a restore drill proves an RTO. " +
			"Exit 1: refused, nothing written; 3: the RTO is below the download time.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := plan.Component(args[0])
			o, err := f.objectives(c, cmd.Flags().Changed)
			if err != nil {
				return err
			}
			switch c {
			case plan.Schedule:
				return a.planManifest(cmd.ErrOrStderr(), c, v1.KindBackupSchedule, o, f)
			case plan.Lake:
				return a.planManifest(cmd.ErrOrStderr(), c, v1.KindConfigMap, o, f)
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
	fl.StringVar(&f.config, "config", "", "platform, kv, blob: path to funcdconfig.yaml (else $FUNCD_CONFIG, "+
		"./funcdconfig.yaml, /etc/funcd/funcdconfig.yaml)")
	fl.StringVarP(&f.file, "file", "f", "", "schedule: a manifest holding one BackupSchedule; lake: one ConfigMap; - for stdin")
	fl.BoolVar(&f.write, "write", false, "edit the file in place instead of printing")
	return cmd
}

// objectives applies Decision 1's flag rules, and ADR-0274's for lake, and parses the given values.
func (f planFlags) objectives(c plan.Component, changed func(string) bool) (plan.Objectives, error) {
	var o plan.Objectives
	switch c {
	case plan.Platform, plan.KV, plan.Blob:
		if changed("file") {
			return o, fault.Invalidf(planOp, "-f applies to schedule and lake; %s reads --config", c)
		}
	case plan.Schedule, plan.Lake:
		switch {
		case changed("config"):
			return o, fault.Invalidf(planOp, "--config applies to platform, kv and blob; %s reads -f", c)
		case f.file == "":
			return o, fault.Invalidf(planOp, "%s needs -f <manifest>", c)
		case f.write && f.file == "-":
			return o, fault.Invalidf(planOp, "--write needs a file: -f - reads stdin")
		}
	default:
		return o, fault.Invalidf(planOp, "component %q: want platform, kv, blob, schedule or lake", c)
	}
	if c == plan.Lake {
		for _, name := range []string{"keep", "rto", "size", "speed"} {
			if changed(name) {
				return o, fault.Invalidf(planOp, "--%s does not apply to lake: --rpo sets its one setting, the catalog "+
					"save interval; its restore points are the App backup's", name)
			}
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

// planConfig plans the platform, kv or blob keys of the located config file (Decisions 2-4).
func (a *cli) planConfig(stderr io.Writer, c plan.Component, o plan.Objectives, f planFlags) error {
	path, err := config.Locate(f.config)
	if err != nil {
		return err
	}
	if f.write && path == "" {
		return fault.NotFoundf(planOp, "--write: no config file found: give --config or set $FUNCD_CONFIG")
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
	merged, changes, err := plan.Apply(data, "", settings)
	if err != nil {
		return err
	}
	warns, err := plan.CheckConfig(path, merged)
	if err != nil {
		return err
	}
	notes := make([]string, 0, len(warns)+1)
	for _, w := range warns {
		notes = append(notes, "warning: "+w.Key+": "+w.Message)
	}
	if c == plan.Platform && o.RPO > 0 {
		interval := (o.RPO / 2).Truncate(time.Millisecond)
		notes = append(notes, fmt.Sprintf("run funcdctl backup verify every %s: (backup.objectives.rpo − "+
			"backup.interval) / 2 (ADR-0205)", v1.Duration(((o.RPO-interval)/2).Truncate(time.Millisecond))))
	}
	printed, _, err := plan.Apply(nil, "", settings)
	if err != nil {
		return err
	}
	return a.planFinish(stderr, o, notes, planResult{path: path, edited: merged, printed: printed, changes: changes,
		write: f.write, next: "restart funcd to apply"})
}

// planManifest plans the keys of the manifest's one document of kind, the schedule and ttl of a BackupSchedule
// (Decision 3) or a Lake ConfigMap's save interval (ADR-0274), checked by apply's pre-flight before and after the edit.
func (a *cli) planManifest(stderr io.Writer, c plan.Component, kind v1.Kind, o plan.Objectives, f planFlags) error {
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
		if d.Object.GroupVersionKind().Kind == kind {
			n++
		}
	}
	if n != 1 {
		return fault.Invalidf(planOp, "-f %s holds %d %s documents: want one", f.file, n, kind)
	}
	settings, err := plan.Derive(c, o, config.Config{})
	if err != nil {
		return err
	}
	edited, changes, err := plan.Apply(data, kind, settings)
	if err != nil {
		return err
	}
	if docs, err = sdk.DecodeManifestDocuments(edited); err != nil {
		return err
	}
	if err := preflight(docs); err != nil {
		return err
	}
	return a.planFinish(stderr, o, nil, planResult{path: f.file, edited: edited, printed: edited, changes: changes,
		write: f.write, next: "apply it live: funcdctl apply -f " + f.file})
}

// planResult is what a checked plan prints, or writes to path.
type planResult struct {
	path, next      string
	edited, printed []byte
	changes         []plan.Change
	write           bool
}

// planFinish writes the notes to stderr, checks the RTO (Decision 5), then prints the result or commits the file and
// lists its changes (Decision 4).
func (a *cli) planFinish(stderr io.Writer, o plan.Objectives, notes []string, r planResult) error {
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
	if !r.write {
		return a.writef("%s", r.printed)
	}
	if len(r.changes) > 0 {
		if err := commitFile(r.path, r.edited); err != nil {
			return err
		}
	}
	for _, ch := range r.changes {
		old := ch.Old
		if old == "" {
			old = "(absent)"
		}
		if err := a.writef("%s: %s → %s\n", ch.Key, old, ch.New); err != nil {
			return err
		}
	}
	return fprintf(stderr, "%s\n", r.next)
}

// commitFile replaces the file at path with data (Decision 4): through its symlinks, a temp file beside the resolved
// file, fsync, then a rename over it; its mode kept, and its owner and group where chown is allowed.
func commitFile(path string, data []byte) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fault.Invalidf(planOp, "write %s: %v", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fault.Invalidf(planOp, "write %s: %v", resolved, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(resolved), "."+filepath.Base(resolved)+".plan-*")
	if err != nil {
		return fault.Invalidf(planOp, "write %s: %v", resolved, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = tmp.Chown(int(st.Uid), int(st.Gid)) // refused unless privileged: the file is then the operator's own
	}
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), resolved)
	}
	if err != nil {
		return fault.Invalidf(planOp, "write %s: %v", resolved, err)
	}
	return nil
}
