package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/version"
)

// pluginPrefix names a plugin's executable: `funcdctl <name>` runs funcdctl-<name>, kubectl-style (ADR-0319).
const pluginPrefix = "funcdctl-"

// pluginCall is a plugin run in funcdctl's place: its executable, its argv and its environment.
type pluginCall struct {
	path string
	argv []string
	env  []string
}

// findPlugin resolves args, funcdctl's command line after its own name, to a plugin call (ADR-0319 Decisions 1 to 3).
// ok is false, and cobra runs args as before, unless the first word after funcdctl's own flags is a plain word, names no
// built-in command, and funcdctl-<word> is an executable on PATH. Only that first word names the plugin; every word
// after it goes to the plugin unchanged.
func findPlugin(root *cobra.Command, args, environ []string) (pluginCall, bool, error) {
	fs := pflag.NewFlagSet("funcdctl", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.SetInterspersed(false)
	fs.AddFlagSet(root.PersistentFlags())
	help := fs.BoolP("help", "h", false, "")
	if fs.Parse(args) != nil || *help || fs.NArg() == 0 {
		return pluginCall{}, false, nil
	}
	name := fs.Arg(0)
	if !plainWord(name) || isBuiltin(root, name) {
		return pluginCall{}, false, nil
	}
	// exec.LookPath also refuses one found only through a relative PATH entry (exec.ErrDot).
	path, err := exec.LookPath(pluginPrefix + name)
	if err != nil {
		return pluginCall{}, false, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return pluginCall{}, false, fault.Wrapf(err, fault.Internal, "funcdctl "+name, "find the running funcdctl for the plugin %s", path)
	}
	flags := root.PersistentFlags()
	env := pluginEnv(environ, exe, flags.Lookup("server").Value.String(), flags.Lookup("token").Value.String())
	return pluginCall{path: path, argv: append([]string{path}, fs.Args()[1:]...), env: env}, true, nil
}

// pluginEnv is environ with what a plugin is handed: FUNCDCTL, the funcdctl that called it, so it calls the same
// version back; FUNCDCTL_VERSION; and FUNCD_SERVER and FUNCD_TOKEN as funcdctl resolved them from its flags or its
// environment. An empty token is not handed on, so `--token ""` clears the environment's.
func pluginEnv(environ []string, exe, server, token string) []string {
	set := []string{"FUNCDCTL=" + exe, "FUNCDCTL_VERSION=" + version.Version, "FUNCD_SERVER=" + server}
	if token != "" {
		set = append(set, "FUNCD_TOKEN="+token)
	}
	env := make([]string, 0, len(environ)+len(set))
	for _, kv := range environ {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "FUNCDCTL", "FUNCDCTL_VERSION", "FUNCD_SERVER", "FUNCD_TOKEN":
		default:
			env = append(env, kv)
		}
	}
	return append(env, set...)
}

// plainWord reports whether s can name a plugin: a lowercase letter, then lowercase letters, digits and dashes. A
// versioned funcdctl such as funcdctl-v0.39.3 is no plugin, and a word with a slash is never tried as a path.
func plainWord(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// runPlugin replaces funcdctl's process with the plugin's, so the exit code, the stdio and the signals sent to
// funcdctl's PID are the plugin's own. It returns only when the exec fails.
func runPlugin(c pluginCall) error {
	err := syscall.Exec(c.path, c.argv, c.env) //nolint:gosec // the plugin the user named, found on their PATH
	name := strings.TrimPrefix(filepath.Base(c.path), pluginPrefix)
	return fault.Wrapf(err, fault.Internal, "funcdctl "+name, "run the plugin %s", c.path)
}

// isBuiltin reports whether name is one of funcdctl's commands, cobra's help and completion commands included, which
// cobra adds only when it runs, so a plugin of that name never runs. cobra's hidden __complete commands are no plain
// words.
func isBuiltin(root *cobra.Command, name string) bool {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, c := range root.Commands() {
		if c.Name() == name || c.HasAlias(name) {
			return true
		}
	}
	return false
}

// pluginCmd is `funcdctl plugin list`.
func (a *cli) pluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Plugins: `funcdctl <name>` runs funcdctl-<name> from PATH when <name> is no built-in command",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List the plugins on PATH, and warn about each one that never runs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return listPlugins(cmd.Root(), filepath.SplitList(os.Getenv("PATH")), a.out, cmd.ErrOrStderr())
		},
	})
	return cmd
}

// listPlugins prints, in PATH order, each plain-word funcdctl-<name> executable of an absolute PATH entry, and warns on
// errOut about each one that never runs: a built-in command or an earlier entry takes its name (ADR-0319 Decision 7). A
// relative entry is skipped, as the lookup skips it.
func listPlugins(root *cobra.Command, dirs []string, out, errOut io.Writer) error {
	var rows, warnings []string
	first := map[string]string{}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) || seen[filepath.Clean(dir)] {
			continue
		}
		seen[filepath.Clean(dir)] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name, ok := strings.CutPrefix(e.Name(), pluginPrefix)
			path := filepath.Join(dir, e.Name())
			if !ok || !plainWord(name) {
				continue
			}
			if _, err := exec.LookPath(path); err != nil {
				continue
			}
			rows = append(rows, name+"\t"+path)
			switch {
			case isBuiltin(root, name):
				warnings = append(warnings, fmt.Sprintf("warning: %s never runs: %q is a built-in command", path, name))
			case first[name] != "":
				warnings = append(warnings, fmt.Sprintf("warning: %s never runs: %s comes first on PATH", path, first[name]))
			default:
				first[name] = path
			}
		}
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "no plugins on PATH")
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tPATH")
	for _, r := range rows {
		_, _ = fmt.Fprintln(tw, r)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, w := range warnings {
		_, _ = fmt.Fprintln(errOut, w)
	}
	return nil
}
