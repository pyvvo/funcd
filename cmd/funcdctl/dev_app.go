//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	neturl "net/url"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/app/template"
	"github.com/pyvvo/funcd/internal/artifact"
	sqlembedengine "github.com/pyvvo/funcd/internal/sql/embedengine"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// App mode (ADR-0253): `funcdctl dev <template dir>` deploys an App template on the embedded platform from local
// builds. Dev pushes each image's build to an OCI layout in its state dir, renders the template with that layout as its
// registry and a lock of those pushes, and applies the App as `app deploy` does. The App reconciler then writes the
// parts, stores, Routes and Sites, and stamps a revision per change.

// Part kinds an image serves.
const (
	partFunction = "function"
	partStep     = "step"
	partSite     = "site"
)

// devFollowPoll paces the follow of each new revision (ADR-0253 Decision 8).
const devFollowPoll = 100 * time.Millisecond

// devAppKeypairName is the name the S3 keypair is derived over for an App with no function part (Decision 11).
const devAppKeypairName = "dev"

// isAppTemplate reports whether dir is an App template directory (ADR-0217): an app.yaml beside resources/.
func isAppTemplate(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "app.yaml"))
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	ri, err := os.Stat(filepath.Join(dir, "resources"))
	return err == nil && ri.IsDir()
}

// devImage is one image of the template and what dev last pushed for it.
type devImage struct {
	part    string
	runtime v1.RuntimeName // a function part's runtime
	build   devBuild       // a function or step build
	site    string         // a Site's build folder
	print   string         // the fingerprint of the build last pushed
	digest  string         // the digest of that push
}

// devAppSession is the state the boot and each reload of an App share.
type devAppSession struct {
	dir     string // the template directory
	cfg     devConfig
	layout  string // the OCI layout each build is pushed to, one repository per image
	stage   string // where a build directory or the template is copied before it is read
	persist string // dev's state dir (the --persist-to dir under --persist), which dev skips inside the template directory
	images  map[string]*devImage
	stamp   string      // the stamp of every input at the last reload
	applied []v1.Object // the --apply documents applied last
	out     io.Writer
	logger  *slog.Logger
	// cancelFollow stops the follow of the revision applied last; the boot sets it first, then only the watcher.
	cancelFollow context.CancelFunc
}

// startDevApp boots the embedded platform for the App template at dir and deploys it from local builds: the --apply
// documents first, then the Secrets the App declares, then the App. A refusal at start stops dev (Decision 8).
func (a *cli) startDevApp(ctx context.Context, dir string, cfg devConfig, logger *slog.Logger) (_ *devInstance, err error) {
	const op = "funcdctl dev"
	plan, err := resolvePersistPlan(cfg)
	if err != nil {
		return nil, err
	}
	processDir, err := devStateDir(cfg, dir)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(processDir)
	s := &devAppSession{dir: dir, cfg: cfg, layout: filepath.Join(root, "oci"), stage: filepath.Join(root, "stage"),
		images: map[string]*devImage{}, out: a.out, logger: logger, persist: stateRoot(processDir)}
	for _, d := range []string{s.layout, s.stage} {
		if merr := os.MkdirAll(d, 0o700); merr != nil {
			return nil, fault.Wrapf(merr, fault.Internal, op, "create %s", d)
		}
	}
	if s.stamp, err = s.inputsStamp(); err != nil {
		return nil, err
	}
	d, err := s.prepare(ctx, op)
	if err != nil {
		return nil, err
	}
	folder := s.platformFolder(d.app, d.docs)
	if (len(d.app.Spec.SQL) > 0 || folder.bindsSQL()) && !sqlembedengine.Bundled() {
		return nil, fault.Unavailablef(op, "App %s declares a SQL database, but this funcdctl has no SQL engine: build it with "+
			"just build-sql-engine %s %s, then go build -tags dev ./cmd/funcdctl", d.app.Name, goruntime.GOOS, goruntime.GOARCH)
	}
	inst := &devInstance{runErr: make(chan error, 1), logger: logger, functions: folder.names(), workflows: folder.workflowNames(),
		runtimes: &devRuntimes{byImage: map[string]v1.RuntimeName{}}, artifactDir: filepath.Join(root, "artifacts"),
		s3Name: appKeypairName(d.app), app: d.app}
	defer func() {
		if err != nil {
			inst.unwind(0)
		}
	}()
	cancelRun, err := a.startPlatform(ctx, op, folder, plan, cfg, inst)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			cancelRun()
			<-inst.runErr
		}
	}()
	client, err := sdk.New(inst.controlURL, sdk.WithToken(funcd.DevToken))
	if err != nil {
		return nil, err
	}
	inst.client = client
	start := time.Now()
	r, err := s.apply(ctx, op, client, d)
	if err != nil {
		return nil, err
	}
	if plan.storeDir != "" {
		// a --persist restart deletes what an earlier session applied that the --apply files no longer hold (Decision 7)
		if _, err = pruneStale(ctx, client, slices.Concat(d.docs, d.secrets), logger); err != nil {
			return nil, fault.Wrapf(err, fault.KindOf(err), op, "prune the documents an earlier session applied")
		}
	}
	deployed := func() string {
		return fmt.Sprintf("App %s deployed in %s", d.app.Name, time.Since(start).Round(time.Millisecond))
	}
	if r.applied {
		s.follow(ctx, a, client, d.app, r, deployed)
	} else if err = a.writef("%s\n", deployed()); err != nil {
		return nil, err
	}
	inst.watchDone = make(chan struct{})
	go s.watch(ctx, op, a, client, inst.watchDone)
	return inst, nil
}

// devAppDesired is what one render asks dev to apply: the App, the --apply documents and the Secrets dev creates.
type devAppDesired struct {
	app     *v1.App
	docs    []v1.Object
	secrets []v1.Object
}

// prepare reads the --apply files, loads the template and the values, pushes each build that changed since its last
// push, and renders the App with dev's layout as its registry and a lock of those pushes. The App's hooks stay only
// under --hooks; an App that requires another is refused. It also returns the Secrets the App declares that no --apply
// document defines, from the environment.
func (s *devAppSession) prepare(ctx context.Context, op string) (devAppDesired, error) {
	docs, err := applyDocuments(op, s.cfg.apply)
	if err != nil {
		return devAppDesired{}, err
	}
	t, err := s.loadTemplate(op)
	if err != nil {
		return devAppDesired{}, err
	}
	values, err := readValues(s.cfg.values)
	if err != nil {
		return devAppDesired{}, err
	}
	in := template.RenderInput{Namespace: devNamespace, Values: values}
	t.Registry = "oci-layout://" + s.layout
	used, lock, _, err := probeImages(t, in)
	if err != nil {
		return devAppDesired{}, err
	}
	keys := make([]string, 0, len(used))
	for key := range used {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		img := used[key]
		if err := s.resolveImage(op, key, img); err != nil {
			return devAppDesired{}, err
		}
		l := lock[key]
		if err := s.push(ctx, op, key, t.Parsed[key].Repo, l.Version, img); err != nil {
			return devAppDesired{}, err
		}
		l.Digest = img.digest
		lock[key] = l
		s.images[key] = img
	}
	t.Lock = lock
	app, err := template.Render(t, in)
	if err != nil {
		return devAppDesired{}, err
	}
	if len(app.Spec.Requires) > 0 {
		r := app.Spec.Requires[0]
		return devAppDesired{}, fault.Invalidf(op, "App %s requires App %s %s: funcdctl dev does not run an App that requires another yet",
			app.Name, r.App, r.Version)
	}
	if !s.cfg.hooks {
		app.Spec.Hooks = nil
	}
	secrets, err := devAppSecrets(op, withoutDefined(app, docs), s.cfg.secrets)
	if err != nil {
		return devAppDesired{}, err
	}
	return devAppDesired{app: app, docs: docs, secrets: secrets}, nil
}

// applyDocuments reads each --apply file's resource documents as folder mode reads a resource file (devDocuments):
// tagged as dev's, in the dev namespace, and refusing an App or another namespace (Decision 7). Two documents of one
// kind and name stop it.
func applyDocuments(op string, files []string) ([]v1.Object, error) {
	var out []v1.Object
	seen := map[string]string{}
	for _, file := range files {
		docs, err := devDocuments(op, file)
		if err != nil {
			return nil, err
		}
		for _, obj := range docs {
			key := string(obj.GroupVersionKind().Kind) + "/" + string(obj.GetName())
			if prev, dup := seen[key]; dup {
				return nil, fault.Invalidf(op, "two %s documents named %q: %s and %s", obj.GroupVersionKind().Kind, obj.GetName(), prev, file)
			}
			seen[key] = file
			out = append(out, obj)
		}
	}
	return out, nil
}

// loadTemplate reads the template directory as template.Load does, skipping only dot-entries (in resources/ too) and
// dev's persist dir (Decision 3): it copies app.yaml and resources/ into a snapshot under the stage dir and loads that,
// so any other entry stops it with Load's refusal. app.lock is neither read nor written.
func (s *devAppSession) loadTemplate(op string) (*template.Template, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fault.Invalidf(op, "read %s: %v", s.dir, err)
	}
	snap, err := os.MkdirTemp(s.stage, "template-")
	if err != nil {
		return nil, fault.Wrapf(err, fault.Internal, op, "create a template snapshot")
	}
	defer func() { _ = os.RemoveAll(snap) }()
	for _, e := range entries {
		src := filepath.Join(s.dir, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), ".") || s.skipped(src) || e.Name() == "app.lock":
		case e.Name() == "app.yaml" && !e.IsDir():
			if err := copyFile(src, filepath.Join(snap, e.Name())); err != nil {
				return nil, fault.Invalidf(op, "copy %s: %v", src, err)
			}
		case e.Name() == "resources" && e.IsDir():
			if err := copyResources(src, filepath.Join(snap, e.Name())); err != nil {
				return nil, fault.Invalidf(op, "copy %s: %v", src, err)
			}
		default:
			return nil, fault.Invalidf(op, "%s: a template holds only app.yaml, app.lock and resources/*.yaml", e.Name())
		}
	}
	return template.Load(snap)
}

// copyResources copies each entry of resources/ but its dot-entries (an editor's swap file, a .DS_Store): a file as is,
// a directory as an empty one, which template.Load then refuses.
func copyResources(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.Name(), "."):
		case e.IsDir():
			if err := os.MkdirAll(filepath.Join(dst, e.Name()), 0o700); err != nil {
				return err
			}
		default:
			if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyFile copies the file at src to dst, a symlink as a symlink, so template.Load sees what the template holds.
func copyFile(src, dst string) error {
	if fi, err := os.Lstat(src); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	data, err := os.ReadFile(src) //nolint:gosec // a file of the template directory the caller names
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

// stateRoot is the absolute dir that holds dev's process dir: the --persist-to dir under --persist.
func stateRoot(processDir string) string {
	abs, err := filepath.Abs(filepath.Dir(processDir))
	if err != nil {
		return filepath.Dir(processDir)
	}
	return abs
}

// skipped reports whether path is dev's persist dir, which dev skips inside the template directory.
func (s *devAppSession) skipped(path string) bool {
	if s.persist == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	return err == nil && abs == s.persist
}

// probeImages renders t once with a stand-in lock whose digest names each image, and returns the images the App's
// parts use, each with its part's kind, that lock (the tag each image is pushed under, and a digest to replace) and the
// stand-in App.
func probeImages(t *template.Template, in template.RenderInput) (map[string]*devImage, map[string]template.LockedImage, *v1.App, error) {
	lock := make(map[string]template.LockedImage, len(t.Images))
	byDigest := make(map[string]string, len(t.Images))
	for key, entry := range t.Images {
		sum := sha256.Sum256([]byte("funcdctl-dev:" + key))
		digest := fmt.Sprintf("sha256:%x", sum)
		tag := t.Parsed[key].Exact
		if tag == "" {
			tag = "0.0.0-dev"
		}
		lock[key] = template.LockedImage{Requested: entry, Version: tag, Digest: digest}
		byDigest[digest] = key
	}
	t.Lock = lock
	app, err := template.Render(t, in)
	if err != nil {
		return nil, nil, nil, err
	}
	used := map[string]*devImage{}
	note := func(image, part string, rt v1.RuntimeName) {
		_, digest, ok := strings.Cut(image, "@")
		if key, found := byDigest[digest]; ok && found && used[key] == nil {
			used[key] = &devImage{part: part, runtime: rt}
		}
	}
	for _, f := range app.Spec.Functions {
		note(f.Image, partFunction, f.Runtime)
	}
	for _, w := range app.Spec.Workflows {
		for _, st := range w.Steps {
			if st.Function != nil {
				note(st.Function.Image, partStep, "")
			}
		}
	}
	for _, si := range app.Spec.Sites {
		note(si.Image, partSite, "")
	}
	return used, lock, app, nil
}

// buildFlags splits --build into the folders each image key is looked up in and the key=path entries.
func (s *devAppSession) buildFlags() (dirs []string, paths map[string]string) {
	paths = map[string]string{}
	for _, b := range s.cfg.builds {
		if key, path, ok := strings.Cut(b, "="); ok {
			paths[key] = path
			continue
		}
		dirs = append(dirs, b)
	}
	return dirs, paths
}

// resolveImage finds the build of image key: the path a --build key=path names, else the first --build folder that
// holds it by standalone dev's rules (ADR-0247 Decision 6: a <key>.mjs, a dist/<key>.mjs or a python entry). A Site's
// folder is named by key=path only, since a folder named after it usually holds the frontend's sources.
func (s *devAppSession) resolveImage(op, key string, img *devImage) error {
	dirs, paths := s.buildFlags()
	if p, ok := paths[key]; ok {
		if img.part == partSite {
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
				return fault.Invalidf(op, "--build %s=%s: a Site's build is a folder", key, p)
			}
			img.site = p
			return nil
		}
		b, err := buildAt(op, key, p)
		if err != nil {
			return err
		}
		img.build = b
		return nil
	}
	if img.part == partSite {
		return fault.Invalidf(op, "images.%s is a Site's image: name its build folder with --build %s=<dir>", key, key)
	}
	for _, d := range dirs {
		f := &devFolder{dir: d, stamps: map[string]string{}}
		py, err := f.pyprojectEntries(op)
		if err != nil {
			return err
		}
		python := strings.HasPrefix(string(img.runtime), "python")
		if img.part == partStep {
			python = f.isPythonStep(key, py)
		}
		if !f.holdsBuild(key, python, py) {
			continue
		}
		b, err := f.resolveBuild(op, key, python, py)
		if err != nil {
			return err
		}
		img.build = b
		return nil
	}
	return fault.Invalidf(op, "no build of images.%s in the --build folders %s; name it with --build %s=<path>",
		key, strings.Join(dirs, ", "), key)
}

// holdsBuild reports whether the folder holds name's build by resolveBuild's rules.
func (f *devFolder) holdsBuild(name string, python bool, pyEntries map[string]string) bool {
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if !python {
		return exists(filepath.Join(f.dir, name+".mjs")) || exists(filepath.Join(f.dir, "dist", name+".mjs"))
	}
	return pyEntries[name] != "" || exists(filepath.Join(f.dir, name+".py")) || f.isSrcMain(name, pyEntries)
}

// buildAt is the build a --build key=path names: a file with its contract file beside it, or a bundle directory with
// its contract file at its root, as funcdctl push reads them (ADR-0247 Decision 2).
func buildAt(op, key, path string) (devBuild, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return devBuild{}, fault.Invalidf(op, "--build %s=%s: %v", key, path, err)
	}
	dir := path
	if !fi.IsDir() {
		dir = filepath.Dir(path)
	}
	c, contractPath, err := sdk.LoadContract(dir, sdk.ContractName(path, fi.IsDir()))
	if err != nil {
		return devBuild{}, err
	}
	if fi.IsDir() {
		return devBuild{name: key, contract: c, contractPath: contractPath, srcDir: path, entry: pushEntryFor("", c.Runtime), inPlace: true}, nil
	}
	return devBuild{name: key, contract: c, contractPath: contractPath, srcDir: dir, entry: filepath.Base(path)}, nil
}

// push pushes img's build to dev's layout as <repo>:<tag> unless its fingerprint is the one last pushed.
func (s *devAppSession) push(ctx context.Context, op, key, repo, tag string, img *devImage) error {
	var fp string
	var err error
	if img.part == partSite {
		fp, err = treeStamp(img.site)
	} else {
		fp, err = img.build.fingerprint(nil)
	}
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "fingerprint the build of images.%s", key)
	}
	if prev := s.images[key]; prev != nil && prev.print == fp && prev.digest != "" {
		img.print, img.digest = prev.print, prev.digest
		return nil
	}
	ref := "oci-layout://" + filepath.Join(s.layout, repo) + ":" + tag
	start := time.Now()
	var digest string
	switch {
	case img.part == partSite:
		digest, err = artifact.PushSite(ctx, ref, img.site)
	case img.build.inPlace:
		digest, err = s.pushStaged(ctx, op, ref, img.build)
	default:
		digest, err = pushFile(ctx, op, ref, img.build)
	}
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "push images.%s", key)
	}
	img.print, img.digest = fp, digest
	_, werr := fmt.Fprintf(s.out, "pushed images.%s as %s:%s@%s in %s\n", key, repo, tag, shortDigest(digest),
		time.Since(start).Round(time.Millisecond))
	return werr
}

// pushFile pushes a single-file build with its gated contract (as funcdctl push does).
func pushFile(ctx context.Context, op, ref string, b devBuild) (string, error) {
	input, output, err := gateContract(op, b.contract)
	if err != nil {
		return "", err
	}
	var blob []byte
	if input != nil {
		if blob, err = artifact.ContractBlob(input, output); err != nil {
			return "", err
		}
	}
	return artifact.Push(ctx, ref, filepath.Join(b.srcDir, b.entry), blob, string(b.contract.Runtime), "")
}

// pushStaged pushes a directory build as a bundle from a copy, so its compiled contract never lands in the sources.
func (s *devAppSession) pushStaged(ctx context.Context, op, ref string, b devBuild) (string, error) {
	input, output, err := gateContract(op, b.contract)
	if err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(s.stage, b.name+"-")
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create a stage for %s", b.name)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := copyBuildTree(b.srcDir, stage); err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "copy the build of %s", b.name)
	}
	if input == nil {
		return artifact.PushBundleNoContract(ctx, ref, stage, b.entry, string(b.contract.Runtime), "")
	}
	if err := writeBundleContract(op, stage, input, output); err != nil {
		return "", err
	}
	return artifact.PushBundle(ctx, ref, stage, b.entry, string(b.contract.Runtime), "")
}

// skipBuildEntry reports whether a build walk leaves name out: dot-entries, __pycache__, node_modules and the
// compiled contract a push writes.
func skipBuildEntry(name string) bool {
	return strings.HasPrefix(name, ".") || name == "__pycache__" || name == "node_modules" || name == bundleContractName
}

// copyBuildTree copies every regular file of src that a build walk keeps into dst.
func copyBuildTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != src && skipBuildEntry(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p) //nolint:gosec // the build the user points dev at
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

// treeStamp digests the relative path, size and mtime of every file a build walk keeps under dir.
func treeStamp(dir string) (string, error) {
	return (&devAppSession{}).treeStamp(dir)
}

// treeStamp is the package treeStamp that also leaves out dev's persist dir.
func (s *devAppSession) treeStamp(dir string) (string, error) {
	sum := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an entry removed mid-walk changes the stamp on the next poll
		}
		if p != dir && (skipBuildEntry(d.Name()) || d.IsDir() && s.skipped(p)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fi, ierr := d.Info(); ierr == nil && fi.Mode().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			_, _ = fmt.Fprintf(sum, "%s\x00%s\n", rel, infoStamp(fi))
		}
		return nil
	})
	return fmt.Sprintf("%x", sum.Sum(nil)), err
}

// inputsStamp digests every input a reload reads: the template directory, the values and --apply files, and the
// --build folders and paths. Dev's persist dir is left out, as dev writes it.
func (s *devAppSession) inputsStamp() (string, error) {
	sum := sha256.New()
	roots := slices.Concat([]string{s.dir}, s.cfg.values, s.cfg.apply)
	dirs, paths := s.buildFlags()
	roots = append(roots, dirs...)
	keys := make([]string, 0, len(paths))
	for key := range paths {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		roots = append(roots, paths[key])
	}
	for _, r := range roots {
		fi, err := os.Stat(r)
		switch {
		case err != nil:
			_, _ = fmt.Fprintf(sum, "%s\x00missing\n", r)
		case fi.IsDir():
			st, terr := s.treeStamp(r)
			if terr != nil {
				return "", terr
			}
			_, _ = fmt.Fprintf(sum, "%s\x00%s\n", r, st)
		default:
			_, _ = fmt.Fprintf(sum, "%s\x00%s\n", r, infoStamp(fi))
		}
	}
	return fmt.Sprintf("%x", sum.Sum(nil)), nil
}

// platformFolder is the devFolder devPlatformOptions reads for app: the template directory, the --apply documents and
// a CatalogService per catalogs entry (which wire the SQL and catalog engines as in folder mode, Decision 5), and a
// Function per function part and per step that dev built (which pick the shims).
func (s *devAppSession) platformFolder(app *v1.App, docs []v1.Object) *devFolder {
	byDigest := map[string]*devImage{}
	for _, img := range s.images {
		byDigest[img.digest] = img
	}
	imageOf := func(image string) *devImage {
		_, digest, _ := strings.Cut(image, "@")
		return byDigest[digest]
	}
	f := &devFolder{dir: s.dir}
	for _, obj := range docs {
		f.resources = append(f.resources, devDoc{obj: obj})
	}
	for _, e := range app.Spec.Catalogs {
		if e.Name != "" {
			cs := &v1.CatalogService{Spec: e.CatalogServiceSpec}
			cs.Name = e.Name
			f.resources = append(f.resources, devDoc{obj: cs})
		}
	}
	for _, part := range app.Spec.Functions {
		if img := imageOf(part.Image); img != nil && part.Ref == "" {
			fn := &v1.Function{Spec: part.FunctionSpec}
			fn.Name = part.Name
			f.functions = append(f.functions, &devFunction{fn: fn, build: img.build})
		}
	}
	for _, w := range app.Spec.Workflows {
		for i, st := range w.Steps {
			if st.Function == nil {
				continue
			}
			if img := imageOf(st.Function.Image); img != nil {
				f.steps = append(f.steps, &devStep{index: i, name: v1.ObjectName(string(w.Name) + "-" + string(st.Name)), build: img.build})
			}
		}
	}
	return f
}

// devAppSecrets is each Secret app declares, each key from the shell, else the envFile, else dev.env, as standalone dev
// creates a Function's (ADR-0254 Decision 5), tagged as dev's. A key none holds stops dev, naming the three places.
func devAppSecrets(op string, app *v1.App, src devSecretSources) ([]v1.Object, error) {
	var out []v1.Object
	for _, decl := range app.Spec.Secrets {
		data := make(map[string][]byte, len(decl.Keys))
		for _, k := range decl.Keys {
			v, ok := src.lookup(k)
			if !ok {
				return nil, src.missing(op, "App "+string(app.Name), string(decl.Name), k)
			}
			data[k] = []byte(v)
		}
		obj, _ := v1.NewObject(v1.KindSecret)
		sec := obj.(*v1.Secret)
		sec.Name, sec.Namespace, sec.ResourceGroup = decl.Name, app.Namespace, app.ResourceGroup
		sec.Tags = v1.Tags{devManagedTag: devManagedBy}
		sec.Spec.Data = data
		out = append(out, sec)
	}
	return out, nil
}

// withoutDefined is app with only the Secret declarations no --apply document defines: dev leaves those to the
// document (Decision 6).
func withoutDefined(app *v1.App, docs []v1.Object) *v1.App {
	out := *app
	out.Spec.Secrets = slices.DeleteFunc(slices.Clone(app.Spec.Secrets), func(decl v1.SecretDeclaration) bool {
		return slices.ContainsFunc(docs, func(o v1.Object) bool {
			return o.GroupVersionKind().Kind == v1.KindSecret && o.GetName() == decl.Name
		})
	})
	return &out
}

// appKeypairName is the name dev derives the S3 keypair over for app (Decision 11): its first function part, else
// devAppKeypairName.
func appKeypairName(app *v1.App) string {
	for _, part := range app.Spec.Functions {
		if part.Name != "" {
			return string(part.Name)
		}
	}
	return devAppKeypairName
}

// appDevEnvName is the keypair name of the App template at dir (Decision 11), from the stand-in render with cfg's
// values files: no build is read and no platform starts.
func appDevEnvName(op, dir string, cfg devConfig) (string, error) {
	stage, err := os.MkdirTemp("", "funcdctl-dev-")
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "create a template snapshot")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	processDir, err := devStateDir(cfg, dir)
	if err != nil {
		return "", err
	}
	s := &devAppSession{dir: dir, cfg: cfg, stage: stage, persist: stateRoot(processDir)}
	t, err := s.loadTemplate(op)
	if err != nil {
		return "", err
	}
	values, err := readValues(cfg.values)
	if err != nil {
		return "", err
	}
	t.Registry = "oci-layout://" + filepath.Join(stage, "oci")
	_, _, app, err := probeImages(t, template.RenderInput{Namespace: devNamespace, Values: values})
	if err != nil {
		return "", err
	}
	return appKeypairName(app), nil
}

// apply applies d in order: the --apply documents as folder mode applies a folder, then the Secrets and the App. Once
// it all applied, it deletes what the last apply held that d no longer does (Decision 7).
func (s *devAppSession) apply(ctx context.Context, op string, c *sdk.Client, d devAppDesired) (devApply, error) {
	if len(d.docs) > 0 {
		if err := applyOrdered(ctx, op, c, d.docs); err != nil {
			return devApply{}, err
		}
	}
	r, err := applyDevApp(ctx, c, d.app, d.secrets)
	if err != nil {
		return devApply{}, err
	}
	s.applied = pruneRemoved(ctx, c, s.applied, slices.Concat(d.docs, d.secrets), true, s.logger)
	return r, nil
}

// devApply is what applyDevApp did: whether it applied the App, and the revision number and Ready line a follow of
// its new revision starts from.
type devApply struct {
	applied bool
	n0      int64
	ready   string
}

// applyDevApp applies the Secrets, then the App unless the stored App already holds its spec.
func applyDevApp(ctx context.Context, c *sdk.Client, app *v1.App, secrets []v1.Object) (devApply, error) {
	for _, sec := range secrets {
		if err := applyDesired(ctx, c, sec); err != nil {
			return devApply{}, err
		}
	}
	for attempt := 1; ; attempt++ {
		stored, err := storedApp(ctx, c, app.Namespace, app.Name)
		if err != nil {
			return devApply{}, err
		}
		var r devApply
		if stored != nil {
			app.ResourceGroup, r.ready = stored.ResourceGroup, readyLine(stored)
			if r.n0, err = revisionNumber(stored); err != nil {
				return devApply{}, err
			}
			app.Spec.Paused = stored.Spec.Paused
			same, serr := sameAppSpec(stored.Spec, app.Spec)
			if serr != nil || same {
				return devApply{}, serr
			}
			app.ResourceVersion = stored.ResourceVersion
		}
		_, err = c.Apply(ctx, app)
		if fault.KindOf(err) == fault.Conflict && attempt < applyAttempts {
			continue
		}
		r.applied = err == nil
		return r, err
	}
}

// follow follows app's new revision in the background and cancels the follow of the one before: the App reconciler
// switches only its latest revision. A revision that fails is reported and dev keeps running, so its author can save a
// fix or run funcdctl app retry.
func (s *devAppSession) follow(ctx context.Context, a *cli, c *sdk.Client, app *v1.App, r devApply, done func() string) {
	if s.cancelFollow != nil {
		s.cancelFollow()
	}
	fctx, cancel := context.WithCancel(ctx)
	s.cancelFollow = cancel
	go func() {
		if err := a.waitDeployEvery(fctx, c, app, r.n0, r.ready, devFollowPoll); err != nil {
			if fctx.Err() == nil {
				s.logger.Warn("the new revision did not become current; save a fix, or run funcdctl app retry", "error", err)
			}
			return
		}
		_ = a.writef("%s\n", done())
	}()
}

// watch polls every input at devReloadPoll (ADR-0247 Decision 8's polling) and redeploys the App when one changed; a
// failed reload is reported and the last App keeps running. Each new revision is followed in the background, and a
// newer one cancels that follow: the App reconciler switches only its latest revision, so a fix saved while a broken
// revision waits rolls at once.
func (s *devAppSession) watch(ctx context.Context, op string, a *cli, c *sdk.Client, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(devReloadPoll)
	defer t.Stop()
	defer func() {
		if s.cancelFollow != nil {
			s.cancelFollow()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// The config file is an input too (ADR-0254 Decision 6): its build, values, apply or env, or its envFile's
		// contents, redeploy; another key asks for a restart.
		forced := s.cfg.file.reloadFile(op, &s.cfg.secrets, s.logger.Warn)
		if forced {
			s.cfg.file.mergeAppKeys(&s.cfg)
		}
		stamp, err := s.inputsStamp()
		if err != nil || (stamp == s.stamp && !forced) {
			continue
		}
		s.stamp = stamp
		start := time.Now()
		var r devApply
		d, err := s.prepare(ctx, op)
		if err == nil {
			r, err = s.apply(ctx, op, c, d)
		}
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("reload failed; the last App keeps running", "error", err)
			}
			continue
		}
		if !r.applied {
			continue
		}
		s.follow(ctx, a, c, d.app, r, func() string { return fmt.Sprintf("reloaded in %s", time.Since(start).Round(time.Millisecond)) })
	}
}

// appRevision is the App's latest revision as the control plane stores it, for the banner (Decision 11).
func (d *devInstance) appRevision() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stored, err := storedApp(ctx, d.client, d.app.Namespace, d.app.Name)
	switch {
	case err != nil || stored == nil:
		return "no revision yet"
	case stored.Status.CurrentRevision != "":
		return "revision " + string(stored.Status.CurrentRevision)
	case stored.Status.LatestRevision != "":
		return "revision " + string(stored.Status.LatestRevision) + " (rolling out)"
	}
	return "no revision yet"
}

// appEndpoints are the banner's lines for the App: each Route's paths and each Site's URL on the gateway.
func (d *devInstance) appEndpoints() [][2]string {
	url := func(host, path string) string {
		if u, err := neturl.Parse(d.gatewayURL); err == nil && host != "" {
			return "http://" + net.JoinHostPort(host, u.Port()) + path
		}
		return d.gatewayURL + path
	}
	var out [][2]string
	for _, r := range d.app.Spec.Routes {
		for _, rule := range r.Rules {
			out = append(out, [2]string{"route", url(r.Host, rule.Path)})
		}
	}
	for _, si := range d.app.Spec.Sites {
		if si.Name == "" {
			continue
		}
		out = append(out, [2]string{"site", url(si.Ingress.Host, v1.IngressPath(&si.Ingress.Ingress, v1.KindSite, si.Name))})
	}
	return out
}

// shortDigest is a digest cut to its algorithm and first 12 hex digits, for a log line.
func shortDigest(d string) string {
	if len(d) > len("sha256:")+12 {
		return d[:len("sha256:")+12]
	}
	return d
}
