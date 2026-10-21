//go:build dev

// dev.go is the `-tags dev` implementation of `funcdctl dev` (ADR-0125, ADR-0247): run a folder's resource files
// locally, from their builds. It embeds the real platform via `funcd.InMemory()` (memory store/KV, mem:// blob,
// in-proc NATS, the PROCESS runtime) and drives the real reconcile → materialize → shim → invoke path; only the
// driver implementations are the in-memory ones (ADR-0125, "reuse don't rebuild").
//
// Dev applies the files production applies (ADR-0247 Decision 4) and changes only what Decision 5 lists: each
// Function runs its local build with one replica, each Workflow image step runs a copy of the build its tag stem
// names through the real workflow materializer (Decision 7), and every object lands in namespace default, tagged as
// dev's. It creates the Secrets a Function declares that no document defines, from the environment, and the
// write-only Role and one RolesAssignment per Function and step (ADR-0128 Decision 2). It synthesizes nothing else.
//
// Around that core: the ADR-0080/0085 S3 frontend over the blob substrate, `--persist` durable drivers (ADR-0125
// Decision 7), the embedded process-mode DuckDB+Quack catalog engine and rqlite SQL engine, a services banner,
// colorized function log streaming, fixed ports, and a polling reload (ADR-0247 Decision 8).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	shimpython "github.com/pyvvo/funcd-python/shim"
	shimnode "github.com/pyvvo/funcd-typescript/shim"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/catalog/devengine"
	"github.com/pyvvo/funcd/internal/catalog/embedengine"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/kvstore"
	kvbadger "github.com/pyvvo/funcd/internal/kvstore/badger"
	"github.com/pyvvo/funcd/internal/platform/observability"
	"github.com/pyvvo/funcd/internal/platform/stopsignal"
	"github.com/pyvvo/funcd/internal/runtime/process"
	sqldevengine "github.com/pyvvo/funcd/internal/sql/devengine"
	sqlembedengine "github.com/pyvvo/funcd/internal/sql/embedengine"
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// devCmd runs a folder's resource files locally, from their builds (ADR-0125, ADR-0247). Only compiled under
// `-tags dev` (the release build gets the rebuild-hint stub in dev_stub.go), so the platform embed never fattens the
// release `funcdctl`.
func (a *cli) devCmd() *cobra.Command {
	var cfg devConfig
	cmd := &cobra.Command{
		Use:   "dev [dir]",
		Short: "Run a folder's resource files, or an App template, locally from their builds on the embedded platform",
		Long: "funcdctl dev runs a folder's resource files from their local builds (ADR-0247). A directory that holds app.yaml " +
			"beside resources/ runs as an App template instead (ADR-0253): dev pushes each image's build, renders the " +
			"template as funcdctl app deploy does and applies the App, then again on each save.\n\n" +
			"A " + devConfigFile + " in the current directory holds what dev is run with (ADR-0254): its dev keys are these " +
			"flags in camelCase (dir, values, build, hooks, apply, persist, persistTo, gport, s3port, cport), plus env and " +
			"envFile for the values of a declared Secret no document defines, and platform for runtime.handOutSettle, " +
			"eventing.blobPollInterval and funclog.segmentMaxAge. A flag given here replaces its key, and a dir argument " +
			"replaces dev.dir.\n\n" +
			"For an App template the file also takes watch, a list of name, run and dir: dev starts each command, waits " +
			"for the builds they write and keeps their output in log files; and liveReload: true, which reloads open " +
			"Site pages after a Site change (ADR-0255).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, _, rerr := resolveDevRun("funcdctl dev", ".", args, cmd.Flags(), &cfg)
			if rerr != nil {
				return rerr
			}
			// --print-env: emit the dev S3 creds as `export …` lines and exit, no server (for
			// `eval "$(funcdctl dev <dir> --s3port <p> --print-env)"`).
			if cfg.printEnv {
				return a.printDevEnv(dir, cfg)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), stopsignal.Signals()...)
			defer stop()

			inst, err := a.startDev(ctx, dir, cfg)
			if err != nil {
				// A stop asked for during the start ends dev as one after it does (#1143); startDev has undone the start.
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if werr := a.printBanner(inst); werr != nil {
				return werr
			}
			<-ctx.Done()
			return inst.stop()
		},
	}
	cmd.Flags().BoolVar(&cfg.persist, "persist", false,
		"persist KV/blob/metastore to a local dir between runs (default: ephemeral in-memory)")
	cmd.Flags().StringVar(&cfg.persistTo, "persist-to", devPersistDir,
		"directory for --persist state (per-service subdirs: metastore/kv/blob)")
	cmd.Flags().IntVar(&cfg.gport, "gport", 0,
		"fixed gateway (function-invoke) port; 0 ⇒ a random free port")
	cmd.Flags().IntVar(&cfg.s3port, "s3port", 0,
		"fixed S3-frontend port; 0 ⇒ a random free port")
	cmd.Flags().IntVar(&cfg.cport, "cport", 0,
		"fixed control-plane port (for `funcdctl --server`: apply, workflow run); 0 ⇒ a random free port")
	cmd.Flags().StringArrayVarP(&cfg.values, "values", "f", nil,
		"an App template's values file; repeat it to merge files in order (App mode)")
	cmd.Flags().StringArrayVar(&cfg.builds, "build", nil,
		"an App's builds: a folder each image key is looked up in, or <key>=<path> (App mode; repeatable)")
	cmd.Flags().BoolVar(&cfg.hooks, "hooks", false,
		"keep the App's preApply and postApply hooks, called on each new revision (App mode)")
	cmd.Flags().StringArrayVar(&cfg.apply, "apply", nil,
		"a file of resource documents applied before the App, again when it changes (App mode; repeatable)")
	cmd.Flags().BoolVar(&cfg.printEnv, "print-env", false,
		"print the dev S3 credentials as `export …` lines and exit (no server); pair with a fixed --s3port. "+
			"Use: eval \"$(funcdctl dev <dir> --s3port 3006 --print-env)\"")
	return cmd
}

// devConfig carries the `funcdctl dev` flags that shape the substrate (ADR-0125 Decision 7). The zero value is the
// ephemeral in-memory default; persist opts into durable-local drivers.
type devConfig struct {
	persist   bool   // swap memory store/KV/blob for durable-local Badger + fileblob
	persistTo string // the --persist state dir (empty ⇒ devPersistDir)
	gport     int    // fixed gateway (data-plane) port; 0 ⇒ a random free port
	s3port    int    // fixed S3-frontend port; 0 ⇒ a random free port
	cport     int    // fixed control-plane port (apply / workflow run); 0 ⇒ a random free port
	printEnv  bool   // print the dev S3 creds as `export …` lines and exit (no server)
	// App mode (ADR-0253): the template's values files, where its builds are, whether its hooks stay, and the files of
	// resource documents applied beside it.
	values []string
	builds []string
	hooks  bool
	apply  []string
	// pickS3Addr picks the S3-frontend address when s3port is 0; nil ⇒ freeLocalAddr. A test hands out a taken one (#627).
	pickS3Addr func() (string, error)
	// cacheDir holds the state dir without --persist (ADR-0167); "" ⇒ os.UserCacheDir. A test passes its own.
	cacheDir string
	pace     devPace          // the three cadences (ADR-0254 Decision 4); zero ⇒ dev's defaults
	secrets  devSecretSources // where a declared Secret no document defines takes its values (ADR-0254 Decision 5)
	file     *devFileState    // funcdctl.config.yaml as read at start, nil without one; the reload re-reads it
	// ADR-0255: the watch commands dev supervises, each dir made absolute from the file's, and Site live reload.
	watch      []devWatchEntry
	liveReload bool
}

// startDev is the entry seam tests drive (no signal handling, no blocking): it reads dir (ADR-0247 Decision 4), boots
// the embedded platform, applies the folder as Decision 5 changes it and starts serving. It returns once the platform
// is running (resources reconcile asynchronously to Ready); the caller cancels ctx to stop.
func (a *cli) startDev(ctx context.Context, dir string, cfg devConfig) (*devInstance, error) {
	const op = "funcdctl dev"
	lg, err := observability.NewLogger(observability.Config{Format: observability.FormatText}, a.out)
	if err != nil {
		return nil, err
	}
	if cfg.pace == (devPace{}) {
		cfg.pace = defaultDevPace()
	}
	if isAppTemplate(dir) {
		return a.startDevApp(ctx, dir, cfg, lg.Root())
	}
	if len(cfg.values) > 0 || len(cfg.builds) > 0 || cfg.hooks || len(cfg.apply) > 0 {
		return nil, fault.Invalidf(op, "-f, --build, --hooks and --apply run an App template; %s holds no app.yaml beside resources/", dir)
	}
	folder, err := loadDevFolder(op, dir)
	if err != nil {
		return nil, err
	}
	return a.bootDev(ctx, op, folder, cfg, lg.Root())
}

// printDevEnv prints the dev S3 credentials of the folder's first Function (or step), or of an App template's keypair
// name (appKeypairName), as `export …` lines and returns;
// NO server is booted. The keypair is the FIXED devS3Master derived over that function's identity (the banner's;
// deterministic across restarts, ADR-0128 Decision 6). The endpoint port is --s3port (default 3006).
func (a *cli) printDevEnv(dir string, cfg devConfig) error {
	const op = "funcdctl dev --print-env"
	var name string
	if isAppTemplate(dir) {
		n, err := appDevEnvName(op, dir, cfg)
		if err != nil {
			return err
		}
		name = n
	} else {
		folder, err := loadDevFolder(op, dir)
		if err != nil {
			return err
		}
		names := folder.names()
		if len(names) == 0 {
			return fault.NotFoundf(op, "%s holds no Function or Workflow image step", dir)
		}
		name = names[0]
	}
	kp := s3gateway.DeriveKeypair([]byte(devS3Master), v1.KindFunction, devNamespace, name)
	port := cfg.s3port
	if port == 0 {
		port = 3006
	}
	_, werr := fmt.Fprintf(a.out,
		"export AWS_ACCESS_KEY_ID=%s\nexport AWS_SECRET_ACCESS_KEY=%s\nexport AWS_REGION=%s\nexport AWS_ENDPOINT_URL_S3=http://127.0.0.1:%d\n",
		kp.AccessKey, kp.SecretKey, devS3Region, port)
	return werr
}

// names are the Functions dev runs, in folder order: each Function, then each Workflow image step's.
func (f *devFolder) names() []string {
	var out []string
	for _, fn := range f.functions {
		out = append(out, string(fn.fn.Name))
	}
	for _, st := range f.steps {
		out = append(out, string(st.name))
	}
	return out
}

// workflowNames are the folder's Workflows.
func (f *devFolder) workflowNames() []string {
	var out []string
	for _, d := range f.resources {
		if d.obj.GroupVersionKind().Kind == v1.KindWorkflow {
			out = append(out, string(d.obj.GetName()))
		}
	}
	return out
}

// catalogNames are the folder's CatalogServices, which the embedded duckdb+quack engine serves (ADR-0125 Decision 5).
func (f *devFolder) catalogNames() []string {
	var out []string
	for _, d := range f.resources {
		if d.obj.GroupVersionKind().Kind == v1.KindCatalogService {
			out = append(out, string(d.obj.GetName()))
		}
	}
	sort.Strings(out)
	return out
}

// bindsSQL reports whether the folder holds a SQLDatabase or a Function or step that binds one.
func (f *devFolder) bindsSQL() bool {
	for _, d := range f.resources {
		switch o := d.obj.(type) {
		case *v1.SQLDatabase:
			return true
		case *v1.Function:
			if len(o.Spec.Bindings.SQL) > 0 {
				return true
			}
		case *v1.Workflow:
			for _, st := range o.Spec.Steps {
				if st.Function != nil && len(st.Function.Bindings.SQL) > 0 {
					return true
				}
			}
		}
	}
	return false
}

// bootDev boots the embedded platform for folder and applies it: the declared Secrets are resolved from the
// environment FIRST, failing fast before any side effect; then each build and contract is delivered (before the
// platform starts: over a durable metastore it starts the stored Functions at once, and a replica started without its
// contract runs unvalidated, #1053); then the platform starts, the folder is applied, and over a durable metastore what
// an earlier session applied that the folder no longer holds is pruned. It returns once serving.
func (a *cli) bootDev(ctx context.Context, op string, folder *devFolder, cfg devConfig, logger *slog.Logger) (_ *devInstance, err error) {
	names := folder.names()
	if len(names) == 0 {
		return nil, fault.NotFoundf(op, "%s holds no Function or Workflow image step to run", folder.dir)
	}
	secrets, serr := folder.declaredSecrets(op, cfg.secrets)
	if serr != nil {
		return nil, serr
	}
	// A database this build cannot serve would only fail later, at the first call (ADR-0237 Decision 4).
	if folder.bindsSQL() && !sqlembedengine.Bundled() {
		return nil, fault.Unavailablef(op, "the folder holds a SQL database, but this funcdctl has no SQL engine: build it with "+
			"just build-sql-engine %s %s, then go build -tags dev ./cmd/funcdctl", goruntime.GOOS, goruntime.GOARCH)
	}
	plan, perr := resolvePersistPlan(cfg)
	if perr != nil {
		return nil, perr
	}
	processDir, derr := devStateDir(cfg, folder.dir)
	if derr != nil {
		return nil, derr
	}

	inst := &devInstance{runErr: make(chan error, 1), logger: logger, functions: names, workflows: folder.workflowNames(),
		runtimes: &devRuntimes{byImage: map[string]v1.RuntimeName{}}}
	// On any error after this point, run the cleanups we accumulated so a failed boot leaves nothing behind.
	defer func() {
		if err != nil {
			inst.unwind(0)
		}
	}()
	sess := &devSession{folder: folder, plan: plan, stateDirs: plan.watchSkips(processDir), runtimes: inst.runtimes,
		bundles: map[v1.ObjectName]string{}, seen: map[v1.ObjectName]string{}, file: cfg.file, secrets: cfg.secrets}
	desired, berr := sess.prepare(op, inst, folder, secrets, logger)
	if berr != nil {
		return nil, berr
	}
	cancelRun, perr := a.startPlatform(ctx, op, folder, plan, cfg, inst)
	if perr != nil {
		return nil, perr
	}
	// A boot that fails once Run is up stops the platform (Run's Shutdown closes its drivers) before the cleanups run,
	// so no controller outlives the boot on a released driver.
	defer func() {
		if err != nil {
			cancelRun()
			<-inst.runErr
		}
	}()

	client, cerr := sdk.New(inst.controlURL, sdk.WithToken(funcd.DevToken))
	if cerr != nil {
		return nil, cerr
	}
	inst.client = client
	applied, aerr := applyBoot(ctx, op, client, desired, plan, inst.logger)
	if aerr != nil {
		return nil, aerr
	}
	inst.watchDone = make(chan struct{})
	go watchFolder(ctx, op, client, sess, applied, inst.logger, inst.watchDone)
	return inst, nil
}

// declaredSecrets creates each Secret a Function declares (ADR-0248) that no document defines, each key from the
// shell's environment, else the envFile, else dev.env (ADR-0254 Decision 5). A key none holds stops dev, naming the
// three places.
func (f *devFolder) declaredSecrets(op string, src devSecretSources) ([]v1.Object, error) {
	defined := map[v1.ObjectName]bool{}
	for _, d := range f.resources {
		if d.obj.GroupVersionKind().Kind == v1.KindSecret {
			defined[d.obj.GetName()] = true
		}
	}
	var out []v1.Object
	for _, fn := range f.functions {
		for _, decl := range fn.fn.Spec.SecretDeclarations {
			if defined[decl.Name] {
				continue
			}
			defined[decl.Name] = true
			data := make(map[string][]byte, len(decl.Keys))
			for _, k := range decl.Keys {
				v, ok := src.lookup(k)
				if !ok {
					return nil, src.missing(op, fmt.Sprintf("Function %s of %s", fn.fn.Name, fn.file), string(decl.Name), k)
				}
				data[k] = []byte(v)
			}
			obj, _ := v1.NewObject(v1.KindSecret)
			s := obj.(*v1.Secret)
			setMeta(&s.ObjectMeta, decl.Name)
			s.Spec.Data = data
			out = append(out, s)
		}
	}
	return out, nil
}

// writerGrants is the write-only Role and one RolesAssignment per Function dev runs (ADR-0128 Decision 2, as amended):
// each is a member of every prefix's writers set in the dev namespace, so the developer can seed an ownerless prefix
// through the real single-writer forbid, while reads stay binding-gated.
func writerGrants(names []string) []v1.Object {
	robj, _ := v1.NewObject(v1.KindRole)
	role := robj.(*v1.Role)
	setMeta(&role.ObjectMeta, devBlobWriterRole)
	role.Spec.Actions = []string{"s3::write"} // data-plane action token (ADR-0136); write only
	out := []v1.Object{role}
	for _, name := range names {
		obj, _ := v1.NewObject(v1.KindRolesAssignment)
		ra := obj.(*v1.RolesAssignment)
		setMeta(&ra.ObjectMeta, v1.ObjectName("dev-blob-writer-"+name))
		ra.Spec = v1.RolesAssignmentSpec{
			Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: v1.ObjectName(name)},
			Assignments: []v1.AssignmentEntry{{
				RoleRef: v1.RoleRef{Kind: v1.RoleRefKindRole, Name: string(devBlobWriterRole)},
				Scope:   &v1.ScopeRef{Kind: v1.ScopeKindNamespace},
			}},
		}
		out = append(out, ra)
	}
	return out
}

// devRuntimes is dev's workflow.RuntimeResolver (ADR-0247 Decision 7): a step's image is the file:// copy of its
// build, whose runtime comes from the build's contract file, since there is no OCI manifest to read it from.
type devRuntimes struct {
	mu      sync.Mutex
	byImage map[string]v1.RuntimeName
}

func (r *devRuntimes) set(image string, rt v1.RuntimeName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byImage[image] = rt
}

// Runtime returns the runtime of the build image is a copy of.
func (r *devRuntimes) Runtime(_ context.Context, image string) (v1.RuntimeName, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rt, ok := r.byImage[image]
	if !ok {
		return "", fault.NotFoundf("funcdctl dev", "%s is not a step build of this session", image)
	}
	return rt, nil
}

// applyBoot applies desired in dependency order (applyOrdered) and, over a durable metastore, prunes what an earlier
// session applied that the folder no longer holds. It returns the objects the reload starts from.
func applyBoot(ctx context.Context, op string, c *sdk.Client, desired []v1.Object, plan persistPlan, logger *slog.Logger) ([]v1.Object, error) {
	if err := applyOrdered(ctx, op, c, desired); err != nil {
		return nil, err
	}
	if plan.storeDir == "" {
		return desired, nil
	}
	stale, lerr := pruneStale(ctx, c, desired, logger)
	if lerr != nil {
		return nil, fault.Wrapf(lerr, fault.KindOf(lerr), op, "prune the objects an earlier session applied")
	}
	return slices.Concat(desired, stale), nil
}

// lateKinds reference Functions or Workflows, so they apply after them.
//
//nolint:gochecknoglobals // an immutable lookup table
var lateKinds = []v1.Kind{v1.KindRoute, v1.KindEventSource, v1.KindSensor, v1.KindSite, v1.KindWorkflowRun}

// applyOrdered applies desired as production would order it: the backing resources (stageResources keeps a table or
// prefix a Function still binds until the Functions no longer bind it, ADR-0073), the Functions with a link's target
// before its caller, the resources staging held back, the Workflows, then what references them.
func applyOrdered(ctx context.Context, op string, c *sdk.Client, desired []v1.Object) error {
	var base, fns, wfs, late []v1.Object
	for _, obj := range desired {
		switch kind := obj.GroupVersionKind().Kind; {
		case kind == v1.KindFunction:
			fns = append(fns, obj)
		case kind == v1.KindWorkflow:
			wfs = append(wfs, obj)
		case slices.Contains(lateKinds, kind):
			late = append(late, obj)
		default:
			base = append(base, obj)
		}
	}
	first, last, serr := stageResources(ctx, op, c, base)
	if serr != nil {
		return serr
	}
	for _, group := range [][]v1.Object{first, orderFunctionsByLinks(fns), last, wfs, late} {
		for _, obj := range group {
			if aerr := applyDesired(ctx, c, obj); aerr != nil {
				return fault.Wrapf(aerr, fault.KindOf(aerr), op, "apply %s %q", obj.GroupVersionKind().Kind, obj.GetName())
			}
		}
	}
	return nil
}

// devSession is the state the boot and each reload share: the folder last applied, the durable layout, the bundle of
// each Function (a private directory, the same across reloads), the fingerprint each was last delivered at, the copy
// directory of each step, and the old step copies that go once their step's new revision serves (Decision 7).
type devSession struct {
	folder    *devFolder
	plan      persistPlan
	stateDirs []string
	runtimes  *devRuntimes
	bundles   map[v1.ObjectName]string // Function → its bundle directory
	seen      map[v1.ObjectName]string // Function or step → the fingerprint last delivered
	stepRoot  string                   // where the step copies live
	stepDirs  map[v1.ObjectName]string // step → its current copy directory
	oldSteps  []oldStep
	started   map[v1.ObjectName]bool // the Functions and steps the boot started; a later one needs a restart
	file      *devFileState          // funcdctl.config.yaml, nil without one (ADR-0254 Decision 6)
	secrets   devSecretSources
	settle    devSettle
}

// oldStep is a step copy that goes once its step's Function serves image.
type oldStep struct {
	fn    v1.ObjectName
	dir   string
	image string
}

// prepare delivers folder's builds and changes its objects as Decision 5 lists, and returns the desired objects: the
// documents, the declared Secrets and the writer grants. At boot it records which Functions and steps started; on a
// reload a new one is left out with a warning, since dev starts no Function after boot.
func (s *devSession) prepare(op string, inst *devInstance, folder *devFolder, secrets []v1.Object, logger *slog.Logger) ([]v1.Object, error) {
	boot := s.started == nil
	if boot {
		s.started = map[v1.ObjectName]bool{}
		s.stepDirs = map[v1.ObjectName]string{}
	}
	skip := map[v1.ObjectName]bool{}
	for _, fn := range folder.functions {
		if !boot && !s.started[fn.fn.Name] {
			logger.Warn("restart funcdctl dev to run a Function added after it started", "function", fn.fn.Name)
			skip[fn.fn.Name] = true
			continue
		}
		s.started[fn.fn.Name] = true
		if err := s.deliverFunction(op, inst, folder, fn); err != nil {
			return nil, err
		}
	}
	for _, st := range folder.steps {
		if !boot && !s.started[st.name] {
			logger.Warn("restart funcdctl dev to run a Workflow step added after it started", "function", st.name)
			continue
		}
		s.started[st.name] = true
		if err := s.deliverStep(op, inst, folder, st); err != nil {
			return nil, err
		}
	}
	var desired []v1.Object
	for _, d := range folder.resources {
		if fn, ok := d.obj.(*v1.Function); ok && skip[fn.Name] {
			continue
		}
		desired = append(desired, d.obj)
	}
	var grantees []string
	for _, name := range folder.names() {
		if s.started[v1.ObjectName(name)] {
			grantees = append(grantees, name)
		}
	}
	return slices.Concat(desired, secrets, writerGrants(grantees)), nil
}

// deliverFunction delivers fn's build into its bundle when it changed and points the Function at it, with one replica
// and the build's fingerprint as its imageDigest, so a rebuild rolls a new revision (ADR-0143).
func (s *devSession) deliverFunction(op string, inst *devInstance, folder *devFolder, fn *devFunction) error {
	name := fn.fn.Name
	dir, ok := s.bundles[name]
	if !ok {
		d, kept, err := privateBundleDir(op, s.plan.bundleDir, name, fn.build.srcDir)
		if err != nil {
			return err
		}
		if !kept {
			inst.cleanup = append(inst.cleanup, func() { _ = os.RemoveAll(d) })
		}
		s.bundles[name], dir = d, d
	}
	fp, err := fn.build.fingerprint(s.stateDirs)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "fingerprint the build of %s", name)
	}
	if fp != s.seen[name] {
		if derr := deliverBuild(op, fn.build, dir, folder.dir); derr != nil {
			return derr
		}
		s.seen[name] = fp
	}
	fn.fn.Spec.Image = "file://" + filepath.Join(dir, fn.build.entry)
	fn.fn.Spec.ImageDigest = fp
	fn.fn.Spec.Replicas = 1
	fn.fn.Spec.Scaling.MinReplicas = 1
	return nil
}

// deliverStep copies st's build to a directory named after its fingerprint and points the step at it: a run compares
// the image reference, so only a new path rolls the step (Decision 7). The previous copy goes once the step's
// Function serves the new one; under --persist the last copy stays.
func (s *devSession) deliverStep(op string, inst *devInstance, folder *devFolder, st *devStep) error {
	if s.stepRoot == "" {
		root := s.plan.bundleDir
		if root == "" {
			tmp, err := os.MkdirTemp("", "funcdctl-dev-steps-")
			if err != nil {
				return fault.Wrapf(err, fault.Internal, op, "create the step copies dir")
			}
			inst.cleanup = append(inst.cleanup, func() { _ = os.RemoveAll(tmp) })
			root = tmp
		}
		s.stepRoot = root
	}
	fp, err := st.build.fingerprint(s.stateDirs)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "fingerprint the build of step %s", st.name)
	}
	dir := filepath.Join(s.stepRoot, string(st.name)+"-"+strings.TrimPrefix(fp, "sha256:")[:16])
	image := "file://" + filepath.Join(dir, st.build.entry)
	if prev := s.stepDirs[st.name]; prev != dir {
		if merr := os.MkdirAll(dir, 0o700); merr != nil {
			return fault.Wrapf(merr, fault.Internal, op, "create %s", dir)
		}
		if derr := deliverBuild(op, st.build, dir, folder.dir); derr != nil {
			return derr
		}
		if prev != "" {
			s.oldSteps = append(s.oldSteps, oldStep{fn: st.name, dir: prev, image: image})
		}
		s.stepDirs[st.name] = dir
	}
	st.workflow.Spec.Steps[st.index].Function.Image = image
	s.runtimes.set(image, st.build.contract.Runtime)
	return nil
}

// collectOldSteps removes each old step copy whose step Function now serves its new image (Decision 7).
func (s *devSession) collectOldSteps(ctx context.Context, c *sdk.Client) {
	s.oldSteps = slices.DeleteFunc(s.oldSteps, func(o oldStep) bool {
		obj, err := c.Get(ctx, v1.KindFunction, devNamespace, o.fn)
		if err != nil {
			return false
		}
		fn, ok := obj.(*v1.Function)
		if !ok || fn.Spec.Image != o.image || fn.Status.Phase != v1.PhaseReady || fn.Status.ServingRevision != fn.Status.CurrentRevision {
			return false
		}
		_ = os.RemoveAll(o.dir)
		return true
	})
}

// deliverBuild puts b's entry and its ADR-0123 contract into bundleDir: links to every file of its directory for a
// python entry that runs in place (its sibling imports resolve), else a copy of the entry with a best-effort link to
// the folder's node_modules. An http Function delivers no contract, and a stale one is removed (ADR-0243 Decision 2).
func deliverBuild(op string, b devBuild, bundleDir, folderDir string) error {
	if b.inPlace {
		if err := linkSources(op, b.srcDir, bundleDir); err != nil {
			return err
		}
	} else {
		data, rerr := os.ReadFile(filepath.Join(b.srcDir, b.entry)) //nolint:gosec // the build of the folder the user runs
		if rerr != nil {
			return fault.Wrapf(rerr, fault.NotFound, op, "read build %q", b.entry)
		}
		dst := filepath.Join(bundleDir, b.entry)
		// A bundle kept across runs (--persist) may hold a link an earlier run made: replace it rather than write
		// through it into the source.
		if fi, lerr := os.Lstat(dst); lerr == nil && fi.Mode()&fs.ModeSymlink != 0 {
			if rerr := os.Remove(dst); rerr != nil {
				return fault.Wrapf(rerr, fault.Internal, op, "replace link %q", dst)
			}
		}
		if werr := os.WriteFile(dst, data, 0o600); werr != nil {
			return fault.Wrapf(werr, fault.Internal, op, "copy the build into its bundle")
		}
		if nm := filepath.Join(folderDir, "node_modules"); dirExists(nm) {
			_ = os.Symlink(nm, filepath.Join(bundleDir, "node_modules"))
		}
	}
	contractPath := filepath.Join(bundleDir, devContractFile)
	blob, err := b.contractBlob()
	if err != nil {
		return fault.Wrapf(err, fault.KindOf(err), op, "build the contract of %s", b.name)
	}
	if blob == nil {
		if rerr := os.Remove(contractPath); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			return fault.Wrapf(rerr, fault.Internal, op, "remove contract %q", contractPath)
		}
		return nil
	}
	if werr := os.WriteFile(contractPath, blob, 0o600); werr != nil {
		return fault.Wrapf(werr, fault.Internal, op, "deliver contract to %q", contractPath)
	}
	return nil
}

const (
	// devNamespace is the namespace `funcd.InMemory()` authorizes by default (WithDevAuth(DevToken,
	// "default")), so every synthesized resource lands there.
	devNamespace = "default"
	// devResourceGroup is a fixed resource group for the synthesized dev resources.
	devResourceGroup = "dev"
	// devManagedTag marks an object funcdctl dev synthesized (its value is devManagedBy), so a --persist boot
	// deletes only its own objects that the manifests no longer name, never one the user applied.
	devManagedTag = "managed-by"
	devManagedBy  = "funcdctl-dev"
	// devContractFile is the ADR-0123 delivered-contract dotfile `funcdctl dev` writes into the bundle
	// dir (the working tree). The Function reconciler probes the bundle root for it and sets
	// FUNCD_CONTRACT_PATH, so the shim runtime-compiles + enforces the manifest's contract with no push.
	devContractFile = ".funcd-contract.json"
	// devS3Master is the FIXED dev S3 master secret the S3 frontend (ADR-0085) derives per-function
	// keypairs from in `funcdctl dev` (Decision 6). Like DevToken, it is a well-known, non-secret dev
	// constant — NEVER a production identity — so the printed dev creds are stable and reproducible
	// across restarts. The author uses the derived keypair to `aws s3 ls` the same substrate the
	// function writes through.
	devS3Master = "funcd-dev-s3-master-do-not-use-in-production" //nolint:gosec // a documented dev-only constant, not a credential
	// devS3Region is the region the S3 frontend serves under (ADR-0085 backend region); the aws CLI /
	// SDK needs AWS_REGION set for SigV4 even against a local endpoint.
	devS3Region = "us-east-1"
	// devPersistDir is the default `--persist-to` directory (gitignored). Per-service subdirs
	// (metastore/kv/blob) live under it (Decision 7).
	devPersistDir = ".funcd-dev"
	// devReloadPoll is how often `funcdctl dev` checks each handler source for an edit (hot-reload).
	devReloadPoll = 300 * time.Millisecond
)

// printBanner prints a formatted list of every localhost service `funcdctl dev` exposes (Decision 6):
// the gateway (invoke functions), the S3 endpoint (inspect blob) + the dev keypair, the embedded
// catalog engine when a catalog is bound, and the loaded workflow — closing with the honest fidelity
// note that egress is NOT isolated in dev (scenario: dev-egress-not-isolated).
func (a *cli) printBanner(inst *devInstance) error {
	// The renderer is bound to a.out, so lipgloss auto-disables color when output is piped or captured
	// (a non-TTY writer → plain text) and colors it on a real terminal.
	r := lipgloss.NewRenderer(a.out)
	accent := lipgloss.AdaptiveColor{Light: "#7D56F4", Dark: "#B694FF"}
	link := lipgloss.AdaptiveColor{Light: "#0969DA", Dark: "#58A6FF"}
	okc := lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#3FB950"}
	dim := lipgloss.AdaptiveColor{Light: "#57606A", Dark: "#8B949E"}
	warn := lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"}

	titleS := r.NewStyle().Bold(true).Foreground(accent)
	subS := r.NewStyle().Foreground(dim)
	headerS := r.NewStyle().Bold(true).Foreground(accent)
	nameS := r.NewStyle().Foreground(okc).Width(9)
	svcURLS := r.NewStyle().Foreground(link).Width(26)
	urlS := r.NewStyle().Foreground(link)
	descS := r.NewStyle().Foreground(dim)
	warnS := r.NewStyle().Foreground(warn).Bold(true)
	keyS := r.NewStyle().Foreground(dim)
	boxS := r.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 3)

	svc := func(n, u, d string) string {
		return nameS.Render(n) + " " + svcURLS.Render(u) + " " + descS.Render(d)
	}

	lines := []string{
		titleS.Render("● funcd dev") + subS.Render("   serving on localhost · Ctrl-C to stop"),
		"",
		headerS.Render("SERVICES"),
		svc("gateway", inst.gatewayURL, "invoke functions"),
		svc("control", inst.controlURL, "apply · workflow run — funcdctl --server <url> --token "+funcd.DevToken),
		svc("s3", inst.s3Endpoint, "inspect blob · aws s3 ls s3://<bucket>/"),
	}
	if len(inst.catalogs) > 0 {
		lines = append(lines, svc("catalog", "embedded duckdb+quack",
			"serves: "+strings.Join(inst.catalogs, ", ")+" · DuckLake over blob"))
	}
	if inst.live != nil {
		lines = append(lines, nameS.Render("reload")+" "+urlS.Render(inst.live.url())+" "+descS.Render("Site pages reload on a Site change"))
	}

	if inst.app != nil {
		lines = append(lines, "", headerS.Render("APP")+subS.Render("   "+string(inst.app.Name)+" · "+inst.appRevision()))
		for _, e := range inst.appEndpoints() {
			lines = append(lines, nameS.Render(e[0])+" "+urlS.Render(e[1]))
		}
	} else {
		lines = append(lines, "", headerS.Render("FUNCTIONS"))
		for _, fn := range inst.functions {
			lines = append(lines, descS.Render("POST")+"  "+urlS.Render(inst.gatewayURL+"/function/"+fn))
		}
	}

	if inst.commands != nil {
		lines = append(lines, "", headerS.Render("WATCH")+subS.Render("   each command's output goes to its log file"))
		for _, e := range inst.commands.entries {
			lines = append(lines, nameS.Render(e.Name)+" "+descS.Render(inst.commands.logs[e.Name]))
		}
	}

	if len(inst.workflows) > 0 {
		// A run is triggered by a WorkflowRun (the surface a Sensor or `funcdctl workflow run` uses); the image steps
		// run from their builds through the workflow materializer (ADR-0247 Decision 7).
		lines = append(lines, "", headerS.Render("WORKFLOWS"))
		for _, wf := range inst.workflows {
			lines = append(lines, urlS.Render(wf)+subS.Render("   apply a WorkflowRun to trigger"))
		}
	}

	// The S3 frontend (ADR-0080/0085): the same surface a function writes blob through, exposed so the
	// author inspects artifacts with prod tools. The creds are the dev function's derived keypair.
	lines = append(lines, "", headerS.Render("S3 CREDENTIALS")+subS.Render("   export to use aws / duckdb against the endpoint"))
	for _, kv := range [][2]string{
		{"AWS_ACCESS_KEY_ID", inst.s3AccessKey},
		{"AWS_SECRET_ACCESS_KEY", inst.s3SecretKey},
		{"AWS_REGION", inst.s3Region},
		{"AWS_ENDPOINT_URL_S3", inst.s3Endpoint},
	} {
		lines = append(lines, keyS.Render("export "+kv[0]+"=")+kv[1])
	}

	lines = append(lines, "", warnS.Render("⚠ egress is NOT isolated in dev — this is not where you validate confidentiality"))

	content := lipgloss.JoinVertical(lipgloss.Left, lines...)
	return a.writef("\n%s\n\n", boxS.Render(content))
}

// devLogStyler renders a streamed function log line compactly and in color: a dim timestamp, the
// function name in yellow, the severity in a level color (INFO blue, WARN yellow, ERROR/FATAL red,
// DEBUG/TRACE dim), the body, then sorted structured attrs dim. Built ONCE per dev session (the
// renderer's terminal detection isn't repeated per line); lipgloss auto-plains a non-TTY writer. Styles
// are inline so a value wider than its column pads instead of wrapping: one record stays one line.
type devLogStyler struct {
	timeS, funcS, bodyS, attrS, defS lipgloss.Style
	sev                              map[string]lipgloss.Style
	// loc is the zone every line's time shows in: a structured record's time is UTC and a plain line's local (#1134).
	loc *time.Location
}

func newDevLogStyler(w io.Writer) *devLogStyler {
	r := lipgloss.NewRenderer(w)
	c := func(code string) lipgloss.Style { return r.NewStyle().Inline(true).Foreground(lipgloss.Color(code)) }
	return &devLogStyler{
		timeS: c("240"),          // dim gray
		funcS: c("11").Width(12), // yellow, padded to align bodies
		bodyS: r.NewStyle(),      // default fg
		attrS: c("240"),          // dim
		defS:  c("7").Width(5),   // unknown severity
		sev: map[string]lipgloss.Style{
			"TRACE": c("240").Width(5),
			"DEBUG": c("245").Width(5),
			"INFO":  c("12").Width(5), // blue
			"WARN":  c("11").Width(5), // yellow
			"ERROR": c("9").Width(5),  // red
			"FATAL": c("9").Bold(true).Width(5),
		},
		loc: time.Local,
	}
}

// format renders one log line (no trailing newline).
func (s *devLogStyler) format(l funcd.LogLine) string {
	sev := termSafe(l.Severity)
	if sev == "" {
		sev = "INFO"
	}
	sevS, ok := s.sev[sev]
	if !ok {
		sevS = s.defS
	}
	var attrs strings.Builder
	if len(l.Attrs) > 0 {
		keys := make([]string, 0, len(l.Attrs))
		for k := range l.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&attrs, " %s=%s", termSafe(k), termSafe(l.Attrs[k]))
		}
	}
	return fmt.Sprintf("  %s  %s %s  %s%s",
		s.timeS.Render(l.Time.In(s.loc).Format("15:04:05")),
		s.funcS.Render(l.Function),
		sevS.Render(sev),
		s.bodyS.Render(termSafe(l.Body)),
		s.attrS.Render(attrs.String()),
	)
}

// devInstance is a running `funcdctl dev` platform + the seams a test (or the command) drives it by.
type devInstance struct {
	platform   *funcd.Platform
	client     *sdk.Client
	gatewayURL string   // the data-plane base URL functions are invoked at (POST <url>/function/<name>)
	controlURL string   // the control-plane base URL (funcdctl --server: apply, workflow run)
	functions  []string // the Functions dev runs: each Function, then each Workflow image step's
	workflows  []string // the folder's Workflows
	catalogs   []string // the CatalogServices the embedded duckdb+quack engine serves; nil if none
	runtimes   *devRuntimes
	// artifactDir, when set, runs the parts from the OCI images dev pushed, pulled into it (App mode, ADR-0253).
	artifactDir string
	// app is the App dev deployed (App mode); the banner names it, its Routes and its Sites instead of the Functions.
	app *v1.App
	// s3Name is the name the S3 keypair is derived over; "" derives it over the first Function.
	s3Name string
	// S3 frontend (ADR-0080/0085, Decision 6): the endpoint + the dev function's derived keypair. The
	// author (or a test) points an S3 client at s3Endpoint with these creds to inspect blob artifacts.
	s3Endpoint  string
	s3AccessKey string
	s3SecretKey string
	s3Region    string
	// Durable drivers (ADR-0125 Decision 7): non-nil only under --persist / a file:// backend. Exposed
	// so a restart test can write + read state directly; the platform owns + Closes them on Shutdown.
	kv        kvstore.KV
	blob      blob.Bucket
	runErr    chan error
	watchDone chan struct{} // closed when the hot-reload watcher has returned
	// commands are the watch commands dev supervises and live serves Site live reload (ADR-0255), each nil when off;
	// the banner names them, and the cleanups stop them.
	commands *devCommands
	live     *devLiveReload
	logger   *slog.Logger // funcdctl dev's own warnings, in the daemon's text form over the cli's out (ADR-0197)
	cleanup  []func()
}

// stop cancels nothing itself (the caller owns the ctx passed to startDev); it waits for the platform's
// Run to return and runs every registered cleanup (shim temp dir, the delivered contract dotfile).
func (d *devInstance) stop() error {
	var runErr error
	if d.runErr != nil {
		runErr = <-d.runErr
	}
	if d.watchDone != nil {
		<-d.watchDone
	}
	d.unwind(0)
	return runErr
}

// unwind runs the cleanups registered from index mark on, newest first, and drops them.
func (d *devInstance) unwind(mark int) {
	for i := len(d.cleanup) - 1; i >= mark; i-- {
		d.cleanup[i]()
	}
	d.cleanup = d.cleanup[:mark]
}

// devS3PortAttempts bounds how many S3 ports startPlatform picks before it gives up (#627).
const devS3PortAttempts = 3

// startPlatform builds and runs the embedded platform and waits until its S3 frontend serves; it returns the cancel of
// the platform's Run. The gateway binds its own listener only when the platform runs (versitygw cannot take an open
// one), so a port picked for it (--s3port 0) can be taken by another process in between: that attempt is stopped and
// unwound, and the platform starts again on a fresh port (#627). A fixed --s3port is never replaced.
func (a *cli) startPlatform(ctx context.Context, op string, folder *devFolder, plan persistPlan, cfg devConfig, inst *devInstance) (context.CancelFunc, error) {
	for attempt := 1; ; attempt++ {
		mark := len(inst.cleanup)
		opts, oerr := a.devPlatformOptions(ctx, op, folder, plan, cfg, inst)
		if oerr != nil {
			return nil, oerr
		}
		p, nerr := funcd.New(opts...)
		// New owns the durable drivers from here: a failed New has closed them, a platform closes them on Shutdown.
		if nerr != nil {
			return nil, nerr
		}
		inst.platform = p
		inst.gatewayURL = "http://" + p.DataPlaneAddr()
		inst.controlURL = "http://" + p.Addr()

		runCtx, cancelRun := context.WithCancel(ctx)
		go func() { inst.runErr <- p.Run(runCtx) }()
		serr := p.WaitS3Gateway(runCtx)
		if serr == nil {
			return cancelRun, nil
		}
		cancelRun()
		<-inst.runErr
		if cfg.s3port != 0 || attempt == devS3PortAttempts || !errors.Is(serr, syscall.EADDRINUSE) {
			return nil, fault.Wrapf(serr, fault.KindOf(serr), op, "serve the S3 frontend on %s", inst.s3Endpoint)
		}
		inst.unwind(mark)
	}
}

// devPlatformOptions builds the embedded platform's options: the extracted shims, the catalog dev engine, the
// function log stream, the fixed listen ports, the durable/ephemeral drivers and the S3 frontend. It registers
// each cleanup on inst and closes the durable drivers itself when it fails after opening them; funcd.New owns them
// once it is called.
func (a *cli) devPlatformOptions(ctx context.Context, op string, folder *devFolder, plan persistPlan, cfg devConfig, inst *devInstance) ([]funcd.Option, error) {
	var needPython, needNode bool
	for _, b := range folder.builds() {
		if strings.HasPrefix(string(b.contract.Runtime), "python") {
			needPython = true
		} else {
			needNode = true
		}
	}
	shimOpts, shimCleanup, sherr := devShimOptions(ctx, op, needPython, needNode)
	if sherr != nil {
		return nil, sherr
	}
	inst.cleanup = append(inst.cleanup, shimCleanup)

	// The process driver and the catalog engine save what they start in the state dir and reap what a crashed run
	// left there before anything starts (ADR-0167).
	stateDir, sderr := devStateDir(cfg, folder.dir)
	if sderr != nil {
		return nil, sderr
	}
	rt, rerr := process.Open(ctx, stateDir, 0, inst.logger)
	if rerr != nil {
		return nil, fault.Wrapf(rerr, fault.KindOf(rerr), op, "open the process runtime in %s", stateDir)
	}
	inst.cleanup = append(inst.cleanup, func() { _ = rt.Close() })

	opts := []funcd.Option{
		funcd.InMemory(),
		funcd.WithRuntime(rt),
		funcd.WithMaterializer(function.NewFileMaterializer()),
		// A step's file:// copy carries no OCI artifact, so the production F65 resolver can never inspect it. An untyped
		// resolver makes the typed-edge gate a no-op and the Workflow reaches Ready (each step still enforces its OWN
		// contract at the shim, ADR-0123); the runtime comes from the build's contract file (ADR-0247 Decision 7).
		funcd.WithWorkflowContractResolver(devWorkflowContracts{}),
		funcd.WithWorkflowRuntimeResolver(inst.runtimes),
	}
	if inst.artifactDir != "" {
		// An App's parts are OCI images in dev's layout, pulled and resolved as a deploy pulls them (ADR-0253).
		opts = []funcd.Option{
			funcd.InMemory(),
			funcd.WithRuntime(rt),
			funcd.WithMaterializer(artifact.NewOrasMaterializer(inst.artifactDir, v1.HostPlatform())),
		}
	}
	// Dev's cadences in both modes (ADR-0254 Decision 4): a saved build, an upload and a log line show within about a
	// second.
	opts = append(opts,
		funcd.WithPacing(funcd.Pacing{HandOutSettle: cfg.pace.handOutSettle}),
		funcd.WithBlobPollInterval(cfg.pace.blobPollInterval),
		funcd.WithFunclog(cfg.pace.segmentMaxAge, 0),
	)
	catOpts, caterr := devCatalogOptions(plan, stateDir, folder, inst)
	if caterr != nil {
		return nil, caterr
	}
	opts = append(opts, catOpts...)
	sqlOpts, sqlerr := devSQLOptions(plan, stateDir, inst)
	if sqlerr != nil {
		return nil, sqlerr
	}
	opts = append(opts, sqlOpts...)

	// Stream function logs to the terminal in real time (Decision 6, dev UX): tee every captured log
	// line to the printer. A mutex keeps concurrent functions' lines from interleaving.
	logStyler := newDevLogStyler(a.out)
	var logMu sync.Mutex
	opts = append(opts, funcd.WithLogObserver(func(l funcd.LogLine) {
		logMu.Lock()
		defer logMu.Unlock()
		_ = a.writef("%s\n", logStyler.format(l))
	}))

	// Fixed listen ports for reproducible URLs (--gport / --s3port / --cport); 0 keeps the ephemeral free
	// port. These override InMemory()'s 127.0.0.1:0 (control plane + data plane); the S3 port is applied in
	// devS3Options.
	if perr := checkFixedPorts(op, cfg); perr != nil {
		return nil, perr
	}
	if cfg.gport != 0 {
		opts = append(opts, funcd.WithDataPlaneAddr(fmt.Sprintf("127.0.0.1:%d", cfg.gport)))
	}
	if cfg.cport != 0 {
		opts = append(opts, funcd.WithListenAddr(fmt.Sprintf("127.0.0.1:%d", cfg.cport)))
	}
	// The SQL engines' change feed (ADR-0235) on a free loopback port, so dev runs side by side never contend for the
	// daemon's fixed default.
	feed, ferr := freeLocalAddr()
	if ferr != nil {
		return nil, fault.Wrapf(ferr, fault.Internal, op, "reserve the SQL change feed port")
	}
	_, feedPort, _ := net.SplitHostPort(feed)
	port, _ := strconv.Atoi(feedPort)
	opts = append(opts, funcd.WithSQLChangeFeedPort(port))

	// Durable-local drivers (ADR-0125 Decision 7): under --persist the memory store/KV/blob are swapped for Badger +
	// fileblob under per-service subdirs. The platform takes ownership + Closes them on Shutdown; they are closed here
	// only if the options fail BEFORE funcd.New takes ownership.
	persistOpts, kv, bkt, closeDurable, derr := buildPersistDrivers(op, plan)
	if derr != nil {
		return nil, derr
	}
	opts = append(opts, persistOpts...)
	inst.kv, inst.blob = kv, bkt

	// S3 frontend (ADR-0080/0085, Decision 6): expose the blob substrate over the S3 protocol on a free
	// node-private port. The printed creds are the FIRST function's derived keypair (representative).
	s3Identity := inst.s3Name
	if s3Identity == "" && len(inst.functions) > 0 {
		s3Identity = inst.functions[0]
	}
	s3Cleanup, s3err := devS3Options(op, &opts, s3Identity, cfg, inst)
	if s3err != nil {
		if closeDurable != nil {
			closeDurable()
		}
		return nil, s3err
	}
	inst.cleanup = append(inst.cleanup, s3Cleanup)
	return append(opts, shimOpts...), nil
}

// devCatalogOptions wires the catalog dev engine (ADR-0125 Decision 5) when the folder holds a CatalogService or a
// Function binds one: the process-mode DuckDB+Quack engine driver (internal/catalog/devengine) becomes the
// CatalogService add-on-provider runtime, running the go:embed'd engine as a host subprocess (no container, no cgo). A
// dev binary built WITHOUT the engine reports catalog-unavailable at reconcile time, not here, so `funcdctl dev` still
// boots. It records the served catalogs and registers each cleanup on inst; no catalog ⇒ no options.
func devCatalogOptions(plan persistPlan, stateDir string, folder *devFolder, inst *devInstance) ([]funcd.Option, error) {
	names := folder.catalogNames()
	if len(names) == 0 && !slices.ContainsFunc(folder.functions, func(f *devFunction) bool { return len(f.fn.Spec.Bindings.Catalog) > 0 }) {
		return nil, nil
	}
	inst.catalogs = names
	// Under --persist, the DuckLake SQLite catalog lives in a durable per-provider dir (mirroring the
	// metastore) so a dev restart reopens it; ephemeral otherwise.
	catOpts := []devengine.Option{devengine.WithStateDir(stateDir)}
	if plan.catalogDir != "" {
		catOpts = append(catOpts, devengine.WithCatalogDir(plan.catalogDir))
	}
	catEngine, err := devengine.New(inst.logger, catOpts...)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "funcdctl dev", "start the catalog engine runtime in %s", stateDir)
	}
	opts := []funcd.Option{funcd.WithCatalogProviderRuntime(catEngine)}
	inst.cleanup = append(inst.cleanup, catEngine.StopAll)

	// A catalog CONSUMER handler runs its own duckdb (from the dev venv) and must LOAD the curated
	// quack/ducklake extensions — in prod those ride the bundle's duckdb-ext (ADR-0089), absent when
	// running from source. Extract the embedded engine's extensions once and point consumers at them
	// via DUCKDB_EXTENSION_DIRECTORY (ADR-0125 dev-catalog-query). A placeholder build (no engine)
	// skips it — the same not-bundled path the provider engine reports at reconcile time.
	if embedengine.Bundled() {
		if extRoot, xerr := os.MkdirTemp("", "funcd-dev-duckdb-ext-*"); xerr == nil {
			if paths, perr := embedengine.Extract(extRoot); perr == nil {
				opts = append(opts, funcd.WithCatalogExtensionDir(paths.ExtensionDir))
				inst.cleanup = append(inst.cleanup, func() { _ = os.RemoveAll(extRoot) })
			} else {
				_ = os.RemoveAll(extRoot)
				inst.logger.Warn("could not extract catalog extensions for consumers", "err", perr)
			}
		}
	}
	return opts, nil
}

// devSQLOptions runs the SQL databases on the dev engine (ADR-0237 Decision 2) when the build embeds one, so a database
// a reload adds is served too: the embedded rqlited as a host subprocess per database, its data under
// <persist-to>/sql with --persist, else in a temporary root, and its processes saved in the state dir for the reap
// after a crash. bootDev has already refused a binding this build cannot serve.
func devSQLOptions(plan persistPlan, stateDir string, inst *devInstance) ([]funcd.Option, error) {
	if !sqlembedengine.Bundled() {
		return nil, nil
	}
	eng, err := sqldevengine.New(inst.logger, sqldevengine.WithStateDir(stateDir), sqldevengine.WithDataDir(plan.sqlDir))
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), "funcdctl dev", "start the SQL engine runtime in %s", stateDir)
	}
	inst.cleanup = append(inst.cleanup, eng.StopAll)
	return []funcd.Option{funcd.WithSQLProviderRuntime(eng)}, nil
}

// checkFixedPorts refuses two equal fixed listen ports (--gport / --s3port / --cport; 0 is the ephemeral free
// port). The ports are checked in a fixed order so the error is stable.
func checkFixedPorts(op string, cfg devConfig) error {
	fixedPorts := []struct {
		name string
		port int
	}{{"--gport", cfg.gport}, {"--s3port", cfg.s3port}, {"--cport", cfg.cport}}
	seenPort := map[int]string{}
	for _, fp := range fixedPorts {
		if fp.port == 0 {
			continue
		}
		if other, dup := seenPort[fp.port]; dup {
			return fault.Invalidf(op, "%s and %s must be different ports (both %d)", other, fp.name, fp.port)
		}
		seenPort[fp.port] = fp.name
	}
	return nil
}

// pruneStale deletes each object an earlier --persist session applied that desired no longer holds, as a reload prunes
// them (ADR-0247 Decision 8: the folder is the session's desired state), and returns the ones that stay.
// Only an object carrying devManagedTag is the session's, so an object the user applied is never deleted. The
// kinds are listed in apply order, so a Workflow goes before its Functions and a Function before what it binds.
func pruneStale(ctx context.Context, c *sdk.Client, desired []v1.Object, logger *slog.Logger) ([]v1.Object, error) {
	var prev []v1.Object
	for _, kind := range []v1.Kind{
		v1.KindKVStore, v1.KindBucket, v1.KindSQLDatabase, v1.KindSecret, v1.KindCatalogService, v1.KindConfigMap,
		v1.KindRole, v1.KindRolesAssignment, v1.KindPolicy, v1.KindEgressPolicy, v1.KindFunction, v1.KindWorkflow,
		v1.KindRoute, v1.KindEventSource, v1.KindSensor, v1.KindSite,
	} {
		objs, err := c.List(ctx, kind, devNamespace)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			if o.GetObjectMeta().Tags[devManagedTag] == devManagedBy {
				prev = append(prev, o)
			}
		}
	}
	kept := pruneRemoved(ctx, c, prev, desired, true, logger)
	return kept[len(desired):], nil
}

// devS3Options enables the ADR-0080/0085 S3 frontend (Decision 6): it reserves a free node-private port,
// writes the FIXED dev master secret to a temp file the gateway reads, appends WithS3Gateway to opts, and
// records the dev function's derived keypair on inst for the banner + tests. The temp master is removed by
// the returned cleanup (registered on stop). Enabled for every `funcdctl dev` run — a non-blob function
// simply gets no keypair injected, and the listener is harmless.
func devS3Options(op string, opts *[]funcd.Option, fnName string, cfg devConfig, inst *devInstance) (cleanup func(), err error) {
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.s3port)
	if cfg.s3port == 0 {
		pick := cfg.pickS3Addr
		if pick == nil {
			pick = freeLocalAddr
		}
		a, aerr := pick()
		if aerr != nil {
			return nil, fault.Wrapf(aerr, fault.Internal, op, "reserve S3 frontend port")
		}
		addr = a
	}
	f, cerr := os.CreateTemp("", "funcdctl-dev-s3-master-*")
	if cerr != nil {
		return nil, fault.Wrapf(cerr, fault.Internal, op, "create S3 master temp file")
	}
	masterPath := f.Name()
	cleanup = func() { _ = os.Remove(masterPath) }
	if _, werr := f.WriteString(devS3Master); werr != nil {
		_ = f.Close()
		cleanup()
		return nil, fault.Wrapf(werr, fault.Internal, op, "write S3 master secret")
	}
	if closeErr := f.Close(); closeErr != nil {
		cleanup()
		return nil, fault.Wrapf(closeErr, fault.Internal, op, "close S3 master file")
	}

	endpoint := "http://" + addr
	*opts = append(*opts, funcd.WithS3Gateway(addr, endpoint, 0, masterPath, ""))
	kp := s3gateway.DeriveKeypair([]byte(devS3Master), v1.KindFunction, devNamespace, fnName)
	inst.s3Endpoint = endpoint
	inst.s3AccessKey = kp.AccessKey
	inst.s3SecretKey = kp.SecretKey
	inst.s3Region = devS3Region
	return cleanup, nil
}

// freeLocalAddr reserves an ephemeral node-private TCP address by binding :0 and releasing it — the
// s3gateway binds its own listener at Run and only reports the configured address, so `funcdctl dev`
// picks a concrete free port up front. Another process can take it before the gateway binds it, and
// then startPlatform starts again on a fresh port (#627). Loopback only.
func freeLocalAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	return addr, l.Close()
}

// persistPlan is the resolved durable-driver layout (ADR-0125 Decision 7): an empty dir for a kind means
// "keep the ephemeral memory driver". Dirs are absolute, per-service subdirs of the persist root.
type persistPlan struct {
	storeDir   string // durable metastore (Badger); "" ⇒ memory
	kvDir      string // durable function KV (Badger); "" ⇒ memory
	blobDir    string // durable blob (fileblob); "" ⇒ mem://
	catalogDir string // durable DuckLake SQLite catalog root (devengine); "" ⇒ ephemeral temp
	sqlDir     string // durable SQL engines' data root (ADR-0237); "" ⇒ ephemeral temp
	bundleDir  string // isolated functions' bundles, the same path on every run (#1053); "" ⇒ a temp dir per run
}

// watchSkips are the dirs the hot-reload watcher never walks, since the session writes them while it runs: the durable
// state, the isolated bundles and processDir.
func (p persistPlan) watchSkips(processDir string) []string {
	return []string{p.storeDir, p.kvDir, p.blobDir, p.catalogDir, p.sqlDir, p.bundleDir, processDir}
}

// devStateDir is where `funcdctl dev` saves the workers and catalog engines it starts (ADR-0167): <persist-to>/process
// under --persist; otherwise there is no data dir, so a per-project dir in the user cache dir, named by the first 16
// hex digits of the sha256 of the absolute project dir.
func devStateDir(cfg devConfig, projectDir string) (string, error) {
	const op = "funcdctl dev"
	if cfg.persist {
		root := cfg.persistTo
		if root == "" {
			root = devPersistDir
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", fault.Wrapf(err, fault.Internal, op, "resolve persist dir %q", root)
		}
		return filepath.Join(abs, "process"), nil
	}
	cache := cfg.cacheDir
	if cache == "" {
		var err error
		if cache, err = os.UserCacheDir(); err != nil {
			return "", fault.Wrapf(err, fault.Internal, op, "locate the user cache dir")
		}
	}
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fault.Wrapf(err, fault.Internal, op, "resolve project dir %q", projectDir)
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(cache, "funcd", "dev", hex.EncodeToString(sum[:])[:16], "process"), nil
}

// resolvePersistPlan computes the durable-driver layout from the flags (ADR-0125 Decision 7): under --persist every
// service gets a subdir of the persist root, else every one stays in memory. Pure + side-effect-free (no dirs
// created), so it is unit-testable.
func resolvePersistPlan(cfg devConfig) (persistPlan, error) {
	const op = "funcdctl dev"
	if !cfg.persist {
		return persistPlan{}, nil
	}
	root := cfg.persistTo
	if root == "" {
		root = devPersistDir
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return persistPlan{}, fault.Wrapf(err, fault.Internal, op, "resolve persist dir %q", root)
	}
	return persistPlan{
		storeDir:   filepath.Join(absRoot, "metastore"),
		kvDir:      filepath.Join(absRoot, "kv"),
		blobDir:    filepath.Join(absRoot, "blob"),
		catalogDir: filepath.Join(absRoot, "catalog"),
		sqlDir:     filepath.Join(absRoot, "sql"),
		bundleDir:  filepath.Join(absRoot, "bundles"),
	}, nil
}

// buildPersistDrivers opens the durable drivers plan calls for and returns the funcd options that inject them,
// the KV + blob handles (for a restart test to drive directly), and a closer that releases every opened driver —
// invoked by the caller ONLY when the boot fails before funcd.New takes ownership (the platform Closes them on
// Shutdown otherwise). A plan with all-memory kinds is a no-op.
func buildPersistDrivers(op string, plan persistPlan) (opts []funcd.Option, kv kvstore.KV, bkt blob.Bucket, closeAll func(), err error) {
	var closers []func() error
	closeAll = func() {
		for i := len(closers) - 1; i >= 0; i-- {
			_ = closers[i]()
		}
	}
	fail := func(e error) (nilOpts []funcd.Option, nilKV kvstore.KV, nilB blob.Bucket, nilClose func(), retErr error) {
		closeAll()
		return nil, nil, nil, nil, e
	}

	if plan.storeDir != "" {
		if merr := os.MkdirAll(plan.storeDir, 0o700); merr != nil {
			return fail(fault.Wrapf(merr, fault.Internal, op, "create metastore dir %q", plan.storeDir))
		}
		eng, e := badgerstore.Open(plan.storeDir)
		if e != nil {
			return fail(fault.Wrapf(e, fault.KindOf(e), op, "open durable metastore at %q", plan.storeDir))
		}
		st := store.New(eng)
		closers = append(closers, st.Close)
		opts = append(opts, funcd.WithStore(st))
	}

	if plan.kvDir != "" {
		if merr := os.MkdirAll(plan.kvDir, 0o700); merr != nil {
			return fail(fault.Wrapf(merr, fault.Internal, op, "create kv dir %q", plan.kvDir))
		}
		k, e := kvbadger.Open(plan.kvDir)
		if e != nil {
			return fail(fault.Wrapf(e, fault.KindOf(e), op, "open durable KV at %q", plan.kvDir))
		}
		kv = k
		if c, ok := k.(io.Closer); ok {
			closers = append(closers, c.Close)
		}
		opts = append(opts, funcd.WithKVStore(k))
	}

	if plan.blobDir != "" {
		if merr := os.MkdirAll(plan.blobDir, 0o700); merr != nil {
			return fail(fault.Wrapf(merr, fault.Internal, op, "create blob dir %q", plan.blobDir))
		}
		b, e := gocloud.Open(context.Background(), gocloud.FileURL(plan.blobDir))
		if e != nil {
			return fail(fault.Wrapf(e, fault.KindOf(e), op, "open durable blob at %q", plan.blobDir))
		}
		bkt = b
		closers = append(closers, b.Close)
		opts = append(opts, funcd.WithBlob(b))
	}

	return opts, kv, bkt, closeAll, nil
}

// tagStem extracts the tag stem of a workflow step's `function.image` (ADR-0125 Decision 8, ADR-0247 Decision 5): the
// segment after the last ':' (oci-layout:///…/registry:ingest → ingest), which names the build dev runs. A
// digest-pinned or tag-less ref has no stem to resolve from.
func tagStem(op, image string) (string, error) {
	if strings.Contains(image, "@") {
		return "", fault.Invalidf(op, "image %q is digest-pinned and has no :tag naming a build", image)
	}
	idx := strings.LastIndex(image, ":")
	if idx < 0 || idx == len(image)-1 {
		return "", fault.Invalidf(op, "image %q has no :tag naming a build", image)
	}
	tag := image[idx+1:]
	if strings.ContainsAny(tag, "/@") {
		return "", fault.Invalidf(op, "image %q tag %q is not a resolvable stem", image, tag)
	}
	return tag, nil
}

// devBlobWriterRole is the name of the write-only Role `funcdctl dev` provisions to make each dev function
// a member of every prefix's `writers` set (ADR-0128 as amended → ADR-0136), without widening reads.
const devBlobWriterRole v1.ObjectName = "dev-blob-writer"

// privateBundleDir returns the dir of an isolated function's bundle: a new temp dir, or under --persist (bundleRoot
// set) bundleRoot/<name>-<first 8 hex digits of the sha256 of the absolute srcDir>, kept across runs as state, since
// a later run's serving Revision still names it (#1053). The digest keeps two projects that share a --persist-to
// apart, so a session that fails on the other's lock never rewrites its bundle.
func privateBundleDir(op, bundleRoot string, name v1.ObjectName, srcDir string) (dir string, kept bool, err error) {
	if bundleRoot == "" {
		if dir, err = os.MkdirTemp("", "funcdctl-dev-fn-"); err != nil {
			return "", false, fault.Wrapf(err, fault.Internal, op, "create bundle temp dir")
		}
		return dir, false, nil
	}
	abs, err := filepath.Abs(srcDir)
	if err != nil {
		return "", false, fault.Wrapf(err, fault.Internal, op, "resolve %s", srcDir)
	}
	sum := sha256.Sum256([]byte(abs))
	dir = filepath.Join(bundleRoot, string(name)+"-"+hex.EncodeToString(sum[:])[:8])
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fault.Wrapf(err, fault.Internal, op, "create bundle dir %q", dir)
	}
	return dir, true, nil
}

// linkSources links every file of srcDir but its contract dotfile into bundleDir, by absolute path, since srcDir is
// relative to the working directory when the project is named as `.`. A link a kept bundle (--persist) already holds
// stays; any other file at its name is replaced.
func linkSources(op, srcDir, bundleDir string) error {
	srcDir, err := filepath.Abs(srcDir)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "resolve %s", srcDir)
	}
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "read %s", srcDir)
	}
	for _, e := range entries {
		if e.Name() == devContractFile {
			continue
		}
		target, link := filepath.Join(srcDir, e.Name()), filepath.Join(bundleDir, e.Name())
		if cur, rerr := os.Readlink(link); rerr == nil && cur == target {
			continue
		}
		_ = os.Remove(link)
		if lerr := os.Symlink(target, link); lerr != nil {
			return fault.Wrapf(lerr, fault.Internal, op, "link %s into the bundle", e.Name())
		}
	}
	return nil
}

// devWorkflowContracts is the `funcdctl dev` workflow ContractResolver (ADR-0125): from-source steps have
// no OCI metadata to inspect, so every step is reported untyped (an empty contract). The F65 typed-edge
// gate then no-ops and the Workflow reaches Ready; each step still enforces its own delivered contract at
// the shim (FUNCD_CONTRACT_PATH, ADR-0123) — the dev fidelity trade is only the cross-step typed-edge check.
type devWorkflowContracts struct{}

// Contract reports every from-source step as untyped (empty contract, no digest, no error).
func (devWorkflowContracts) Contract(_ context.Context, _ string) (v1.WorkflowContract, string, error) {
	return v1.WorkflowContract{}, "", nil
}

// orderFunctionsByLinks returns the Functions ordered so a link's target precedes the caller that binds
// it — admission enforces link-target existence, so a caller applied before its target would be rejected.
// A DFS post-order over the in-set link graph; a link cycle (mutual links) is broken arbitrarily (the
// platform's strict existence gate can't admit a cycle anyway — not a shape dev creates).
func orderFunctionsByLinks(fns []v1.Object) []v1.Object {
	inSet := make(map[v1.ObjectName]*v1.Function, len(fns))
	for _, o := range fns {
		if fn, ok := o.(*v1.Function); ok {
			inSet[fn.Name] = fn
		}
	}
	ordered := make([]v1.Object, 0, len(fns))
	const (
		visiting = 1
		done     = 2
	)
	state := map[v1.ObjectName]int{}
	var dfs func(fn *v1.Function)
	dfs = func(fn *v1.Function) {
		if state[fn.Name] != 0 { // done or visiting (cycle) — don't recurse again
			return
		}
		state[fn.Name] = visiting
		for _, l := range fn.Spec.Bindings.Links {
			if dep, ok := inSet[l.Target]; ok {
				dfs(dep)
			}
		}
		state[fn.Name] = done
		ordered = append(ordered, fn)
	}
	for _, o := range fns {
		if fn, ok := o.(*v1.Function); ok {
			dfs(fn)
		}
	}
	return ordered
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// setMeta stamps the shared namespace/resource-group and the dev ownership tag onto a synthesized resource.
func setMeta(meta *v1.ObjectMeta, name v1.ObjectName) {
	meta.Name = name
	meta.Namespace = devNamespace
	meta.ResourceGroup = devResourceGroup
	meta.Tags = v1.Tags{devManagedTag: devManagedBy}
}

// devInterpreter resolves a runtime interpreter (ADR-0247 Decision 9): the envKey override, else pathName on PATH (`uv
// run funcdctl dev .` puts the project's virtualenv first). "" ⇒ none found.
func devInterpreter(envKey, pathName string) string {
	if v := envOr(envKey, ""); v != "" {
		return v
	}
	if p, err := exec.LookPath(pathName); err == nil {
		return p
	}
	return ""
}

// devShimOptions extracts the embedded Node + Python runtime shims to a temp dir and returns the
// funcd options that launch them on the process runtime (mirroring cmd/funcd's process-mode wiring).
// A default shim (node when present, else python) is always registered so the reconciler's
// materializer gate is satisfied; at least one runtime must be on PATH (or FUNCD_NODE/FUNCD_PYTHON).
// A python that cannot import the shim is never registered, as in the daemon; when the run has a
// python handler (needPython), a missing python or one that cannot load the shim is a startup error;
// when it has a handler for the default (node) shim (needNode), a missing node is.
func devShimOptions(ctx context.Context, op string, needPython, needNode bool) (_ []funcd.Option, cleanup func(), err error) {
	dir, derr := os.MkdirTemp("", "funcdctl-dev-shim")
	if derr != nil {
		return nil, nil, fault.Wrapf(derr, fault.Internal, op, "create shim temp dir")
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	defer func() {
		if err != nil { // an error return has already set cleanup to nil
			_ = os.RemoveAll(dir)
		}
	}()

	var opts []funcd.Option
	haveDefault := false

	node := devInterpreter("FUNCD_NODE", "node")
	if node == "" && needNode {
		return nil, nil, fault.NotFoundf(op, "no node interpreter found for the node handler (need node on PATH; set FUNCD_NODE)")
	}
	if node != "" {
		shimPath := filepath.Join(dir, "shim.mjs")
		if werr := os.WriteFile(shimPath, shimnode.Shim, 0o600); werr != nil {
			return nil, nil, fault.Wrapf(werr, fault.Internal, op, "extract node shim")
		}
		opts = append(opts, funcd.WithRuntimeShim(node, shimPath))
		haveDefault = true
	}

	python := devInterpreter("FUNCD_PYTHON", "python3")
	if python == "" && needPython {
		return nil, nil, fault.NotFoundf(op, "no python interpreter found for the python handler (need python3 on PATH; set FUNCD_PYTHON)")
	}
	if python != "" {
		shimEntry, _, perr := shimpython.Extract(filepath.Join(dir, "shim-python"))
		if perr != nil {
			return nil, nil, fault.Wrapf(perr, fault.Internal, op, "extract python shim")
		}
		switch reason := process.PythonShimLoadError(ctx, python, filepath.Dir(shimEntry)); {
		case reason == "":
			opts = append(opts, funcd.WithRuntimeShimFor("python", python, shimEntry))
			if !haveDefault {
				opts = append(opts, funcd.WithRuntimeShim(python, shimEntry))
				haveDefault = true
			}
		case needPython:
			return nil, nil, fault.Invalidf(op, "python %s cannot load the runtime shim (needs Python ≥3.12 with fastjsonschema; set FUNCD_PYTHON): %s", python, reason)
		}
	}

	if !haveDefault {
		return nil, nil, fault.NotFoundf(op, "no runtime found on PATH (need node or python3; set FUNCD_NODE / FUNCD_PYTHON) — funcdctl dev runs the handler from source")
	}
	return opts, cleanup, nil
}
