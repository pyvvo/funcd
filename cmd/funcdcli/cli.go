package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/artifact"
	"github.com/green-0-rabbit/funcd/internal/contract"
	"github.com/green-0-rabbit/funcd/pkg/sdk"
)

// cli holds the funcdcli command state (ADR-0042): where to write, the persistent connection
// flags, and an optionally injected SDK client (tests mount the real control plane on httptest).
type cli struct {
	out    io.Writer
	server string
	token  string
	client *sdk.Client // non-nil → injected (tests); else built from server/token on demand
}

// newRootCmd builds the funcdcli cobra command tree writing to out (production path).
func newRootCmd(out io.Writer) *cobra.Command { return newRootCmdWith(out, nil) }

// newRootCmdWith builds the tree with an optional injected SDK client — the test seam
// (drive via root.SetArgs(...).Execute()).
func newRootCmdWith(out io.Writer, client *sdk.Client) *cobra.Command {
	a := &cli{out: out, client: client}
	root := &cobra.Command{
		Use:           "funcdcli",
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
		a.getCmd(), a.describeCmd(), a.applyCmd(), a.deleteCmd(), // control-plane verbs (need the SDK client)
		a.pushCmd(), a.pullCmd(), a.loginCmd(), a.logoutCmd(), // artifact verbs (internal/artifact; no server)
		a.benchCmd(), // data-plane load/latency probe (ADR-0053; stdlib internal/loadgen, no SDK)
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
		return fault.Invalidf("funcdcli get", "unknown kind %q", args[0])
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
		Short: "Apply a manifest (JSON; - for stdin)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if file == "" {
				return fault.Invalidf("funcdcli apply", "usage: apply -f <file.json>")
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
				return fault.Wrapf(verr, fault.KindOf(verr), "funcdcli apply", "manifest is invalid")
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
	cmd.Flags().StringVarP(&file, "file", "f", "", "manifest file (JSON); - for stdin")
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
				return fault.Invalidf("funcdcli delete", "unknown kind %q", args[0])
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
// in Function.spec.artifact (ADR-0031). It talks to the registry/layout, not the control plane.
func (a *cli) pushCmd() *cobra.Command {
	var contracts []string
	cmd := &cobra.Command{
		Use:   "push <file> <ref>",
		Short: "Package a bundle as an OCI artifact and push it (prints <ref>@<digest>)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Gate the build-generated contract schema(s) against the funcd profile (the "def",
			// ADR-0058/0060) BEFORE packaging — an out-of-profile contract never ships. The schema
			// is the source of truth (funcd compiled the validator from it); the build emits it and
			// passes it here with --contract (input and/or output).
			for _, path := range contracts {
				schema, rerr := os.ReadFile(path) //nolint:gosec // path is a user-supplied CLI argument
				if rerr != nil {
					return fault.Invalidf("funcdcli push", "read contract %q: %v", path, rerr)
				}
				if cerr := contract.Check(schema); cerr != nil {
					return fault.Wrapf(cerr, fault.KindOf(cerr), "funcdcli push", "contract %q is outside the funcd profile", path)
				}
			}
			digest, err := artifact.Push(cmd.Context(), args[1], args[0])
			if err != nil {
				return err
			}
			return a.writef("%s@%s\n", args[1], digest)
		},
	}
	cmd.Flags().StringSliceVar(&contracts, "contract", nil,
		"path to a generated contract JSON Schema to gate against the funcd profile before pushing (repeatable: input + output)")
	return cmd
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
					return fault.Internalf("funcdcli pull", "temp dir: %v", err)
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
				return fault.Invalidf("funcdcli login", "a password is required (-p)")
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
			return nil, fault.Invalidf("funcdcli apply", "read stdin: %v", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied manifest path is intentional
	if err != nil {
		return nil, fault.Invalidf("funcdcli apply", "read %s: %v", path, err)
	}
	return data, nil
}

func (a *cli) renderObject(obj v1.Object, asJSON bool) error {
	if asJSON {
		b, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return fault.Internalf("funcdcli", "marshal: %v", err)
		}
		return a.writef("%s\n", string(b))
	}
	return a.writef("%s\n", obj.GetName())
}

func (a *cli) renderList(objs []v1.Object, asJSON bool) error {
	if asJSON {
		b, err := json.MarshalIndent(objs, "", "  ")
		if err != nil {
			return fault.Internalf("funcdcli", "marshal: %v", err)
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
		return fault.Internalf("funcdcli", "write output: %v", err)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
