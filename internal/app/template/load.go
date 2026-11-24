// Package template renders an App template on the client (ADR-0217): a directory of App spec fragments plus a values
// schema becomes one typed, checked App. Only cmd/funcdctl imports it; the server never sees a template.
package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	yamlv3 "go.yaml.in/yaml/v3"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/expr"
	"github.com/pyvvo/funcd/pkg/sdk"
)

const loadOp = "app.load"

// Template is a loaded template directory (ADR-0217 Decisions 1 and 2, ADR-0218 Decisions 1 and 3).
type Template struct {
	Name         v1.ObjectName
	Version      string                 // strict semver
	Registry     string                 // a literal or one ${{ }} over values
	Images       map[string]string      // name → "<repo>:<version or range>", as written
	Parsed       map[string]Image       // name → ParseImage(Images[name]); Load fills it
	Lock         map[string]LockedImage // app.lock; nil when the directory has none
	ValuesSchema json.RawMessage        // empty ⇒ {"type":"object"}
	When         map[string]string      // "resources/<file>.yaml" → ${{ }} condition
	Files        map[string][]byte      // "resources/<file>.yaml" → fragment
	// Addon is addon.yaml's Addon, which deploy and dev apply before the App and render never evaluates (ADR-0337
	// Decision 1); nil when the template has none.
	Addon *v1.Addon
}

// appFile is app.yaml: a plain client file, decoded strictly (an unknown key is refused) as YAML 1.2.
type appFile struct {
	Name         string            `yaml:"name"`
	Version      string            `yaml:"version"`
	Registry     string            `yaml:"registry"`
	Images       map[string]string `yaml:"images"`
	ValuesSchema yamlv3.Node       `yaml:"valuesSchema"`
	When         map[string]string `yaml:"when"`
}

const resourcesDir = "resources"

// AddonFile is the template's Addon (ADR-0337 Decision 1).
const AddonFile = "addon.yaml"

// layoutRule is the refusal of an entry that is no file of a template.
const layoutRule = "a template holds only app.yaml, app.lock, addon.yaml and resources/*.yaml"

var imageName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load reads a template directory: app.yaml, app.lock, addon.yaml and resources/*.yaml, nothing else (ADR-0217
// Decisions 1 and 2, ADR-0218 Decision 3, ADR-0337 Decision 1).
func Load(dir string) (*Template, error) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fault.Invalidf(loadOp, "%s is not a template directory", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fault.Invalidf(loadOp, "read %s: %v", dir, err)
	}
	t := &Template{Files: map[string][]byte{}}
	var sawApp, sawLock, sawAddon bool
	for _, e := range entries {
		switch {
		case e.Name() == "app.yaml" && e.Type().IsRegular():
			sawApp = true
		case e.Name() == AddonFile && e.Type().IsRegular():
			sawAddon = true
		case e.Name() == resourcesDir && e.IsDir():
			if err := t.loadResources(filepath.Join(dir, resourcesDir)); err != nil {
				return nil, err
			}
		case e.Name() == lockFile && e.Type().IsRegular():
			sawLock = true
		default:
			return nil, fault.Invalidf(loadOp, "%s: %s", e.Name(), layoutRule)
		}
	}
	if !sawApp {
		return nil, fault.Invalidf(loadOp, "%s holds no app.yaml", dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "app.yaml")) //nolint:gosec // the template directory is the caller's argument
	if err != nil {
		return nil, fault.Invalidf(loadOp, "read app.yaml: %v", err)
	}
	if err := t.parseApp(data); err != nil {
		return nil, err
	}
	if sawLock {
		if t.Lock, err = ReadLock(dir); err != nil {
			return nil, err
		}
	}
	if sawAddon {
		if t.Addon, err = readAddon(filepath.Join(dir, AddonFile)); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// readAddon reads addon.yaml: exactly one document, an Addon that names no namespace and adds a kind, since app deploy
// finds the template's Addon by its kind (ADR-0344 Decision 10). Its ${{ }} are the Addon's own, left as written: the
// Addon's admission checks them (ADR-0332 Decision 4).
func readAddon(path string) (*v1.Addon, error) {
	data, err := os.ReadFile(path) //nolint:gosec // a file of the template directory
	if err != nil {
		return nil, fault.Invalidf(loadOp, "read %s: %v", AddonFile, err)
	}
	objs, err := sdk.DecodeManifests(data)
	if err != nil {
		return nil, fault.Wrapf(err, fault.Invalid, loadOp, "%s", AddonFile)
	}
	if len(objs) != 1 {
		return nil, fault.Invalidf(loadOp, "%s holds %d documents: it holds one Addon", AddonFile, len(objs))
	}
	a, ok := objs[0].(*v1.Addon)
	if !ok {
		return nil, fault.Invalidf(loadOp, "%s holds a %s: it holds one Addon", AddonFile, objs[0].GroupVersionKind().Kind)
	}
	if a.Namespace != "" {
		return nil, fault.Invalidf(loadOp, "%s: an Addon is cluster-scoped, so it names no namespace", AddonFile)
	}
	if a.Spec.Kind() == "" {
		return nil, fault.Invalidf(loadOp, "%s: its Addon adds no kind, and app deploy finds a template's Addon by its kind", AddonFile)
	}
	return a, nil
}

func (t *Template) loadResources(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fault.Invalidf(loadOp, "read %s: %v", resourcesDir, err)
	}
	for _, e := range entries {
		key := resourcesDir + "/" + e.Name()
		if !e.Type().IsRegular() || filepath.Ext(e.Name()) != ".yaml" {
			return fault.Invalidf(loadOp, "%s: resources/ holds only .yaml files, one level", key)
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a file of the template directory
		if err != nil {
			return fault.Invalidf(loadOp, "read %s: %v", key, err)
		}
		t.Files[key] = data
	}
	return nil
}

func (t *Template) parseApp(data []byte) error {
	var f appFile
	dec := yamlv3.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return fault.Invalidf(loadOp, "app.yaml: %v", err)
	}
	if f.Name == "" {
		return fault.Invalidf(loadOp, "app.yaml: name is required")
	}
	probe := &v1.App{TypeMeta: v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(f.Name), Namespace: "default", ResourceGroup: v1.ResourceGroupName(f.Name)}}
	if err := probe.Validate(); err != nil {
		return fault.Invalidf(loadOp, "app.yaml: name %q is not a valid App name: %v", f.Name, err)
	}
	if _, err := semver.StrictNewVersion(f.Version); err != nil {
		return fault.Invalidf(loadOp, "app.yaml: version %q is not a strict semver version (three parts, no v): %v", f.Version, err)
	}
	if len(f.Images) > 0 && f.Registry == "" {
		return fault.Invalidf(loadOp, "app.yaml: registry is required when images is not empty")
	}
	t.Parsed = make(map[string]Image, len(f.Images))
	for name, entry := range f.Images {
		if !imageName.MatchString(name) {
			return fault.Invalidf(loadOp, "app.yaml: images.%s: a name is [A-Za-z_][A-Za-z0-9_]*", name)
		}
		img, err := ParseImage(entry)
		if err != nil {
			return fault.Wrapf(err, fault.Invalid, loadOp, "app.yaml: images.%s", name)
		}
		t.Parsed[name] = img
	}
	if err := checkForm("registry", f.Registry, expr.Select, valuesRoot); err != nil {
		return fault.Invalidf(loadOp, "app.yaml: %v", err)
	}
	for key, cond := range f.When {
		if _, ok := t.Files[key]; !ok {
			return fault.Invalidf(loadOp, "app.yaml: when.%s names no file of the template", key)
		}
		if strings.TrimSpace(cond) == "" {
			return fault.Invalidf(loadOp, "app.yaml: when.%s is empty", key)
		}
		if err := checkForm("when."+key, cond, expr.Condition, valuesRoot, appRoot); err != nil {
			return fault.Invalidf(loadOp, "app.yaml: %v", err)
		}
	}
	schema := json.RawMessage(`{"type":"object"}`)
	if f.ValuesSchema.Kind != 0 {
		raw, err := nodeJSON(&f.ValuesSchema)
		if err != nil {
			return fault.Invalidf(loadOp, "app.yaml: valuesSchema: %v", err)
		}
		schema = raw
	}
	t.Name, t.Version, t.Registry, t.Images, t.ValuesSchema, t.When = v1.ObjectName(f.Name), f.Version, f.Registry, f.Images, schema, f.When
	return nil
}

// nodeJSON converts a YAML 1.2 node to JSON.
func nodeJSON(n *yamlv3.Node) (json.RawMessage, error) {
	var v interface{}
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	if err := stringKeys(v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// stringKeys refuses a mapping key that is not a string, which JSON cannot hold.
func stringKeys(v interface{}) error {
	switch t := v.(type) {
	case map[string]interface{}:
		for _, x := range t {
			if err := stringKeys(x); err != nil {
				return err
			}
		}
	case map[interface{}]interface{}:
		return fault.Invalidf(loadOp, "a mapping holds a key that is not a string")
	case []interface{}:
		for _, x := range t {
			if err := stringKeys(x); err != nil {
				return err
			}
		}
	}
	return nil
}
