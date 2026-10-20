//go:build dev

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
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

// App mode (#1067, an experiment without an ADR): `funcdctl dev <template dir>` deploys an App template on the embedded
// platform from local builds. Dev pushes each image's build to an OCI layout in its state dir, renders the template
// with that layout as its registry and a lock of those pushes, and applies the App as `app deploy` does. The App
// reconciler then writes the parts, stores, Routes and Sites, and stamps a revision per change.

// Part kinds an image serves.
const (
	partFunction = "function"
	partStep     = "step"
	partSite     = "site"
)

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
	dir    string // the template directory
	cfg    devConfig
	layout string // the OCI layout each build is pushed to, one repository per image
	stage  string // where a build directory is copied before its push
	images map[string]*devImage
	stamp  string // the stamp of every input at the last reload
	out    io.Writer
	// cancelFollow stops the follow of the revision applied last; the boot sets it first, then only the watcher.
	cancelFollow context.CancelFunc
}

// startDevApp boots the embedded platform for the App template at dir and deploys it from local builds.
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
		images: map[string]*devImage{}, out: a.out}
	for _, d := range []string{s.layout, s.stage} {
		if merr := os.MkdirAll(d, 0o700); merr != nil {
			return nil, fault.Wrapf(merr, fault.Internal, op, "create %s", d)
		}
	}
	if s.stamp, err = s.inputsStamp(); err != nil {
		return nil, err
	}
	app, secrets, err := s.prepare(ctx, op)
	if err != nil {
		return nil, err
	}
	if len(app.Spec.SQL) > 0 && !sqlembedengine.Bundled() {
		return nil, fault.Unavailablef(op, "App %s declares a SQL database, but this funcdctl has no SQL engine", app.Name)
	}
	folder := s.platformFolder(app)
	inst := &devInstance{runErr: make(chan error, 1), logger: logger, functions: folder.names(), workflows: folder.workflowNames(),
		runtimes: &devRuntimes{byImage: map[string]v1.RuntimeName{}}, artifactDir: filepath.Join(root, "artifacts")}
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
	r, err := applyDevApp(ctx, client, app, secrets)
	if err != nil {
		return nil, err
	}
	deployed := func() string {
		return fmt.Sprintf("App %s deployed in %s", app.Name, time.Since(start).Round(time.Millisecond))
	}
	if r.applied {
		s.follow(ctx, a, client, app, r, deployed, logger)
	} else if err = a.writef("%s\n", deployed()); err != nil {
		return nil, err
	}
	inst.watchDone = make(chan struct{})
	go s.watch(ctx, op, a, client, logger, inst.watchDone)
	return inst, nil
}

// prepare loads the template and the values, pushes each build that changed since its last push, and renders the App
// with dev's layout as its registry and a lock of those pushes. The App's hooks stay only under --hooks; an App that
// requires another is refused. It also returns the Secrets the App declares, from the environment.
func (s *devAppSession) prepare(ctx context.Context, op string) (*v1.App, []v1.Object, error) {
	t, err := template.Load(s.dir)
	if err != nil {
		return nil, nil, err
	}
	values, err := readValues(s.cfg.values)
	if err != nil {
		return nil, nil, err
	}
	in := template.RenderInput{Namespace: devNamespace, Values: values}
	t.Registry = "oci-layout://" + s.layout
	used, lock, err := probeImages(t, in)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(used))
	for key := range used {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		img := used[key]
		if err := s.resolveImage(op, key, img); err != nil {
			return nil, nil, err
		}
		l := lock[key]
		if err := s.push(ctx, op, key, t.Parsed[key].Repo, l.Version, img); err != nil {
			return nil, nil, err
		}
		l.Digest = img.digest
		lock[key] = l
		s.images[key] = img
	}
	t.Lock = lock
	app, err := template.Render(t, in)
	if err != nil {
		return nil, nil, err
	}
	if len(app.Spec.Requires) > 0 {
		r := app.Spec.Requires[0]
		return nil, nil, fault.Invalidf(op, "App %s requires App %s %s: funcdctl dev does not run an App that requires another yet",
			app.Name, r.App, r.Version)
	}
	if !s.cfg.hooks {
		app.Spec.Hooks = nil
	}
	secrets, err := devAppSecrets(op, app)
	if err != nil {
		return nil, nil, err
	}
	return app, secrets, nil
}

// probeImages renders t once with a stand-in lock whose digest names each image, and returns the images the App's
// parts use, each with its part's kind, and that lock (the tag each image is pushed under, and a digest to replace).
func probeImages(t *template.Template, in template.RenderInput) (map[string]*devImage, map[string]template.LockedImage, error) {
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
		return nil, nil, err
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
	return used, lock, nil
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
	sum := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an entry removed mid-walk changes the stamp on the next poll
		}
		if p != dir && skipBuildEntry(d.Name()) {
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

// inputsStamp digests every input a reload reads: the template directory, the values files, and the --build folders
// and paths.
func (s *devAppSession) inputsStamp() (string, error) {
	sum := sha256.New()
	roots := append([]string{s.dir}, s.cfg.values...)
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
			st, terr := treeStamp(r)
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

// platformFolder is the devFolder devPlatformOptions reads for app: the template directory and a Function per function
// part and per step that dev built, which pick the shims, the catalog engine and the S3 keypair.
func (s *devAppSession) platformFolder(app *v1.App) *devFolder {
	byDigest := map[string]*devImage{}
	for _, img := range s.images {
		byDigest[img.digest] = img
	}
	imageOf := func(image string) *devImage {
		_, digest, _ := strings.Cut(image, "@")
		return byDigest[digest]
	}
	f := &devFolder{dir: s.dir}
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

// devAppSecrets is each Secret app declares, each key from the environment variable of that name, as standalone dev
// creates a Function's (ADR-0247 Decision 5). A missing variable stops dev, naming it.
func devAppSecrets(op string, app *v1.App) ([]v1.Object, error) {
	var out []v1.Object
	for _, decl := range app.Spec.Secrets {
		data := make(map[string][]byte, len(decl.Keys))
		for _, k := range decl.Keys {
			v, ok := os.LookupEnv(k)
			if !ok {
				return nil, fault.Invalidf(op, "App %s declares Secret %s with key %s; set the environment variable %s",
					app.Name, decl.Name, k, k)
			}
			data[k] = []byte(v)
		}
		obj, _ := v1.NewObject(v1.KindSecret)
		sec := obj.(*v1.Secret)
		sec.Name, sec.Namespace, sec.ResourceGroup = decl.Name, app.Namespace, app.ResourceGroup
		sec.Spec.Data = data
		out = append(out, sec)
	}
	return out, nil
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
func (s *devAppSession) follow(ctx context.Context, a *cli, c *sdk.Client, app *v1.App, r devApply, done func() string, logger *slog.Logger) {
	if s.cancelFollow != nil {
		s.cancelFollow()
	}
	fctx, cancel := context.WithCancel(ctx)
	s.cancelFollow = cancel
	go func() {
		if err := a.waitDeploy(fctx, c, app, r.n0, r.ready); err != nil {
			if fctx.Err() == nil {
				logger.Warn("the new revision did not become current; save a fix, or run funcdctl app retry", "error", err)
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
func (s *devAppSession) watch(ctx context.Context, op string, a *cli, c *sdk.Client, logger *slog.Logger, done chan<- struct{}) {
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
		stamp, err := s.inputsStamp()
		if err != nil || stamp == s.stamp {
			continue
		}
		s.stamp = stamp
		start := time.Now()
		var r devApply
		app, secrets, err := s.prepare(ctx, op)
		if err == nil {
			r, err = applyDevApp(ctx, c, app, secrets)
		}
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("reload failed; the last App keeps running", "error", err)
			}
			continue
		}
		if !r.applied {
			continue
		}
		s.follow(ctx, a, c, app, r, func() string { return fmt.Sprintf("reloaded in %s", time.Since(start).Round(time.Millisecond)) }, logger)
	}
}

// shortDigest is a digest cut to its algorithm and first 12 hex digits, for a log line.
func shortDigest(d string) string {
	if len(d) > len("sha256:")+12 {
		return d[:len("sha256:")+12]
	}
	return d
}
