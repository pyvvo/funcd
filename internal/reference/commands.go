package reference

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/pyvvo/funcd/internal/platform/config"
)

// Command is one visible command and its flags.
type Command struct {
	Path, Use, Short, Long string
	DevOnly                bool
	Flags                  []Flag
}

// Flag is one flag of a command.
type Flag struct {
	Name, Shorthand, Type, Default, Usage string
}

// DevAnnotation marks, in a command's Annotations, a command that only the -tags dev build registers.
const DevAnnotation = "build"

// Commands lists every visible command under root, depth first, root first, with cobra's help and completion commands
// added as --help lists them. It fails, naming the command and the flag, on a text or a flag usage that names an
// ADR, and on a default that holds the value of a set FUNCD_* variable or has a part, split at "/", equal to the
// host's runtime.GOOS or runtime.GOARCH (ADR-0324 Decision 3).
func Commands(root *cobra.Command) ([]Command, error) {
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	adr := regexp.MustCompile(`\bADR-\d{4}\b`)
	host := hostValues()
	var cmds []Command
	var problems []string
	var walk func(c *cobra.Command, devOnly bool)
	walk = func(c *cobra.Command, devOnly bool) {
		if c.Hidden {
			return
		}
		devOnly = devOnly || c.Annotations[DevAnnotation] == "dev"
		cmd := Command{Path: c.CommandPath(), Use: c.UseLine(), Short: c.Short, Long: c.Long, DevOnly: devOnly}
		if adr.MatchString(c.Short + "\n" + c.Long) {
			problems = append(problems, cmd.Path+": its help text names an ADR")
		}
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Hidden || f.Name == "help" {
				return
			}
			cmd.Flags = append(cmd.Flags, Flag{
				Name: f.Name, Shorthand: f.Shorthand, Type: f.Value.Type(), Default: f.DefValue, Usage: f.Usage,
			})
			if adr.MatchString(f.Usage) {
				problems = append(problems, cmd.Path+" --"+f.Name+": its usage names an ADR")
			}
			if hostDependent(f.DefValue, host) {
				problems = append(problems, cmd.Path+" --"+f.Name+": its default depends on the machine")
			}
		})
		cmds = append(cmds, cmd)
		for _, child := range c.Commands() {
			walk(child, devOnly)
		}
	}
	walk(root, false)
	if len(problems) > 0 {
		return nil, fmt.Errorf("reference: %s", strings.Join(problems, "; "))
	}
	return cmds, nil
}

// hostValues are the values a default must not hold: those of the set FUNCD_* variables, and the host's OS and
// architecture.
func hostValues() (values []string) {
	for _, kv := range os.Environ() {
		if name, value, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "FUNCD_") && value != "" {
			values = append(values, value)
		}
	}
	return values
}

func hostDependent(def string, variables []string) bool {
	for _, value := range variables {
		if strings.Contains(def, value) {
			return true
		}
	}
	for _, part := range strings.Split(def, "/") {
		if part == runtime.GOOS || part == runtime.GOARCH {
			return true
		}
	}
	return false
}

// CommandTables renders each command as a heading, its usage line and text, and a table of its flags.
func CommandTables(cmds []Command) []byte {
	var b bytes.Buffer
	for i, c := range cmds {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "## `%s`\n\n", c.Path)
		if c.DevOnly {
			b.WriteString("funcdctl-dev only.\n\n")
		}
		text := c.Long
		if text == "" {
			text = c.Short
		}
		fmt.Fprintf(&b, "```text\n%s\n\n%s\n```\n", c.Use, strings.TrimSpace(text))
		if len(c.Flags) == 0 {
			continue
		}
		b.WriteString("\n| Flag | Type | Default | Description |\n| --- | --- | --- | --- |\n")
		for _, f := range c.Flags {
			name := code("--" + f.Name)
			if f.Shorthand != "" {
				name = code("-"+f.Shorthand) + ", " + name
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", name, f.Type, code(f.Default), cell(f.Usage))
		}
	}
	return b.Bytes()
}

// Variables lists, sorted, the FUNCD_* variables that config.Config's env tags and the Go files in dirs name: those a
// page test gives values of their own before it builds a command tree (ADR-0324 Decision 3).
func Variables(dirs ...string) ([]string, error) {
	seen := map[string]bool{}
	for _, k := range ConfigKeys(config.Config{}) {
		if name, _, _ := strings.Cut(k.Env, "="); name != "" {
			seen[name] = true
		}
	}
	literal := regexp.MustCompile(`"(FUNCD_[A-Z0-9_]+)"`)
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			return nil, fmt.Errorf("reference: %w", err)
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("reference: %w", err)
			}
			for _, m := range literal.FindAllSubmatch(data, -1) {
				seen[string(m[1])] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}
