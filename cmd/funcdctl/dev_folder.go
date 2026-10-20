//go:build dev

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// devFolder is what `funcdctl dev [dir]` reads (ADR-0247 Decision 4): the funcd resources of the folder's top-level
// *.yaml files, the build of each Function and of each Workflow image step (Decision 6), and the stamp of every file it
// read, which the reload compares (Decision 8).
type devFolder struct {
	dir       string
	resources []devDoc
	functions []*devFunction
	steps     []*devStep
	stamps    map[string]string // file → its stamp when read; a folder listing is keyed "<dir>/"
}

// devDoc is one resource document and the file it came from.
type devDoc struct {
	obj  v1.Object
	file string
}

// devFunction is a Function document and the build it runs from source.
type devFunction struct {
	fn    *v1.Function
	file  string
	build devBuild
}

// devStep is a Workflow image step and the build its tag stem names (Decision 5).
type devStep struct {
	workflow *v1.Workflow
	index    int
	name     v1.ObjectName // <workflow>-<step>, the Function the workflow materializer builds (Decision 7)
	build    devBuild
}

// devBuild is a function's build: its runtime and contract from its contract file, and the entry it runs.
type devBuild struct {
	name         string
	contract     *sdk.ContractFile
	contractPath string
	srcDir       string // the directory holding entry
	entry        string
	inPlace      bool // a python entry that runs in place: its bundle links every file of srcDir
}

// loadDevFolder reads dir as ADR-0247 Decision 4 says: its top-level *.yaml files, skipping contract files, documents
// that are not funcd's and FuncdConfig documents, and stopping on an App (ADR-0253 runs one from its template), on two
// documents of one kind and name, on a namespace other than default, and on a file that does not parse. A funcdctl.yaml
// stops it, naming the files that replace it (Decision 11).
func loadDevFolder(op, dir string) (*devFolder, error) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fault.Invalidf(op, "%s is not a folder: funcdctl dev runs a folder of resource files and contract files (ADR-0247)", dir)
	}
	f := &devFolder{dir: dir, stamps: map[string]string{}}
	if err := refuseManifests(op, dir); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fault.Internalf(op, "list %s: %v", dir, err)
	}
	sort.Strings(files)
	f.stamps[dir+string(filepath.Separator)] = listingStamp(files)
	seen := map[string]string{}
	for _, file := range files {
		if _, _, isContract := contractFileName(file); isContract {
			continue
		}
		if err := f.stampFile(file); err != nil {
			return nil, fault.Invalidf(op, "read %s: %v", file, err)
		}
		docs, derr := devDocuments(op, file)
		if derr != nil {
			return nil, derr
		}
		for _, obj := range docs {
			key := string(obj.GroupVersionKind().Kind) + "/" + string(obj.GetName())
			if prev, dup := seen[key]; dup {
				return nil, fault.Invalidf(op, "two %s documents named %q: %s and %s", obj.GroupVersionKind().Kind, obj.GetName(), prev, file)
			}
			seen[key] = file
			f.resources = append(f.resources, devDoc{obj: obj, file: file})
		}
	}
	if err := f.resolveBuilds(op); err != nil {
		return nil, err
	}
	return f, nil
}

// refuseManifests stops on a funcdctl.yaml or <name>.funcdctl.yaml in dir (ADR-0247 Decision 11).
func refuseManifests(op, dir string) error {
	old, err := filepath.Glob(filepath.Join(dir, "*funcdctl.yaml"))
	if err != nil {
		return fault.Internalf(op, "list %s: %v", dir, err)
	}
	if len(old) > 0 {
		return fault.Invalidf(op, "%s is read nowhere since ADR-0247: describe each function in its resource file and "+
			"its <name>.contract.yaml", strings.Join(old, ", "))
	}
	return nil
}

// devDocuments decodes file's funcd documents, skipping the rest and FuncdConfig documents, and stopping on an App, a
// namespace other than default, and a document that does not parse. Each kept object lands in the dev namespace, in
// group dev when it names none, tagged as dev's (Decision 5).
func devDocuments(op, file string) ([]v1.Object, error) {
	objs, err := resourceDocuments(op, file)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		kind := obj.GroupVersionKind().Kind
		if kind == v1.KindApp {
			return nil, fault.Invalidf(op, "%s: funcdctl dev runs an App from its template directory (app.yaml beside resources/), not from a resource file", file)
		}
		meta := obj.GetObjectMeta()
		if meta.Namespace != "" && meta.Namespace != devNamespace {
			return nil, fault.Invalidf(op, "%s: %s %q is in namespace %q; funcdctl dev runs namespace %s only",
				file, kind, obj.GetName(), meta.Namespace, devNamespace)
		}
		meta.Namespace = devNamespace
		if meta.ResourceGroup == "" {
			meta.ResourceGroup = devResourceGroup
		}
		if meta.Tags == nil {
			meta.Tags = v1.Tags{}
		}
		meta.Tags[devManagedTag] = devManagedBy
	}
	return objs, nil
}

// resolveBuilds finds the build of each Function and of each Workflow image step (Decision 6).
func (f *devFolder) resolveBuilds(op string) error {
	pyEntries, err := f.pyprojectEntries(op)
	if err != nil {
		return err
	}
	for _, d := range f.resources {
		switch o := d.obj.(type) {
		case *v1.Function:
			b, berr := f.resolveBuild(op, string(o.Name), strings.HasPrefix(string(o.Spec.Runtime), "python"), pyEntries)
			if berr != nil {
				return fault.Wrapf(berr, fault.KindOf(berr), op, "Function %q of %s", o.Name, d.file)
			}
			if b.contract.Runtime != o.Spec.Runtime || b.contract.Protocol.Normalized() != o.Spec.Protocol.Normalized() {
				return fault.Invalidf(op, "%s and %s disagree on the runtime or protocol of %q", d.file, b.contractPath, o.Name)
			}
			f.functions = append(f.functions, &devFunction{fn: o, file: d.file, build: b})
		case *v1.Workflow:
			for i, st := range o.Spec.Steps {
				if st.Function == nil || st.Function.Image == "" {
					continue
				}
				stem, terr := tagStem(op, st.Function.Image)
				if terr != nil {
					return fault.Wrapf(terr, fault.KindOf(terr), op, "Workflow %q step %q of %s", o.Name, st.Name, d.file)
				}
				python := f.isPythonStep(stem, pyEntries)
				b, berr := f.resolveBuild(op, stem, python, pyEntries)
				if berr != nil {
					return fault.Wrapf(berr, fault.KindOf(berr), op, "Workflow %q step %q of %s", o.Name, st.Name, d.file)
				}
				f.steps = append(f.steps, &devStep{workflow: o, index: i, name: v1.ObjectName(string(o.Name) + "-" + string(st.Name)), build: b})
			}
		}
	}
	return nil
}

// isPythonStep reports whether stem names a python build: no <stem>.mjs in the folder or dist/, and a python entry.
func (f *devFolder) isPythonStep(stem string, pyEntries map[string]string) bool {
	for _, p := range []string{filepath.Join(f.dir, stem+".mjs"), filepath.Join(f.dir, "dist", stem+".mjs")} {
		if _, err := os.Stat(p); err == nil {
			return false
		}
	}
	_, listed := pyEntries[stem]
	if _, err := os.Stat(filepath.Join(f.dir, stem+".py")); listed || err == nil {
		return true
	}
	return f.isSrcMain(stem, pyEntries)
}

// isSrcMain reports whether name runs from src/main.py: no [tool.funcd-bundle.functions] list, the folder named after
// it and src/main.py present (Decision 6).
func (f *devFolder) isSrcMain(name string, pyEntries map[string]string) bool {
	if pyEntries != nil {
		return false
	}
	abs, err := filepath.Abs(f.dir)
	if err != nil || filepath.Base(abs) != name {
		return false
	}
	_, serr := os.Stat(filepath.Join(f.dir, "src", "main.py"))
	return serr == nil
}

// resolveBuild finds name's build and contract file (Decision 6). A node build is <name>.mjs in the folder or in
// dist/ (both is an error), with its contract file beside it. A python one runs in place from its entry in
// [tool.funcd-bundle.functions], else from src/main.py when the folder is named after it, else from <name>.py; its
// contract file is at the folder root. A missing build names the paths looked at.
func (f *devFolder) resolveBuild(op, name string, python bool, pyEntries map[string]string) (devBuild, error) {
	b := devBuild{name: name}
	var contractDir string
	if !python {
		root, dist := filepath.Join(f.dir, name+".mjs"), filepath.Join(f.dir, "dist", name+".mjs")
		_, rerr := os.Stat(root)
		_, derr := os.Stat(dist)
		switch {
		case rerr == nil && derr == nil:
			return b, fault.Invalidf(op, "two builds of %q: %s and %s; keep one", name, root, dist)
		case rerr == nil:
			b.srcDir, b.entry = f.dir, name+".mjs"
		case derr == nil:
			b.srcDir, b.entry = filepath.Join(f.dir, "dist"), name+".mjs"
		default:
			return b, buildMiss{fault.Invalidf(op, "no build of %q: neither %s nor %s exists (build it with vite build)", name, root, dist)}
		}
		contractDir = b.srcDir
	} else {
		var entry string
		var looked []string
		switch {
		case pyEntries[name] != "":
			entry = filepath.Join(f.dir, filepath.FromSlash(pyEntries[name]))
			looked = []string{entry}
		case pyEntries == nil && f.isSrcMain(name, nil):
			entry = filepath.Join(f.dir, "src", "main.py")
		default:
			entry = filepath.Join(f.dir, name+".py")
			looked = append(looked, filepath.Join(f.dir, "pyproject.toml [tool.funcd-bundle.functions]"), filepath.Join(f.dir, "src", "main.py"))
		}
		if _, err := os.Stat(entry); err != nil {
			return b, buildMiss{fault.Invalidf(op, "no build of %q: looked at %s", name, strings.Join(append(looked, entry), ", "))}
		}
		b.srcDir, b.entry, b.inPlace = filepath.Dir(entry), filepath.Base(entry), true
		contractDir = f.dir
	}
	c, path, err := sdk.LoadContract(contractDir, name)
	if err != nil {
		return b, contractMiss(contractDir, name, err)
	}
	b.contract, b.contractPath = c, path
	if err := f.stampFile(path); err != nil {
		return b, fault.Invalidf(op, "read %s: %v", path, err)
	}
	return b, nil
}

// pyprojectEntries is [tool.funcd-bundle.functions] of the folder's pyproject.toml, nil when it has none.
func (f *devFolder) pyprojectEntries(op string) (map[string]string, error) {
	path := filepath.Join(f.dir, "pyproject.toml")
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err := f.stampFile(path); err != nil {
		return nil, fault.Invalidf(op, "read %s: %v", path, err)
	}
	entries, err := pyprojectFunctions(op, path)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// stampFile records path's stamp, which the reload compares (Decision 8).
func (f *devFolder) stampFile(path string) error {
	s, err := fileStamp(path)
	if err != nil {
		return err
	}
	f.stamps[path] = s
	return nil
}

// listingStamp is the stamp of a folder listing: its *.yaml names, so an added or removed file reloads.
func listingStamp(files []string) string {
	sum := sha256.New()
	for _, p := range files {
		_, _ = fmt.Fprintf(sum, "%s\n", filepath.Base(p))
	}
	return fmt.Sprintf("%x", sum.Sum(nil))
}

// contractBlob is the ADR-0123 contract dev delivers beside a build: none for an http Function (ADR-0243).
func (b devBuild) contractBlob() ([]byte, error) {
	in, out := b.contract.Sides()
	if in == nil {
		return nil, nil
	}
	return artifact.ContractBlob(in, out)
}

// fingerprint digests the size and mtime of the files whose change reloads b: its contract file, its entry and, for a
// python entry that runs in place, every file under its directory but dot-entries, __pycache__ and node_modules. A
// Function carries it as spec.imageDigest, and a step names its copy after it (Decision 7), so a rebuild rolls a new
// revision.
func (b devBuild) fingerprint(stateDirs []string) (string, error) {
	sum := sha256.New()
	add := func(p, stamp string) { _, _ = fmt.Fprintf(sum, "%s\x00%s\n", p, stamp) }
	for _, p := range []string{b.contractPath, filepath.Join(b.srcDir, b.entry)} {
		stamp, err := fileStamp(p)
		if err != nil {
			return "", err
		}
		add(p, stamp)
	}
	if b.inPlace {
		root, err := filepath.Abs(b.srcDir)
		if err != nil {
			return "", err
		}
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return nil //nolint:nilerr // an entry removed mid-walk changes the fingerprint on the next poll
			}
			name := d.Name()
			ignored := p != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__")
			if ignored || d.IsDir() && slices.Contains(stateDirs, p) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if fi, ierr := d.Info(); ierr == nil && fi.Mode().IsRegular() {
				add(p, infoStamp(fi))
			}
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("sha256:%x", sum.Sum(nil)), nil
}

// builds are the builds of every Function and step of the folder.
func (f *devFolder) builds() []devBuild {
	var out []devBuild
	for _, fn := range f.functions {
		out = append(out, fn.build)
	}
	for _, st := range f.steps {
		out = append(out, st.build)
	}
	return out
}
