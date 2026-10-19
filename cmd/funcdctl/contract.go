package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// bundleContractName is the bundle's embedded I/O contract file (ADR-0090). funcdctl writes it from the contract file
// so PushBundle bakes it (mirrors internal/artifact's bundleContractFile); it is funcdctl's own file (ADR-0247
// Decision 2).
const bundleContractName = "__funcd_contract.json"

// buildContract returns the contract file of a build (ADR-0247 Decision 2): <dir>/<stem>.contract.* beside a file
// <dir>/<stem>.<ext>, and <dir>/<base>.contract.* at the root of a bundle directory <dir> named <base>.
func buildContract(path string) (c *sdk.ContractFile, isDir bool, err error) {
	info, serr := os.Stat(path)
	if serr != nil {
		return nil, false, fault.Invalidf("funcdctl push", "stat %s: %v", path, serr)
	}
	isDir = info.IsDir()
	dir := path
	if !isDir {
		dir = filepath.Dir(path)
	}
	c, _, err = sdk.LoadContract(dir, sdk.ContractName(path, isDir))
	return c, isDir, err
}

// gateContract returns the contract file's two sides after gating each against the funcd profile (ADR-0058,
// contract.Check), or none for an http Function. push and types share it, so both reject the same contracts.
func gateContract(op string, c *sdk.ContractFile) (input, output []byte, err error) {
	input, output = c.Sides()
	if input == nil {
		return nil, nil, nil
	}
	if cerr := contract.Check(input); cerr != nil {
		return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "the contract file's input is outside the funcd profile")
	}
	if cerr := contract.Check(output); cerr != nil {
		return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "the contract file's output is outside the funcd profile")
	}
	return input, output, nil
}

// pushEntryFor is a bundle push's entry: --entry when set, else main.py for a python runtime and main.mjs otherwise
// (ADR-0247 Decision 2).
func pushEntryFor(entry string, rt v1.RuntimeName) string {
	if entry != "" {
		return entry
	}
	if strings.HasPrefix(string(rt), "python") {
		return "main.py"
	}
	return "main.mjs"
}

// pushFromContract pushes a build with the contract and runtime of its contract file (ADR-0247 Decision 2): it gates
// and bakes input and output, or none for an http Function, and records the runtime.
func (a *cli) pushFromContract(ctx context.Context, path, ref, entry string, platform v1.OCIPlatform) error {
	const op = "funcdctl push"
	c, isDir, err := buildContract(path)
	if err != nil {
		return err
	}
	input, output, err := gateContract(op, c)
	if err != nil {
		return err
	}
	runtime := string(c.Runtime)
	var digest string
	switch {
	case isDir && input == nil:
		// One an earlier push left would stop the shim (ADR-0243 Decision 2).
		if rerr := os.Remove(filepath.Join(path, bundleContractName)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return fault.Internalf(op, "remove %s: %v", bundleContractName, rerr)
		}
		digest, err = artifact.PushBundleNoContract(ctx, ref, path, pushEntryFor(entry, c.Runtime), runtime, platform)
	case isDir:
		if werr := writeBundleContract(op, path, input, output); werr != nil {
			return werr
		}
		digest, err = artifact.PushBundle(ctx, ref, path, pushEntryFor(entry, c.Runtime), runtime, platform)
	default:
		var blob []byte
		if input != nil {
			if blob, err = artifact.ContractBlob(input, output); err != nil {
				return err
			}
		}
		digest, err = artifact.Push(ctx, ref, path, blob, runtime, platform)
	}
	if err != nil {
		return err
	}
	return a.writef("%s@%s\n", ref, digest)
}

// writeBundleContract writes the gated {input, output} sides into the bundle dir's __funcd_contract.json, the
// compiled contract PushBundle bakes.
func writeBundleContract(op, dir string, input, output []byte) error {
	doc := struct {
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
	}{Input: input, Output: output}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fault.Internalf(op, "marshal bundle contract: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dir, bundleContractName), data, 0o644); werr != nil { //nolint:gosec // non-secret compiled contract in the user's bundle dir
		return fault.Internalf(op, "write %s: %v", bundleContractName, werr)
	}
	return nil
}

// contractFileName splits a contract file path into its folder and function name, or reports it is not one.
func contractFileName(path string) (dir, name string, ok bool) {
	base := filepath.Base(path)
	for _, ext := range []string{".contract.yaml", ".contract.json"} {
		if n, found := strings.CutSuffix(base, ext); found && n != "" {
			return filepath.Dir(path), n, true
		}
	}
	return "", "", false
}

// functionDocument returns the Function document named name among the *.yaml files at the top level of dir and its
// file, or nil when there is none (a step, an App part). Contract files and documents that do not decode as funcd
// resources are skipped; two Functions named name are an error naming both files (ADR-0247 Decisions 3 and 4).
func functionDocument(op, dir, name string) (*v1.Function, string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, "", fault.Internalf(op, "list %s: %v", dir, err)
	}
	var found *v1.Function
	var foundFile string
	for _, f := range files {
		if _, _, isContract := contractFileName(f); isContract {
			continue
		}
		docs, derr := resourceDocuments(op, f)
		if derr != nil {
			return nil, "", derr
		}
		for _, d := range docs {
			fn, isFn := d.(*v1.Function)
			if !isFn || string(fn.Name) != name {
				continue
			}
			if found != nil {
				return nil, "", fault.Invalidf(op, "two Functions named %q: %s and %s", name, foundFile, f)
			}
			found, foundFile = fn, f
		}
	}
	return found, foundFile, nil
}

// resourceDocuments decodes the funcd documents of a resource file (ADR-0247 Decision 4): it skips a document whose
// apiVersion is not funcd's and a FuncdConfig, and fails, naming the file, on YAML that does not parse and on a funcd
// document that does not decode.
func resourceDocuments(op, file string) ([]v1.Object, error) {
	data, err := os.ReadFile(file) //nolint:gosec // a file of the user's project
	if err != nil {
		return nil, fault.Invalidf(op, "read %s: %v", file, err)
	}
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	var out []v1.Object
	for n := 1; ; n++ {
		var doc yamlv3.Node
		if derr := dec.Decode(&doc); errors.Is(derr, io.EOF) {
			return out, nil
		} else if derr != nil {
			return nil, fault.Invalidf(op, "%s: parse document %d: %v", file, n, derr)
		}
		if len(doc.Content) == 0 || doc.Content[0].Kind != yamlv3.MappingNode {
			continue
		}
		apiVersion, kind := mappingValue(doc.Content[0], "apiVersion"), mappingValue(doc.Content[0], "kind")
		if apiVersion != v1.KindFunction.GVK().APIVersion() || kind == config.Kind {
			continue
		}
		raw, merr := yamlv3.Marshal(&doc)
		if merr != nil {
			return nil, fault.Invalidf(op, "%s: re-encode document %d: %v", file, n, merr)
		}
		obj, oerr := sdk.DecodeManifest(raw)
		if oerr != nil {
			return nil, fault.Invalidf(op, "%s: document %d: %v", file, n, oerr)
		}
		out = append(out, obj)
	}
}

// mappingValue is the scalar value of key in a YAML mapping, or "".
func mappingValue(m *yamlv3.Node, key string) string {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1].Value
		}
	}
	return ""
}

// pyprojectFunctions is the [tool.funcd-bundle.functions] table of the pyproject.toml at path (name → entry relative
// to its folder), nil when it has none (ADR-0247 Decisions 6 and 10).
func pyprojectFunctions(op, path string) (map[string]string, error) {
	var project struct {
		Tool struct {
			FuncdBundle struct {
				Functions map[string]string `toml:"functions"`
			} `toml:"funcd-bundle"`
		} `toml:"tool"`
	}
	if _, err := toml.DecodeFile(path, &project); err != nil {
		return nil, fault.Invalidf(op, "parse %s: %v", path, err)
	}
	return project.Tool.FuncdBundle.Functions, nil
}

// pythonSourceDir is the directory of a python Function's source entry (ADR-0247 Decision 6), relative to dir: its
// entry in pyproject.toml's [tool.funcd-bundle.functions], else src when dir is named after it and holds src/main.py,
// else dir itself, where <name>.py runs.
func pythonSourceDir(op, dir, name string) (string, error) {
	path := filepath.Join(dir, "pyproject.toml")
	if _, err := os.Stat(path); err == nil {
		entries, perr := pyprojectFunctions(op, path)
		if perr != nil {
			return "", perr
		}
		if entries != nil {
			if entry, ok := entries[name]; ok {
				return filepath.Join(dir, filepath.Dir(filepath.FromSlash(entry))), nil
			}
			return dir, nil
		}
	}
	abs, aerr := filepath.Abs(dir)
	if aerr != nil {
		return "", fault.Internalf(op, "resolve %s: %v", dir, aerr)
	}
	if filepath.Base(abs) == name {
		if _, serr := os.Stat(filepath.Join(dir, "src", "main.py")); serr == nil {
			return filepath.Join(dir, "src"), nil
		}
	}
	return dir, nil
}

// typesCmd writes funcd_types.py or funcd.d.ts from a contract file and its Function document (ADR-0247 Decision 3):
// FuncInput and FuncOutput from the contract file, and the binding types of the Function named after it in the same
// folder, if any. DX-only.
func (a *cli) typesCmd() *cobra.Command {
	var file, outDir string
	cmd := &cobra.Command{
		Use:   "types -f <name>.contract.yaml",
		Short: "Generate FuncInput/FuncOutput + binding types from a contract file and its Function (.py / .d.ts; DX-only)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			const op = "funcdctl types"
			dir, name, ok := contractFileName(file)
			if !ok {
				return fault.Invalidf(op, "-f %q is not a contract file (<name>.contract.yaml or <name>.contract.json)", file)
			}
			c, path, err := sdk.LoadContract(dir, name)
			if err != nil {
				return err
			}
			if _, _, err := gateContract(op, c); err != nil {
				return err
			}
			fn, fnFile, err := functionDocument(op, dir, name)
			if err != nil {
				return err
			}
			var bindings v1.FunctionBindings
			if fn != nil {
				if fn.Spec.Runtime != c.Runtime || fn.Spec.Protocol.Normalized() != c.Protocol.Normalized() {
					return fault.Invalidf(op, "%s and %s disagree on the runtime or protocol of %q", fnFile, path, name)
				}
				bindings = fn.Spec.Bindings
			}
			files, err := sdk.GenerateTypes(c, bindings)
			if err != nil {
				return err
			}
			out := outDir
			if out == "" {
				out = "."
				if strings.HasPrefix(string(c.Runtime), "python") {
					if out, err = pythonSourceDir(op, dir, name); err != nil {
						return err
					}
				}
			}
			for fname, content := range files {
				dest := filepath.Join(out, fname)
				if werr := os.WriteFile(dest, content, 0o644); werr != nil { //nolint:gosec // DX-only generated types in the author's workspace
					return fault.Internalf(op, "write %s: %v", dest, werr)
				}
				if perr := a.writef("wrote %s\n", dest); perr != nil {
					return perr
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", "", "the contract file, <name>.contract.yaml or <name>.contract.json")
	cmd.Flags().StringVarP(&outDir, "out", "o", "", "output directory (default: a python Function's source entry directory, else .)")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}
