package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/contract"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// cli holds the funcdctl command state (ADR-0042): where to write, the persistent connection
// flags, and an optionally injected SDK client (tests mount the real control plane on httptest).
type cli struct {
	out    io.Writer
	server string
	token  string
	client *sdk.Client // non-nil → injected (tests); else built from server/token on demand
}

// newRootCmd builds the funcdctl cobra command tree writing to out (production path).
func newRootCmd(out io.Writer) *cobra.Command { return newRootCmdWith(out, nil) }

// newRootCmdWith builds the tree with an optional injected SDK client — the test seam
// (drive via root.SetArgs(...).Execute()).
func newRootCmdWith(out io.Writer, client *sdk.Client) *cobra.Command {
	a := &cli{out: out, client: client}
	root := &cobra.Command{
		Use:           "funcdctl",
		Short:         "funcd control-plane CLI (kubectl-style)",
		SilenceUsage:  true, // funcd's api/fault error is printed once by main, not cobra's usage dump
		SilenceErrors: true,
	}
	root.SetOut(out)
	root.PersistentFlags().StringVar(&a.server, "server", envOr("FUNCD_SERVER", "http://localhost:8080"),
		"control-plane base URL ($FUNCD_SERVER)")
	root.PersistentFlags().StringVar(&a.token, "token", os.Getenv("FUNCD_TOKEN"),
		"bearer token for the authenticated control plane ($FUNCD_TOKEN)")
	root.AddCommand(
		a.getCmd(), a.describeCmd(), a.applyCmd(), a.deleteCmd(), a.logsCmd(), a.workflowCmd(), // control-plane verbs (need the SDK client)
		a.pushCmd(), a.pullCmd(), a.inspectCmd(), a.loginCmd(), a.logoutCmd(), // artifact verbs (internal/artifact; no server)
		a.benchCmd(), // data-plane load/latency probe (ADR-0053; stdlib internal/testkit/loadgen, no SDK)
	)
	return root
}

// sdkClient returns the injected client (tests) or builds one from the persistent flags. Only the
// control-plane verbs call it, so the artifact verbs never require --server/--token (ADR-0042).
func (a *cli) sdkClient() (*sdk.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	var opts []sdk.Option
	if a.token != "" {
		opts = append(opts, sdk.WithToken(a.token))
	}
	return sdk.New(a.server, opts...)
}

func (a *cli) getCmd() *cobra.Command {
	var ns, output string
	cmd := &cobra.Command{
		Use:   "get <kind> [name]",
		Short: "List a kind, or get one object (table; -o json for the object)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			return a.runGet(cmd.Context(), args, c, ns, output == "json")
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace")
	cmd.Flags().StringVarP(&output, "output", "o", "", "output format: json")
	return cmd
}

func (a *cli) describeCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "describe <kind> [name]",
		Short: "Get an object (or list) as full JSON (get -o json)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			return a.runGet(cmd.Context(), args, c, ns, true)
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace")
	return cmd
}

func (a *cli) runGet(ctx context.Context, args []string, c *sdk.Client, ns string, asJSON bool) error {
	kind, ok := sdk.KindFromToken(args[0])
	if !ok {
		return fault.Invalidf("funcdctl get", "unknown kind %q", args[0])
	}
	if len(args) >= 2 {
		obj, err := c.Get(ctx, kind, v1.NamespaceName(ns), v1.ObjectName(args[1]))
		if err != nil {
			return err
		}
		return a.renderObject(obj, asJSON)
	}
	objs, err := c.List(ctx, kind, v1.NamespaceName(ns))
	if err != nil {
		return err
	}
	return a.renderList(objs, asJSON)
}

func (a *cli) applyCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply a manifest (YAML or JSON; - for stdin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return fault.Invalidf("funcdctl apply", "usage: apply -f <file.yaml>")
			}
			data, err := readManifest(file)
			if err != nil {
				return err
			}
			obj, err := sdk.DecodeManifest(data)
			if err != nil {
				return err
			}
			// Pre-flight: the shared api/types validator, offline, before any network call.
			if verr := obj.Validate(); verr != nil {
				return fault.Wrapf(verr, fault.KindOf(verr), "funcdctl apply", "manifest is invalid")
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			applied, err := c.Apply(cmd.Context(), obj)
			if err != nil {
				return err
			}
			return a.writef("applied %s/%s\n", applied.GroupVersionKind().Kind, applied.GetName())
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "manifest file (YAML or JSON); - for stdin")
	return cmd
}

func (a *cli) deleteCmd() *cobra.Command {
	var ns string
	cmd := &cobra.Command{
		Use:   "delete <kind> <name>",
		Short: "Delete an object",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, ok := sdk.KindFromToken(args[0])
			if !ok {
				return fault.Invalidf("funcdctl delete", "unknown kind %q", args[0])
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), kind, v1.NamespaceName(ns), v1.ObjectName(args[1])); err != nil {
				return err
			}
			return a.writef("deleted %s/%s\n", kind, args[1])
		},
	}
	cmd.Flags().StringVarP(&ns, "namespace", "n", "", "namespace")
	return cmd
}

// cmdPush packages a bundle as an OCI artifact and pushes it, printing "<ref>@<digest>" to put
// in Function.spec.image (ADR-0031/0097). It talks to the registry/layout, not the control plane.
func (a *cli) pushCmd() *cobra.Command {
	var schemaPath string
	var entry string
	var runtime string
	cmd := &cobra.Command{
		Use:   "push <path> <ref>",
		Short: "Package a function (a file or a bundle directory) as an OCI artifact and push it (prints <ref>@<digest>)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, ref := args[0], args[1]
			// A DIRECTORY is a multi-file bundle (ADR-0089): the mandatory {input, output} contract
			// travels IN the bundle as __funcd_contract.json (not --schema), and PushBundle gates it
			// (VerifyBundleContract, ADR-0090) + promotes it to the OCI contract layer. A FILE is the
			// unchanged single-blob push whose contract comes from --schema.
			if info, serr := os.Stat(path); serr == nil && info.IsDir() {
				digest, err := artifact.PushBundle(cmd.Context(), ref, path, entry, runtime)
				if err != nil {
					return err
				}
				return a.writef("%s@%s\n", ref, digest)
			}
			// Every single-file function carries a mandatory single I/O contract (ADR-0090): one
			// --schema file holding {input, output} (both keys required — a void side is {"type":"null"},
			// never absent). Each side is gated against the funcd profile (the "def", ADR-0058/0060)
			// BEFORE packaging, then assembled into the {dialect, input, output} contract blob embedded
			// as OCI metadata (ADR-0059), readable later via `funcdctl inspect` without pulling the bundle.
			input, output, gerr := gateSchema(schemaPath)
			if gerr != nil {
				return gerr
			}
			blob, berr := artifact.ContractBlob(input, output)
			if berr != nil {
				return berr
			}
			digest, err := artifact.Push(cmd.Context(), ref, path, blob, runtime)
			if err != nil {
				return err
			}
			return a.writef("%s@%s\n", ref, digest)
		},
	}
	cmd.Flags().StringVar(&schemaPath, "schema", "",
		"path to the mandatory I/O contract file — one JSON object {\"input\":…,\"output\":…} (both required; a void side is {\"type\":\"null\"}); each side is gated against the funcd profile, then embedded as OCI metadata (single-file push only; a bundle carries __funcd_contract.json)")
	cmd.Flags().StringVar(&entry, "entry", "handler.py",
		"handler entry file relative to the bundle root (directory push only, ADR-0089)")
	cmd.Flags().StringVar(&runtime, "runtime", "",
		"runtime class recorded on the manifest (dev.funcd.runtime.v1, ADR-0094) so the workflow materializer resolves a step image's runtime without pulling the bundle")
	return cmd
}

// gateSchema reads the single --schema file holding one {input, output} contract document (ADR-0090),
// requires BOTH keys present, and runs each side through the funcd profile gate (ADR-0058/0060,
// contract.Check checks one schema at a time). A void side is {"type":"null"}, never absent. Contracts
// are mandatory: an empty path fails fault.Invalid (no contract-less push).
func gateSchema(path string) (input, output []byte, err error) {
	const op = "funcdctl push"
	if path == "" {
		return nil, nil, fault.Invalidf(op, "every function must declare an I/O contract (--schema <file> with {input, output})")
	}
	data, rerr := os.ReadFile(path) //nolint:gosec // path is a user-supplied CLI argument
	if rerr != nil {
		return nil, nil, fault.Invalidf(op, "read --schema %q: %v", path, rerr)
	}
	var doc struct {
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}
	if jerr := json.Unmarshal(data, &doc); jerr != nil {
		return nil, nil, fault.Invalidf(op, "--schema %q is not a valid {input, output} JSON document: %v", path, jerr)
	}
	if len(doc.Input) == 0 {
		return nil, nil, fault.Invalidf(op, "--schema %q is missing the \"input\" key (a void side is {\"type\":\"null\"})", path)
	}
	if len(doc.Output) == 0 {
		return nil, nil, fault.Invalidf(op, "--schema %q is missing the \"output\" key (a void side is {\"type\":\"null\"})", path)
	}
	if cerr := contract.Check(doc.Input); cerr != nil {
		return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "--schema %q input side is outside the funcd profile", path)
	}
	if cerr := contract.Check(doc.Output); cerr != nil {
		return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "--schema %q output side is outside the funcd profile", path)
	}
	return doc.Input, doc.Output, nil
}

// inspectCmd reads a function artifact's I/O contract from its OCI metadata (ADR-0059) — manifest +
// contract blob only, never the bundle, never running — and prints the input/output JSON Schemas.
func (a *cli) inspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <ref>[@<digest>]",
		Short: "Print a function artifact's I/O contract from its OCI metadata (no bundle pull, no run)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, digest := splitRefDigest(args[0])
			blob, err := artifact.Inspect(cmd.Context(), ref, digest)
			if err != nil {
				return err
			}
			var pretty bytes.Buffer
			if ierr := json.Indent(&pretty, blob, "", "  "); ierr != nil {
				return a.writef("%s\n", blob) // not indentable → print raw
			}
			return a.writef("%s\n", pretty.String())
		},
	}
}

// splitRefDigest splits "<ref>@<digest>" into the ref and the (possibly empty) digest. A local layout
// ref keeps its own "oci-layout://…" scheme; only a trailing "@sha256:…" is treated as the digest.
func splitRefDigest(arg string) (ref, digest string) {
	if i := strings.LastIndex(arg, "@"); i >= 0 {
		return arg[:i], arg[i+1:]
	}
	return arg, ""
}

func (a *cli) pullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <ref> <digest> [dir]",
		Short: "Fetch an artifact by digest into a dir (verifies the digest)",
		Args:  cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := ""
			if len(args) >= 3 {
				dir = args[2]
			}
			if dir == "" {
				tmp, err := os.MkdirTemp("", "funcd-pull-*")
				if err != nil {
					return fault.Internalf("funcdctl pull", "temp dir: %v", err)
				}
				dir = tmp
			}
			path, err := artifact.Pull(cmd.Context(), args[0], args[1], dir)
			if err != nil {
				return err
			}
			return a.writef("%s\n", path)
		},
	}
}

func (a *cli) loginCmd() *cobra.Command {
	var user, pass string
	cmd := &cobra.Command{
		Use:   "login <registry>",
		Short: "Store registry credentials",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if pass == "" {
				return fault.Invalidf("funcdctl login", "a password is required (-p)")
			}
			if err := artifact.Login(cmd.Context(), args[0], user, pass); err != nil {
				return err
			}
			return a.writef("login succeeded for %s\n", args[0])
		},
	}
	cmd.Flags().StringVarP(&user, "user", "u", "", "username")
	cmd.Flags().StringVarP(&pass, "password", "p", "", "password")
	return cmd
}

func (a *cli) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <registry>",
		Short: "Remove stored credentials for a registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := artifact.Logout(cmd.Context(), args[0]); err != nil {
				return err
			}
			return a.writef("logout succeeded for %s\n", args[0])
		},
	}
}

// readManifest reads a manifest file, or stdin when path is "-".
func readManifest(path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fault.Invalidf("funcdctl apply", "read stdin: %v", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied manifest path is intentional
	if err != nil {
		return nil, fault.Invalidf("funcdctl apply", "read %s: %v", path, err)
	}
	return data, nil
}

func (a *cli) renderObject(obj v1.Object, asJSON bool) error {
	if asJSON {
		b, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return fault.Internalf("funcdctl", "marshal: %v", err)
		}
		return a.writef("%s\n", string(b))
	}
	return a.writef("%s\n", obj.GetName())
}

func (a *cli) renderList(objs []v1.Object, asJSON bool) error {
	if asJSON {
		b, err := json.MarshalIndent(objs, "", "  ")
		if err != nil {
			return fault.Internalf("funcdctl", "marshal: %v", err)
		}
		return a.writef("%s\n", string(b))
	}
	if err := a.writef("NAME\n"); err != nil {
		return err
	}
	for _, o := range objs {
		if err := a.writef("%s\n", o.GetName()); err != nil {
			return err
		}
	}
	return nil
}

// writef is a checked fmt.Fprintf to the cli's writer (errcheck-clean); the ...any variadic is
// the sanctioned printf form (ADR-0002 §3 / forbidigo exclusion).
func (a *cli) writef(format string, args ...any) error {
	if _, err := fmt.Fprintf(a.out, format, args...); err != nil {
		return fault.Internalf("funcdctl", "write output: %v", err)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
