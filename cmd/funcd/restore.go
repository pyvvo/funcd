package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/backup"
	"github.com/pyvvo/funcd/internal/backup/envelope"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/platform/version"
	"github.com/pyvvo/funcd/internal/restore"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

// restoreFlags are the flags every restore subcommand takes (ADR-0206 Decision 1).
type restoreFlags struct {
	config, from, credentialsFile, escrow, timeline string
	identities                                      []string
}

func (f *restoreFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.config, "config", "", "path to funcdconfig.yaml (as the daemon locates it)")
	cmd.Flags().StringVar(&f.from, "from", "", "the backup target to read (default backup.target)")
	cmd.Flags().StringVar(&f.credentialsFile, "credentials-file", "",
		"the operator's restore credential for an s3:// target (never backup.credentialsFile)")
	cmd.Flags().StringArrayVar(&f.identities, "identity", nil, "an age identity file that opens the generation (repeatable)")
	cmd.Flags().StringVar(&f.escrow, "escrow", "", "the escrow directory holding the secrets key and master secret")
	cmd.Flags().StringVar(&f.timeline, "timeline", "", "keep only this timeline and those it descends from")
}

// open loads the config and opens the restore source and identities.
func (f *restoreFlags) open(ctx context.Context) (restore.Options, func(), error) {
	path, err := config.Locate(f.config)
	if err != nil {
		return restore.Options{}, nil, err
	}
	cfg, err := config.Load(path, config.Flags{})
	if err != nil {
		return restore.Options{}, nil, err
	}
	from := f.from
	if from == "" {
		from = cfg.Backup.Target
	}
	if from == "" {
		return restore.Options{}, nil, fault.Invalidf("funcd restore", "no --from and backup.target is empty")
	}
	ids, err := envelope.ReadIdentities(f.identities)
	if err != nil {
		return restore.Options{}, nil, err
	}
	src, err := gocloud.OpenWith(ctx, from, gocloud.OpenOptions{CredentialsFile: f.credentialsFile})
	if err != nil {
		return restore.Options{}, nil, fault.Wrapf(err, fault.Invalid, "funcd restore", "open %s", from)
	}
	return restore.Options{Config: cfg, Source: src, Identities: ids, EscrowDir: f.escrow},
		func() { _ = src.Close() }, nil
}

// point parses a restore point and applies --timeline.
func (f *restoreFlags) point(s string) (restore.Point, error) {
	p, err := restore.ParsePoint(s)
	p.Timeline = f.timeline
	return p, err
}

// newRestoreCmd is `funcd restore` (ADR-0206 Decision 1): offline work on the backup target and the data
// directories. `restore kv` and `restore blob` join it with ADR-0209 and ADR-0208.
func newRestoreCmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "List, inspect and restore platform backup generations (offline; the restored platform boots held)",
	}
	cmd.AddCommand(newRestoreListCmd(out), newRestoreInspectCmd(out), newRestoreRunCmd(out))
	return cmd
}

func newRestoreListCmd(out io.Writer) *cobra.Command {
	var f restoreFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the generations as a lineage tree: each timeline under the generation its restore loaded",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o, done, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer done()
			gens, err := restore.List(cmd.Context(), o.Source)
			if err != nil {
				return err
			}
			return writeTree(out, gens)
		},
	}
	f.register(cmd)
	return cmd
}

// writeTree prints a row per generation, each timeline indented under the generation its restore loaded; a
// timeline whose parent is not listed starts at the left.
func writeTree(out io.Writer, gens []restore.Generation) error {
	byTimeline := map[string][]restore.Generation{}
	children := map[backup.GenRef][]string{}
	listed := map[backup.GenRef]bool{}
	var timelines []string
	for _, g := range gens {
		tl := g.Manifest.Timeline
		if _, ok := byTimeline[tl]; !ok {
			timelines = append(timelines, tl)
		}
		byTimeline[tl] = append(byTimeline[tl], g)
		listed[g.Ref()] = true
	}
	parentOf := map[string]backup.GenRef{}
	for _, tl := range timelines {
		for _, g := range byTimeline[tl] {
			if p := g.Manifest.Parent; p != nil {
				parentOf[tl] = *p
			}
		}
		if p, ok := parentOf[tl]; ok && listed[p] && !slices.Contains(children[p], tl) {
			children[p] = append(children[p], tl)
		}
	}
	w := &errWriter{w: out}
	w.printf("%-36s %-12s %-24s %-14s %s\n", "GENERATION", "CLASS", "AT", "FUNCD", "STATE")
	var walk func(tl string, depth int)
	walk = func(tl string, depth int) {
		rows := byTimeline[tl]
		for i, g := range rows {
			at := "-"
			if !time.Time(g.Manifest.At).IsZero() {
				at = g.Manifest.At.String()
			}
			name := strings.Repeat("  ", depth) + fmt.Sprintf("%s/%d", tl, g.Manifest.Generation)
			w.printf("%-36s %-12s %-24s %-14s %s\n", name, g.Class, at, cmp.Or(g.Manifest.Funcd, "-"), g.State)
			if i+1 == len(rows) || rows[i+1].Ref() != g.Ref() {
				for _, child := range children[g.Ref()] {
					walk(child, depth+1)
				}
			}
		}
	}
	for _, tl := range timelines {
		if p, ok := parentOf[tl]; !ok || !listed[p] {
			walk(tl, 0)
		}
	}
	return w.err
}

// errWriter keeps the first write error.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, args...)
	}
}

// inspectReport is what `restore inspect` prints (Decision 1): the manifest with the keys it needs, the version
// verdict, the counts per kind, the evidence, and --diff.
type inspectReport struct {
	Generation        backup.GenRef   `json:"generation"`
	Class             backup.Class    `json:"class"`
	State             string          `json:"state"`
	Manifest          backup.Manifest `json:"manifest"`
	Version           string          `json:"version"`
	Counts            map[v1.Kind]int `json:"counts"`
	NonTerminalRuns   []string        `json:"nonTerminalRuns,omitempty"`
	RecordsWithoutRun []string        `json:"recordsWithoutRun,omitempty"`
	DeadLetters       []string        `json:"deadLetters,omitempty"`
	Diff              *diffReport     `json:"diff,omitempty"`
}

type diffReport struct {
	Since   backup.GenRef        `json:"since"`
	Added   map[v1.Kind][]string `json:"added,omitempty"`
	Removed map[v1.Kind][]string `json:"removed,omitempty"`
	Changed map[v1.Kind][]string `json:"changed,omitempty"`
}

func newRestoreInspectCmd(out io.Writer) *cobra.Command {
	var f restoreFlags
	var diff, object, secretsKey, output string
	var reveal bool
	cmd := &cobra.Command{
		Use:   "inspect <point>",
		Short: "Show a generation: manifest, keys needed, version verdict, counts, evidence; one object; a diff",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output != "yaml" && output != "json" {
				return fault.Invalidf("funcd restore inspect", "unknown output %q (want yaml or json)", output)
			}
			ctx := cmd.Context()
			o, done, err := f.open(ctx)
			if err != nil {
				return err
			}
			defer done()
			if secretsKey != "" {
				if o.SecretsKey, err = os.ReadFile(secretsKey); err != nil { //nolint:gosec // the operator's escrowed key file
					return fault.Wrapf(err, fault.Invalid, "funcd restore inspect", "read --secrets-key %s", secretsKey)
				}
			}
			gens, err := restore.List(ctx, o.Source)
			if err != nil {
				return err
			}
			view, g, err := inspectPoint(ctx, &f, gens, args[0], o)
			if err != nil {
				return err
			}
			defer func() { _ = view.Close() }()
			if object != "" {
				return writeObject(ctx, out, view, object, reveal, output)
			}
			rep, err := report(ctx, g, view)
			if err != nil {
				return err
			}
			if diff != "" {
				from, fg, err := inspectPoint(ctx, &f, gens, diff, o)
				if err != nil {
					return err
				}
				defer func() { _ = from.Close() }()
				if rep.Diff, err = diffOf(fg, from, view); err != nil {
					return err
				}
			}
			data, err := json.Marshal(rep)
			return render(out, data, err, output)
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&diff, "diff", "", "list per kind the objects added, removed or changed since this point")
	cmd.Flags().StringVar(&object, "object", "", "print one object, <Kind>/<namespace>/<name>, for `funcdctl apply -f`")
	cmd.Flags().StringVar(&secretsKey, "secrets-key", "", "the escrowed secrets key file, to decode Secrets (--object)")
	cmd.Flags().BoolVar(&reveal, "reveal-secrets", false, "print Secret values (with --object and --secrets-key)")
	cmd.Flags().StringVarP(&output, "output", "o", "yaml", "output format: yaml or json")
	return cmd
}

func inspectPoint(ctx context.Context, f *restoreFlags, gens []restore.Generation, s string, o restore.Options) (*restore.View, restore.Generation, error) {
	p, err := f.point(s)
	if err != nil {
		return nil, restore.Generation{}, err
	}
	g, err := restore.Resolve(gens, p)
	if err != nil {
		return nil, restore.Generation{}, err
	}
	v, err := restore.Inspect(ctx, g, o)
	return v, g, err
}

func report(ctx context.Context, g restore.Generation, v *restore.View) (inspectReport, error) {
	rep := inspectReport{Generation: g.Ref(), Class: g.Class, State: g.State, Manifest: g.Manifest, Version: "restores",
		Counts: map[v1.Kind]int{}}
	if err := restore.CheckVersion(g.Manifest.Funcd, version.Version); err != nil {
		rep.Version = err.Error()
	}
	for _, k := range v1.AllKinds() {
		n := len(v.Secrets)
		if k != v1.KindSecret {
			l, err := v.Meta.List(ctx, k.GVK(), store.ListOptions{})
			if err != nil {
				return inspectReport{}, err
			}
			n = len(l.Items)
		}
		if n > 0 {
			rep.Counts[k] = n
		}
	}
	runs, err := v.Meta.List(ctx, v1.KindWorkflowRun.GVK(), store.ListOptions{})
	if err != nil {
		return inspectReport{}, err
	}
	have := map[string]bool{}
	for _, o := range runs.Items {
		ref := string(o.GetNamespace()) + "/" + string(o.GetName())
		have[ref] = true
		if run, ok := o.(*v1.WorkflowRun); ok && !slices.Contains([]v1.RunPhase{v1.RunSucceeded, v1.RunFailed, v1.RunCancelled}, run.Status.Phase) {
			rep.NonTerminalRuns = append(rep.NonTerminalRuns, ref)
		}
	}
	recs, err := v.Runs.List(ctx, runstate.ListOptions{})
	if err != nil {
		return inspectReport{}, err
	}
	for _, r := range recs {
		if ref := string(r.Namespace) + "/" + string(r.Name); !have[ref] {
			rep.RecordsWithoutRun = append(rep.RecordsWithoutRun, ref)
		}
	}
	nss, err := v.Meta.List(ctx, v1.KindNamespace.GVK(), store.ListOptions{})
	if err != nil {
		return inspectReport{}, err
	}
	for _, ns := range nss.Items {
		dls, err := v.Events.DeadLetters().List(ctx, v1.NamespaceName(ns.GetName()))
		if err != nil {
			return inspectReport{}, err
		}
		for _, dl := range dls {
			rep.DeadLetters = append(rep.DeadLetters, string(ns.GetName())+"/"+dl.ID)
		}
	}
	return rep, nil
}

func diffOf(since restore.Generation, from, to *restore.View) (*diffReport, error) {
	added, removed, changed, err := restore.Diff(from, to)
	if err != nil {
		return nil, err
	}
	names := func(m map[v1.Kind][]v1.ObjectRef) map[v1.Kind][]string {
		out := map[v1.Kind][]string{}
		for k, refs := range m {
			for _, r := range refs {
				out[k] = append(out[k], string(r.Namespace)+"/"+string(r.Name))
			}
		}
		return out
	}
	return &diffReport{Since: since.Ref(), Added: names(added), Removed: names(removed), Changed: names(changed)}, nil
}

// writeObject prints one object as `funcdctl apply -f` recreates it (Decision 5). A Secret that does not decode
// (no --secrets-key) prints by its key alone.
func writeObject(ctx context.Context, out io.Writer, v *restore.View, object string, reveal bool, output string) error {
	const op = "funcd restore inspect"
	parts := strings.Split(object, "/")
	var ns, name string
	switch len(parts) {
	case 2:
		name = parts[1]
	case 3:
		ns, name = parts[1], parts[2]
	default:
		return fault.Invalidf(op, "--object %q: want <Kind>/<namespace>/<name>", object)
	}
	kind := v1.Kind(parts[0])
	if _, ok := v1.NewObject(kind); !ok {
		return fault.Invalidf(op, "--object %q: unknown kind %q", object, parts[0])
	}
	obj, err := v.Meta.Get(ctx, kind.GVK(), v1.NamespaceName(ns), v1.ObjectName(name))
	if err != nil && kind == v1.KindSecret && fault.KindOf(err) != fault.NotFound &&
		slices.Contains(v.Secrets, v1.ObjectRef{Kind: kind, Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name)}) {
		s := &v1.Secret{ObjectMeta: v1.ObjectMeta{Namespace: v1.NamespaceName(ns), Name: v1.ObjectName(name)}}
		if _, err := fmt.Fprintf(out, "# Secret %s/%s: its values do not decode without --secrets-key\n", ns, name); err != nil {
			return err
		}
		obj = s
	} else if err != nil {
		return err
	}
	e, err := restore.Export(obj, reveal)
	if err != nil {
		return err
	}
	data, err := json.Marshal(e)
	return render(out, data, err, output)
}

// render writes v's JSON encoding as YAML or indented JSON; v is the JSON of the value printed.
func render(out io.Writer, v []byte, err error, output string) error {
	if err == nil && output == "json" {
		var b []byte
		b, err = json.MarshalIndent(json.RawMessage(v), "", "  ")
		v = append(b, '\n')
	} else if err == nil {
		v, err = yaml.JSONToYAML(v)
	}
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "funcd restore", "encode the output")
	}
	_, err = out.Write(v)
	return err
}

func newRestoreRunCmd(out io.Writer) *cobra.Command {
	var f restoreFlags
	var newMaster bool
	cmd := &cobra.Command{
		Use:   "run <point>",
		Short: "Restore a generation into the empty data directories; funcd then boots held until `funcdctl hold release`",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := f.point(args[0])
			if err != nil {
				return err
			}
			o, done, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer done()
			o.NewMasterSecret, o.Out = newMaster, out
			start := time.Now()
			rep, err := restore.Run(cmd.Context(), p, o)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "restored %s/%d as timeline %s in %s; the platform is held: start funcd, read "+
				"`funcdctl hold status`, then `funcdctl hold release`\n", rep.From.Timeline, rep.From.Generation,
				rep.Timeline, time.Since(start).Round(time.Millisecond))
			return err
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&newMaster, "new-master-secret", false,
		"restore without the generation's master secret: the credentials derived from it change")
	return cmd
}
