package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// manifestFileName is the colocated authoring manifest funcdctl reads (ADR-0122): the client-side
// analogue of wrangler.toml. Its presence beside a pushed file (or in a pushed bundle dir) makes it
// the PRIMARY contract source (source-resolution: funcdctl.yaml → bundle __funcd_contract.json →
// --schema → code-derived).
const manifestFileName = "funcdctl.yaml"

// bundleContractName is the bundle's embedded I/O contract file (ADR-0090). When a funcdctl.yaml is
// primary for a bundle push, funcdctl compiles the manifest's inline contract into this file so
// PushBundle bakes the manifest's schema (mirrors internal/artifact's bundleContractFile).
const bundleContractName = "__funcd_contract.json"

// resolveManifest returns the funcdctl.yaml governing a push of path (ADR-0122/ADR-0124). A bundle dir
// resolves the generic <dir>/funcdctl.yaml (a bundle is one function). A single file resolves the
// per-function-named <dir>/<stem>.funcdctl.yaml FIRST (stem = the file's basename minus extension, e.g.
// front.mjs → front.funcdctl.yaml), then falls back to the generic <dir>/funcdctl.yaml — so a directory
// holding several single-file functions carries one manifest per function. Absent ⇒ (nil, "", nil)
// (the legacy push path runs — unchanged).
func resolveManifest(path string) (*sdk.Manifest, string, error) {
	dir := path
	isDir := false
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		isDir = true
	} else {
		dir = filepath.Dir(path)
	}
	var candidates []string
	if !isDir {
		base := filepath.Base(path)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		candidates = append(candidates, filepath.Join(dir, stem+"."+manifestFileName))
	}
	candidates = append(candidates, filepath.Join(dir, manifestFileName))
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		m, err := sdk.LoadManifest(candidate)
		if err != nil {
			return nil, "", err
		}
		return m, candidate, nil
	}
	return nil, "", nil
}

// pushFromManifest packages a push whose contract and runtime come from a funcdctl.yaml (ADR-0122):
// it gates each schema side against the funcd profile (contract.Check), then bakes the schema-only
// contract and pushes — recording the runtime annotation, with no language toolchain invoked.
func (a *cli) pushFromManifest(ctx context.Context, path, ref string, m *sdk.Manifest, entryFlag string, platform v1.OCIPlatform) error {
	const op = "funcdctl push"
	input, output, err := gateManifestContract(op, m)
	if err != nil {
		return err
	}
	runtime := string(m.Runtime)

	info, serr := os.Stat(path)
	if serr == nil && info.IsDir() {
		// funcdctl.yaml is primary for the bundle: compile its inline contract into the bundle's
		// __funcd_contract.json so PushBundle (which re-gates + promotes it) bakes the manifest's schema.
		if werr := writeBundleContract(op, path, input, output); werr != nil {
			return werr
		}
		digest, perr := artifact.PushBundle(ctx, ref, path, entryFlag, runtime, platform)
		if perr != nil {
			return perr
		}
		return a.writef("%s@%s\n", ref, digest)
	}

	blob, berr := artifact.ContractBlob(input, output)
	if berr != nil {
		return berr
	}
	digest, perr := artifact.Push(ctx, ref, path, blob, runtime, platform)
	if perr != nil {
		return perr
	}
	return a.writef("%s@%s\n", ref, digest)
}

// gateManifestContract returns the manifest's two contract sides after gating each against the funcd
// profile (ADR-0058, contract.Check). push and types share it, so both reject the same contracts.
func gateManifestContract(op string, m *sdk.Manifest) (input, output []byte, err error) {
	input, output, err = m.ContractSides()
	if err != nil {
		return nil, nil, err
	}
	if cerr := contract.Check(input); cerr != nil {
		return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "funcdctl.yaml contract input side is outside the funcd profile")
	}
	if cerr := contract.Check(output); cerr != nil {
		return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "funcdctl.yaml contract output side is outside the funcd profile")
	}
	return input, output, nil
}

// writeBundleContract materializes the manifest's gated {input, output} sides into the bundle dir's
// __funcd_contract.json — the compiled contract PushBundle bakes. Overwriting realizes "funcdctl.yaml
// is the primary contract source": its inline schema wins over any stale on-disk contract.
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

// typesCmd generates editor-facing declaration files (.pyi / .d.ts) from a funcdctl.yaml (ADR-0122
// Decision 4): FuncInput/FuncOutput from the contract schema plus a typed binding context. DX-only.
func (a *cli) typesCmd() *cobra.Command {
	var file, outDir string
	cmd := &cobra.Command{
		Use:   "types",
		Short: "Generate FuncInput/FuncOutput + binding types from a funcdctl.yaml (.pyi / .d.ts; DX-only)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			m, err := sdk.LoadManifest(file)
			if err != nil {
				return err
			}
			if _, _, cerr := gateManifestContract("funcdctl types", m); cerr != nil {
				return cerr
			}
			files, gerr := sdk.GenerateTypes(m)
			if gerr != nil {
				return gerr
			}
			for name, content := range files {
				dest := filepath.Join(outDir, name)
				if werr := os.WriteFile(dest, content, 0o644); werr != nil { //nolint:gosec // DX-only generated types in the author's workspace
					return fault.Internalf("funcdctl types", "write %s: %v", dest, werr)
				}
				if perr := a.writef("wrote %s\n", dest); perr != nil {
					return perr
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&file, "file", "f", manifestFileName, "funcdctl.yaml manifest to generate types from")
	cmd.Flags().StringVarP(&outDir, "out", "o", ".", "output directory for the generated declaration files")
	return cmd
}
