package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ContractFile is a function's contract file, <name>.contract.yaml or <name>.contract.json (ADR-0247 Decision 1): what
// its artifact is. The Function resource says how it runs.
type ContractFile struct {
	Runtime  v1.RuntimeName      `json:"runtime"`
	Protocol v1.FunctionProtocol `json:"protocol,omitempty"`
	Input    json.RawMessage     `json:"input,omitempty"`
	Output   json.RawMessage     `json:"output,omitempty"`
}

// contractFileExts are the contract file's two variants; one name may have only one of them (ADR-0247 Decision 1).
//
//nolint:gochecknoglobals // an immutable lookup table (a slice can't be const)
var contractFileExts = []string{".contract.yaml", ".contract.json"}

// LoadContract reads <dir>/<name>.contract.yaml or .json (exactly one), decodes it strictly, validates it
// (ADR-0247 Decision 1) and returns its path. A funcdctl.yaml where it looks fails, naming the files that replace it
// (Decision 11).
func LoadContract(dir, name string) (*ContractFile, string, error) {
	const op = "sdk.LoadContract"
	paths := make([]string, len(contractFileExts))
	var found []string
	for i, ext := range contractFileExts {
		paths[i] = filepath.Join(dir, name+ext)
		if _, err := os.Stat(paths[i]); err == nil {
			found = append(found, paths[i])
		}
	}
	for _, old := range []string{filepath.Join(dir, name+".funcdctl.yaml"), filepath.Join(dir, "funcdctl.yaml")} {
		if _, err := os.Stat(old); err == nil {
			return nil, "", fault.Invalidf(op, "%s is read nowhere since ADR-0247: describe the function in %s and its Function resource file", old, paths[0])
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, "", fault.Invalidf(op, "stat %s: %v", old, err)
		}
	}
	switch len(found) {
	case 0:
		return nil, "", fault.Invalidf(op, "no contract file for %q: neither %s nor %s exists", name, paths[0], paths[1])
	case 2:
		return nil, "", fault.Invalidf(op, "two contract files for %q: %s and %s; keep one", name, paths[0], paths[1])
	}
	data, err := os.ReadFile(found[0])
	if err != nil {
		return nil, "", fault.Invalidf(op, "read %s: %v", found[0], err)
	}
	c, err := parseContractFile(op, found[0], data)
	if err != nil {
		return nil, "", err
	}
	return c, found[0], nil
}

// parseContractFile decodes a contract file strictly, as parseManifest decodes funcdctl.yaml: YAML 1.2 text is kept
// (quoteStrings, quoteTextScalars) and an unknown key is refused.
func parseContractFile(op, path string, data []byte) (*ContractFile, error) {
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(data, &doc); err != nil {
		return nil, fault.Invalidf(op, "parse %s: %v", path, err)
	}
	if len(doc.Content) > 0 {
		quoteStrings(&doc)
		quoteTextScalars(&doc, reflect.TypeOf(ContractFile{}))
		raw, err := yamlv3.Marshal(&doc)
		if err != nil {
			return nil, fault.Invalidf(op, "re-encode %s: %v", path, err)
		}
		data = raw
	}
	var c ContractFile
	if err := yaml.UnmarshalStrict(data, &c); err != nil {
		return nil, fault.Invalidf(op, "parse %s: %v", path, err)
	}
	if err := c.validate(op, path); err != nil {
		return nil, err
	}
	return &c, nil
}

// validate applies ADR-0247 Decision 1: runtime is required; cloudevent needs both sides; http needs a node runtime
// and takes neither side.
func (c *ContractFile) validate(op, path string) error {
	if c.Runtime == "" {
		return fault.Invalidf(op, "%s is missing runtime", path)
	}
	in, out := len(bytes.TrimSpace(c.Input)) > 0, len(bytes.TrimSpace(c.Output)) > 0
	switch c.Protocol {
	case "", v1.ProtocolCloudEvent:
		if !in {
			return fault.Invalidf(op, "%s is missing input (a void side is %s)", path, VoidSchema)
		}
		if !out {
			return fault.Invalidf(op, "%s is missing output (a void side is %s)", path, VoidSchema)
		}
	case v1.ProtocolHTTP:
		if !c.Runtime.IsNode() {
			return fault.Invalidf(op, "%s: protocol http needs a node runtime, not %q", path, c.Runtime)
		}
		if in || out {
			return fault.Invalidf(op, "%s: a Function with protocol http has no I/O contract; remove input and output", path)
		}
	default:
		return fault.Invalidf(op, "%s: protocol %q is neither cloudevent nor http", path, c.Protocol)
	}
	return nil
}

// Sides returns the two contract sides of a cloudevent Function, or none for an http one (ADR-0243).
func (c *ContractFile) Sides() (input, output []byte) {
	if c.Protocol == v1.ProtocolHTTP {
		return nil, nil
	}
	return c.Input, c.Output
}

// ContractName is the name a build's contract file carries: a file's stem, or a bundle directory's base name
// (ADR-0247 Decision 2).
func ContractName(build string, isDir bool) string {
	base := filepath.Base(filepath.Clean(build))
	if isDir {
		return base
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}
