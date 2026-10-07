//go:build dev

// dev.go is the `-tags dev` implementation of `funcdctl dev` (ADR-0125): run a function locally,
// from source, with zero hand-written resource CRDs. It embeds the real platform via
// `funcd.InMemory()` (memory store/KV, mem:// blob, in-proc NATS, the PROCESS runtime) and drives the
// real reconcile → materialize → shim → invoke path — only the driver implementations are the
// in-memory ones, so fidelity is inherited from the ports, not faked (Decision, "reuse don't rebuild").
//
// PHASE 1 (the hermetic core): a single function, run from the working tree (FUNCD_BUNDLE_DIR = the
// manifest's dir), with the ADR-0123 contract enforced (FUNCD_CONTRACT_PATH) and the KV/Bucket/
// ConfigMap/Secret resources its bindings imply auto-provisioned.
//
// PHASE 2 (this file also): the ADR-0080/0085 S3 frontend is exposed over the blob substrate so the
// author inspects blob artifacts with the same tools as prod (`aws s3 ls`, DuckDB `read_parquet`) —
// dev creds are the function's own derived keypair over a fixed dev master (Decision 6); and
// `--persist [--persist-to DIR]` swaps the memory store/KV/blob for durable-local Badger + fileblob
// under per-service subdirs (`<DIR>/{metastore,kv,blob}/`, Decision 7), so state survives a restart.
// Ephemeral (memory) stays the default; secrets are NEVER served from disk — they are re-read from
// ${ENV} every boot (a missing var fails fast even under --persist).
//
// PHASE 3 (this file also): the WORKFLOW + function-selection surface (ADR-0125 Decisions 8-9).
// `funcdctl dev workflow.yaml` loads a Workflow CRD as the DAG: each step's `function.image` tag stem
// (registry:ingest → ingest) resolves to `<stem>.funcdctl.yaml` (+ handler) via ADR-0124, is synthesized
// as a from-source Function, and the step is rewritten to dispatch to it by `function.ref` — so the real
// embedded workflow engine runs the DAG with no OCI pull. A directory with several `<stem>.funcdctl.yaml`
// files runs ALL of them (each its own single-file function); `funcdctl dev <stem>` selects one. fn-to-fn
// `spec.links` are restored on every synthesized Function so `context.invoke("<alias>", …)` resolves
// in-process via the UDS local API (an UNKNOWN alias → Forbidden, the ADR-0064/0075 default-deny — a
// preserved dev feature).
//
// PHASE 4 (this file also): a `catalogs` binding is served by the embedded process-mode DuckDB+Quack
// engine (internal/catalog/devengine, injected as the CatalogService provider) — no container, no cgo
// (Decision 5). Dev-UX polish rounds it out: a lipgloss services banner, real-time colorized function
// log streaming (funcd.WithLogObserver), and --gport/--s3port for reproducible URLs.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

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
	"github.com/pyvvo/funcd/internal/store"
	badgerstore "github.com/pyvvo/funcd/internal/store/badger"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

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
	// devApplyAttempts bounds the re-apply of one resource that loses its update with a Conflict.
	devApplyAttempts = 5
)

// envRef matches a ${ENV_VAR} reference in a dev.secrets value (ADR-0125 Decision 4): secret values are
// NEVER read from the file, only from the process environment.
//
//nolint:gochecknoglobals // a compiled, immutable regexp
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// devCmd runs a function locally from source (ADR-0125). Only compiled under `-tags dev` (the release
// build gets the rebuild-hint stub in dev_stub.go), so the platform embed never fattens release
// `funcdctl`.
func (a *cli) devCmd() *cobra.Command {
	var entry string
	var cfg devConfig
	cmd := &cobra.Command{
		Use:   "dev [path]",
		Short: "Run a function locally from source (zero CRDs) on the embedded in-memory platform",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			// --print-env: emit the dev S3 creds as `export …` lines and exit, no server (for
			// `eval "$(funcdctl dev <target> --s3port <p> --print-env)"`).
			if cfg.printEnv {
				return a.printDevEnv(path, entry, cfg)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), stopsignal.Signals()...)
			defer stop()

			inst, err := a.startDev(ctx, path, entry, cfg)
			if err != nil {
				return err
			}
			if werr := a.printBanner(inst); werr != nil {
				return werr
			}
			<-ctx.Done()
			return inst.stop()
		},
	}
	cmd.Flags().StringVar(&entry, "entry", "",
		"handler entry file relative to the manifest dir (default: handler.py for python*, handler.mjs otherwise)")
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
	cmd.Flags().StringVar(&cfg.name, "name", "",
		"function name for a single generic funcdctl.yaml (default: the dir name); a <stem>.funcdctl.yaml always names by stem")
	cmd.Flags().BoolVar(&cfg.printEnv, "print-env", false,
		"print the dev S3 credentials as `export …` lines and exit (no server); pair with a fixed --s3port. "+
			"Use: eval \"$(funcdctl dev <target> --s3port 3006 --print-env)\"")
	return cmd
}

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
			"bound: "+strings.Join(inst.catalogs, ", ")+" · DuckLake over blob"))
	}

	lines = append(lines, "", headerS.Render("FUNCTIONS"))
	for _, fn := range inst.functions {
		lines = append(lines, descS.Render("POST")+"  "+urlS.Render(inst.gatewayURL+"/function/"+fn))
	}

	if inst.workflow != "" {
		// Phase 3: a workflow run is triggered by applying a WorkflowRun for it (the same surface a Sensor
		// or `funcdctl workflow run` uses); its steps run from source as ordinary invocable functions.
		lines = append(lines, "", headerS.Render("WORKFLOW"),
			urlS.Render(inst.workflow)+subS.Render(fmt.Sprintf("   %d step functions from source · apply a WorkflowRun to trigger",
				len(inst.functions))))
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
		s.timeS.Render(l.Time.Format("15:04:05")),
		s.funcS.Render(l.Function),
		sevS.Render(sev),
		s.bodyS.Render(termSafe(l.Body)),
		s.attrS.Render(attrs.String()),
	)
}

// devConfig carries the `funcdctl dev` flags that shape the substrate (ADR-0125 Decision 7). The zero
// value is the ephemeral in-memory default; persist opts into durable-local drivers.
type devConfig struct {
	persist   bool   // swap memory store/KV/blob for durable-local Badger + fileblob
	persistTo string // the --persist state dir (empty ⇒ devPersistDir)
	gport     int    // fixed gateway (data-plane) port; 0 ⇒ a random free port
	s3port    int    // fixed S3-frontend port; 0 ⇒ a random free port
	cport     int    // fixed control-plane port (apply / workflow run); 0 ⇒ a random free port
	name      string // override the single-generic-funcdctl.yaml function name; "" ⇒ the dir basename
	printEnv  bool   // print the dev S3 creds as `export …` lines and exit (no server)
	// pickS3Addr picks the S3-frontend address when s3port is 0; nil ⇒ freeLocalAddr. A test hands out a taken one (#627).
	pickS3Addr func() (string, error)
	// cacheDir holds the state dir without --persist (ADR-0167); "" ⇒ os.UserCacheDir. A test passes its own.
	cacheDir string
}

// devInstance is a running `funcdctl dev` platform + the seams a test (or the command) drives it by.
type devInstance struct {
	platform   *funcd.Platform
	client     *sdk.Client
	gatewayURL string   // the data-plane base URL functions are invoked at (POST <url>/function/<name>)
	controlURL string   // the control-plane base URL (funcdctl --server: apply, workflow run)
	functions  []string // the loaded function names (one function, or a workflow's step functions)
	workflow   string   // the loaded Workflow name (Phase 3, `funcdctl dev workflow.yaml`); "" for a function run
	catalogs   []string // bound catalog aliases served by the embedded duckdb+quack engine (Phase 4); nil if none
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
	logger    *slog.Logger  // funcdctl dev's own warnings, in the daemon's text form over the cli's out (ADR-0197)
	cleanup   []func()
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

// startDev is the entry seam tests drive (no signal handling, no blocking). It classifies the path arg —
// a Workflow CRD (`funcdctl dev workflow.yaml`), a directory or stem of function manifests, or a single
// function — then boots the embedded platform, synthesizes + applies the desired state (Functions run
// from source + the KV/Bucket/ConfigMap/Secret/links each implies + an optional Workflow DAG), and starts
// serving. It returns once the platform is running (resources reconcile asynchronously to Ready); the
// caller cancels ctx to stop.
func (a *cli) startDev(ctx context.Context, path, entryFlag string, cfg devConfig) (*devInstance, error) {
	const op = "funcdctl dev"
	lg, err := observability.NewLogger(observability.Config{Format: observability.FormatText}, a.out)
	if err != nil {
		return nil, err
	}
	// Stamp the path before it is read, so an edit to a workflow file racing the boot still reloads it.
	stamp, _ := fileStamp(path)
	// Phase 3 (Decision 8): a Workflow CRD is the DAG. Detect it FIRST — a workflow.yaml is not a
	// funcdctl.yaml, so it must not be run through the function resolver.
	if wf, isWorkflow, derr := detectWorkflow(op, path); derr != nil {
		return nil, derr
	} else if isWorkflow {
		return a.startDevWorkflow(ctx, op, &devWorkflow{path: path, obj: wf, seen: stamp}, cfg, lg.Root())
	}
	// Phase 3 (Decision 9): resolve the function set — all `<stem>.funcdctl.yaml` in a dir, one by stem,
	// or the single generic funcdctl.yaml (the Phase-1/2 path, unchanged).
	pfs, rerr := resolveDevFunctions(op, path, entryFlag, cfg.name)
	if rerr != nil {
		return nil, rerr
	}
	return a.bootDev(ctx, op, pfs, nil, cfg, lg.Root())
}

// startDevWorkflow runs a Workflow CRD locally from source (ADR-0125 Decision 8). Each `function.image`
// step's tag stem (registry:ingest → ingest) resolves to `<stem>.funcdctl.yaml` (+ handler) in the
// workflow's dir (ADR-0124); that manifest is synthesized as a from-source Function and the step is
// REWRITTEN to dispatch to it by `function.ref` — so the real embedded workflow engine runs the DAG
// with no OCI pull (the materializer only materializes image steps; a ref step targets an existing
// Function directly). builtin (wait/pass) and pre-existing ref steps run as-is.
func (a *cli) startDevWorkflow(ctx context.Context, op string, wf *devWorkflow, cfg devConfig, logger *slog.Logger) (*devInstance, error) {
	pfs, err := resolveWorkflowPlan(op, wf.path, wf.obj)
	if err != nil {
		return nil, err
	}
	inst, berr := a.bootDev(ctx, op, pfs, wf, cfg, logger)
	if berr != nil {
		return nil, berr
	}
	inst.workflow = string(wf.obj.Name)
	return inst, nil
}

// resolveWorkflowPlan resolves a Workflow CRD's steps to from-source plannedFuncs (Decision 8): each
// function.ref/image step's stem → <stem>.funcdctl.yaml, synthesized as a from-source Function with the
// step REWRITTEN to dispatch by ref, and lands wf in the dev namespace/group. Factored from the boot path
// so `--print-env` resolves the same first-function identity (the S3 keypair's subject) without booting.
func resolveWorkflowPlan(op, path string, wf *v1.Workflow) ([]plannedFunc, error) {
	dir := filepath.Dir(path)
	var pfs []plannedFunc
	seen := map[v1.ObjectName]bool{}
	for i := range wf.Spec.Steps {
		st := &wf.Spec.Steps[i]
		if st.Function == nil {
			continue // builtin / sub-workflow steps need no from-source materialization
		}
		// Resolve the step's function to a <stem>.funcdctl.yaml in the workflow dir: a function.ref names
		// the manifest stem directly; a function.image resolves by its tag stem (Decision 8).
		var stem string
		switch {
		case st.Function.Ref != "":
			stem = string(st.Function.Ref)
		case st.Function.Image != "":
			s, terr := tagStem(op, st.Function.Image)
			if terr != nil {
				return nil, fault.Wrapf(terr, fault.KindOf(terr), op, "workflow step %q", st.Name)
			}
			stem = s
		default:
			continue
		}
		manifestPath := filepath.Join(dir, stem+"."+manifestFileName)
		m, lerr := loadManifestAt(op, manifestPath)
		if lerr != nil {
			return nil, fault.Wrapf(lerr, fault.KindOf(lerr), op, "workflow step %q resolves to %s", st.Name, filepath.Base(manifestPath))
		}
		name := sanitizeName(stem)
		if !seen[name] {
			srcDir, entry, isolate := manifestEntry(m, dir, stemEntry(stem, m.Runtime), true)
			pfs = append(pfs, plannedFunc{m: m, name: name, srcDir: srcDir, entry: entry, isolate: isolate, manifestPath: manifestPath})
			seen[name] = true
		}
		// Rewrite the step to dispatch to the from-source Function (ADR-0125): drop any OCI image, target
		// the synthesized Function by ref (same namespace). The engine dispatches to it without pulling.
		st.Function.Image = ""
		st.Function.Ref = name
	}
	if len(pfs) == 0 {
		return nil, fault.Invalidf(op, "workflow %q has no function.ref/image steps resolving to a <stem>.funcdctl.yaml", wf.Name)
	}
	// Land the workflow in the dev-authorized namespace/group so the run engine + link resolver reach it.
	wf.TypeMeta = v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow}
	wf.Namespace = devNamespace
	if wf.ResourceGroup == "" {
		wf.ResourceGroup = devResourceGroup
	}
	if wf.Tags == nil {
		wf.Tags = v1.Tags{}
	}
	wf.Tags[devManagedTag] = devManagedBy
	return pfs, nil
}

// resolveDevPlan resolves a dev target (workflow, dir, stem, or single manifest) to its plannedFuncs
// WITHOUT booting — the shared front half of startDev, reused by `--print-env`.
func resolveDevPlan(op, path, entryFlag string, cfg devConfig) ([]plannedFunc, error) {
	if wf, isWorkflow, derr := detectWorkflow(op, path); derr != nil {
		return nil, derr
	} else if isWorkflow {
		return resolveWorkflowPlan(op, path, wf)
	}
	return resolveDevFunctions(op, path, entryFlag, cfg.name)
}

// printDevEnv resolves the target's first function and prints the dev S3 credentials as `export …` lines
// to stdout, then returns — NO server is booted. The keypair is the FIXED devS3Master derived over that
// function's identity (the same keypair the banner shows; deterministic across restarts, ADR-0128
// Decision 6), so `eval "$(funcdctl dev <target> --s3port <p> --print-env)"` loads working creds. The
// endpoint port is --s3port (default 3006) and must match the running daemon's --s3port.
func (a *cli) printDevEnv(path, entryFlag string, cfg devConfig) error {
	const op = "funcdctl dev --print-env"
	pfs, err := resolveDevPlan(op, path, entryFlag, cfg)
	if err != nil {
		return err
	}
	kp := s3gateway.DeriveKeypair([]byte(devS3Master), v1.KindFunction, devNamespace, string(pfs[0].name))
	port := cfg.s3port
	if port == 0 {
		port = 3006
	}
	_, werr := fmt.Fprintf(a.out,
		"export AWS_ACCESS_KEY_ID=%s\nexport AWS_SECRET_ACCESS_KEY=%s\nexport AWS_REGION=%s\nexport AWS_ENDPOINT_URL_S3=http://127.0.0.1:%d\n",
		kp.AccessKey, kp.SecretKey, devS3Region, port)
	return werr
}

// catalogAliases returns the distinct catalog binding aliases across the planned functions (sorted) —
// the trigger for wiring the dev process-mode catalog engine (ADR-0125 Decision 5) and what the
// banner lists as served catalogs. Empty ⇒ no catalog is bound.
func catalogAliases(pfs []plannedFunc) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, pf := range pfs {
		for _, c := range pf.m.Bindings.Catalogs {
			if _, ok := seen[c.Alias]; !ok {
				seen[c.Alias] = struct{}{}
				out = append(out, c.Alias)
			}
		}
	}
	sort.Strings(out)
	return out
}

// bootDev is the shared boot path for a function set (single, multi, or a workflow's step functions): it
// synthesizes the resources (resolving env secrets FAIL-FAST before any side effect), delivers each
// function's bundle + ADR-0123 contract, boots the embedded platform with the extracted shims + the S3
// frontend + the durable/ephemeral drivers, then applies the resources, then the Functions, then the
// Workflow (nil for a function set). Over a durable metastore it then deletes what an earlier session
// synthesized that the manifests no longer name. It returns once serving; the caller cancels ctx to stop.
func (a *cli) bootDev(ctx context.Context, op string, pfs []plannedFunc, wf *devWorkflow, cfg devConfig, logger *slog.Logger) (_ *devInstance, err error) {
	if len(pfs) == 0 {
		return nil, fault.NotFoundf(op, "no function to run")
	}
	// Resources first: this resolves each dev.secret from ${ENV} and fails fast on a missing var BEFORE
	// any file/boot side effect (so a bad manifest leaves nothing behind).
	resObjs, rerr := synthesizeResources(op, pfs)
	if rerr != nil {
		return nil, rerr
	}
	plan, perr := resolvePersistPlan(cfg, pfs[0].m)
	if perr != nil {
		return nil, perr
	}
	// The durable state dirs change while the session runs, so the hot-reload watcher never walks them.
	stateDirs := []string{plan.storeDir, plan.kvDir, plan.blobDir, plan.catalogDir}

	inst := &devInstance{runErr: make(chan error, 1), logger: logger}
	for _, pf := range pfs {
		inst.functions = append(inst.functions, string(pf.name))
	}
	// On any error after this point, run the cleanups we accumulated so a failed boot leaves nothing behind.
	defer func() {
		if err != nil {
			inst.unwind(0)
		}
	}()

	fnObjs, handlers, herr := prepareHandlers(op, pfs, stateDirs, inst)
	if herr != nil {
		return nil, herr
	}
	cancelRun, perr := a.startPlatform(ctx, op, pfs, plan, cfg, inst)
	if perr != nil {
		return nil, perr
	}
	// A boot that fails once Run is up stops the platform (Run's Shutdown closes its drivers) before the
	// cleanups run, so no controller outlives the boot on a released driver.
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
	applied, aerr := applyBoot(ctx, op, client, resObjs, fnObjs, wf, plan, inst.logger)
	if aerr != nil {
		return nil, aerr
	}
	inst.watchDone = make(chan struct{})
	go watchHandlers(ctx, op, client, handlers, wf, applied, stateDirs, inst.logger, inst.watchDone)
	return inst, nil
}

// devS3PortAttempts bounds how many S3 ports startPlatform picks before it gives up (#627).
const devS3PortAttempts = 3

// startPlatform builds and runs the embedded platform and waits until its S3 frontend serves; it returns the cancel of
// the platform's Run. The gateway binds its own listener only when the platform runs (versitygw cannot take an open
// one), so a port picked for it (--s3port 0) can be taken by another process in between: that attempt is stopped and
// unwound, and the platform starts again on a fresh port (#627). A fixed --s3port is never replaced.
func (a *cli) startPlatform(ctx context.Context, op string, pfs []plannedFunc, plan persistPlan, cfg devConfig, inst *devInstance) (context.CancelFunc, error) {
	for attempt := 1; ; attempt++ {
		mark := len(inst.cleanup)
		opts, oerr := a.devPlatformOptions(ctx, op, pfs, plan, cfg, inst)
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

// prepareHandlers delivers each function's bundle + ADR-0123 contract and synthesizes its Function (run from
// source, links restored), registering each bundle's cleanup on inst. A stem/workflow function is isolated in a
// private temp bundle so per-function contracts never collide when several single-file functions share a dir
// (ADR-0124). Admission enforces link-target existence, so the Functions come back in link order: a link's target
// before the caller that binds it.
func prepareHandlers(op string, pfs []plannedFunc, stateDirs []string, inst *devInstance) ([]v1.Object, []*devHandler, error) {
	var fnObjs []v1.Object
	handlers := make([]*devHandler, 0, len(pfs))
	for _, pf := range pfs {
		contractBlob, cerr := artifact.ContractBlob(pf.m.Contract.Input, pf.m.Contract.Output)
		if cerr != nil {
			return nil, nil, fault.Wrapf(cerr, fault.KindOf(cerr), op, "build contract for %s", pf.name)
		}
		// Fingerprint before the bundle is read, so an edit racing the boot still triggers a reload.
		h := &devHandler{pf: pf}
		fp, ferr := h.fingerprint(stateDirs)
		imagePath, cleanup, berr := prepareBundle(op, pf, contractBlob)
		if berr != nil {
			return nil, nil, berr
		}
		inst.cleanup = append(inst.cleanup, cleanup)
		if ferr != nil {
			return nil, nil, fault.Wrapf(ferr, fault.Internal, op, "fingerprint the sources of %s", pf.name)
		}
		h.bundle, h.seen = imagePath, fp
		fn := synthesizeFunction(pf, imagePath)
		fn.Spec.ImageDigest = fp
		fnObjs = append(fnObjs, fn)
		handlers = append(handlers, h)
	}
	return orderFunctionsByLinks(fnObjs), handlers, nil
}

// devPlatformOptions builds the embedded platform's options: the extracted shims, the catalog dev engine, the
// function log stream, the fixed listen ports, the durable/ephemeral drivers and the S3 frontend. It registers
// each cleanup on inst and closes the durable drivers itself when it fails after opening them; funcd.New owns them
// once it is called.
func (a *cli) devPlatformOptions(ctx context.Context, op string, pfs []plannedFunc, plan persistPlan, cfg devConfig, inst *devInstance) ([]funcd.Option, error) {
	// The interpreter config (dev.python/dev.node) is GLOBAL per run; the first function's block is
	// representative (like dev.backends). Its relative path resolves against that manifest's dir.
	devBlock, baseDir := pfs[0].m.Dev, filepath.Dir(pfs[0].manifestPath)
	isPython := func(pf plannedFunc) bool { return strings.HasPrefix(string(pf.m.Runtime), "python") }
	needPython := slices.ContainsFunc(pfs, isPython)
	needNode := slices.ContainsFunc(pfs, func(pf plannedFunc) bool { return !isPython(pf) })
	shimOpts, shimCleanup, sherr := devShimOptions(ctx, op, devBlock, baseDir, needPython, needNode)
	if sherr != nil {
		return nil, sherr
	}
	inst.cleanup = append(inst.cleanup, shimCleanup)

	// The process driver and the catalog engine save what they start in the state dir and reap what a crashed run
	// left there before anything starts (ADR-0167).
	stateDir, sderr := devStateDir(cfg, baseDir)
	if sderr != nil {
		return nil, sderr
	}
	rt, rerr := process.Open(ctx, stateDir, 0, slog.Default())
	if rerr != nil {
		return nil, fault.Wrapf(rerr, fault.KindOf(rerr), op, "open the process runtime in %s", stateDir)
	}
	inst.cleanup = append(inst.cleanup, func() { _ = rt.Close() })

	opts := []funcd.Option{
		funcd.InMemory(),
		funcd.WithRuntime(rt),
		funcd.WithMaterializer(function.NewFileMaterializer()),
		// From-source workflow steps carry no OCI artifact, so the production F65 resolver can never
		// inspect their file:// bundles. Inject an untyped resolver so the typed-edge gate is a no-op and
		// the Workflow reaches Ready (each step still enforces its OWN contract at the shim, ADR-0123).
		// Harmless for a single-function run (no Workflow is applied).
		funcd.WithWorkflowContractResolver(devWorkflowContracts{}),
		// Dev blob writes run the REAL prod single-writer authz (ADR-0128 as amended 2026-07-14): rather than
		// dropping the forbid, synthesizeResources auto-provisions a Blob Data Writer RolesAssignment (ADR-0136)
		// per dev function, so the dev principal is a legit writer. Seeding a no-owner `landing` and a producer
		// writing a binding-inferred prefix both pass the forbid; an unassigned principal is still denied.
	}
	catOpts, caterr := devCatalogOptions(plan, stateDir, pfs, inst)
	if caterr != nil {
		return nil, caterr
	}
	opts = append(opts, catOpts...)

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

	// Durable-local drivers (ADR-0125 Decision 7): under --persist (or a `dev.backends` file:// override)
	// the memory store/KV/blob are swapped for Badger + fileblob under per-service subdirs. dev.backends is
	// GLOBAL per kind, so the first function's block is representative. The platform takes ownership +
	// Closes them on Shutdown; they are closed here only if the options fail BEFORE funcd.New takes ownership.
	persistOpts, kv, bkt, closeDurable, derr := buildPersistDrivers(op, cfg, pfs[0].m)
	if derr != nil {
		return nil, derr
	}
	opts = append(opts, persistOpts...)
	inst.kv, inst.blob = kv, bkt

	// S3 frontend (ADR-0080/0085, Decision 6): expose the blob substrate over the S3 protocol on a free
	// node-private port. The printed creds are the FIRST function's derived keypair (representative).
	s3Cleanup, s3err := devS3Options(op, &opts, string(pfs[0].name), cfg, inst)
	if s3err != nil {
		if closeDurable != nil {
			closeDurable()
		}
		return nil, s3err
	}
	inst.cleanup = append(inst.cleanup, s3Cleanup)
	return append(opts, shimOpts...), nil
}

// devCatalogOptions wires the catalog dev engine (Decision 5) when any function binds a catalog: the process-mode
// DuckDB+Quack engine driver (internal/catalog/devengine) becomes the CatalogService add-on-provider runtime — it
// runs the go:embed'd engine as a host subprocess (no container, no cgo). A dev binary built WITHOUT the engine
// (the committed placeholder) reports catalog-unavailable at reconcile time, not here — so `funcdctl dev` still
// boots. It records the served catalogs and registers each cleanup on inst; no catalog binding ⇒ no options.
func devCatalogOptions(plan persistPlan, stateDir string, pfs []plannedFunc, inst *devInstance) ([]funcd.Option, error) {
	aliases := catalogAliases(pfs)
	if len(aliases) == 0 {
		return nil, nil
	}
	inst.catalogs = aliases
	// Under --persist, the DuckLake SQLite catalog lives in a durable per-provider dir (mirroring the
	// metastore) so a dev restart reopens it; ephemeral otherwise. resolvePersistPlan is pure, so the
	// second call in buildPersistDrivers is harmless.
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

// applyBoot applies the boot's desired state in order: the backing resources (KVStore/Bucket/ConfigMap/Secret) →
// the Functions (bind them) → the KVStores and Buckets that drop a table or prefix (stageResources) → the Workflow
// of a workflow run (wf, nil otherwise; it references its step Functions). ADR-0121's reconcile-time existence gate
// resolves against what is already applied. Over a durable metastore it then prunes what an earlier session
// synthesized that the manifests no longer name. It returns the resources the hot-reload watcher starts from.
func applyBoot(ctx context.Context, op string, c *sdk.Client, resObjs, fnObjs []v1.Object, wf *devWorkflow, plan persistPlan, logger *slog.Logger) ([]v1.Object, error) {
	firstRes, lastRes, serr := stageResources(ctx, op, c, resObjs)
	if serr != nil {
		return nil, serr
	}
	var extraObjs []v1.Object
	if wf != nil {
		extraObjs = []v1.Object{wf.obj}
		wf.applied = append(slices.Clone(fnObjs), wf.obj)
	}
	for _, group := range [][]v1.Object{firstRes, fnObjs, lastRes, extraObjs} {
		for _, obj := range group {
			if aerr := applyDesired(ctx, c, obj); aerr != nil {
				return nil, fault.Wrapf(aerr, fault.KindOf(aerr), op, "apply %s %q", obj.GroupVersionKind().Kind, obj.GetName())
			}
		}
	}
	if plan.storeDir == "" {
		return resObjs, nil
	}
	stale, lerr := pruneStale(ctx, c, slices.Concat(resObjs, fnObjs, extraObjs), logger)
	if lerr != nil {
		return nil, fault.Wrapf(lerr, fault.KindOf(lerr), op, "prune the objects an earlier session applied")
	}
	return slices.Concat(resObjs, stale), nil
}

// pruneStale deletes each object an earlier --persist session synthesized that desired no longer holds, as a
// reload prunes them (ADR-0125: the manifests are the session's desired state), and returns the ones that stay.
// Only an object carrying devManagedTag is the session's, so an object the user applied is never deleted. The
// kinds are listed in apply order, so a Workflow goes before its Functions and a Function before what it binds.
func pruneStale(ctx context.Context, c *sdk.Client, desired []v1.Object, logger *slog.Logger) ([]v1.Object, error) {
	var prev []v1.Object
	for _, kind := range []v1.Kind{
		v1.KindKVStore, v1.KindBucket, v1.KindSecret, v1.KindCatalogService, v1.KindConfigMap,
		v1.KindRole, v1.KindRolesAssignment, v1.KindFunction, v1.KindWorkflow,
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

// resolvePersistPlan computes the durable-driver layout from the flags + `dev.backends` (Decision 4/7),
// GLOBAL per kind. Precedence per kind: an explicit `dev.backends.<kind>` wins ("memory" ⇒ ephemeral even
// under --persist; a path / file:// ⇒ durable at that path); otherwise the default is memory, or a
// per-service subdir of the persist root under --persist. The metastore has no `dev.backends` knob — it is
// durable exactly when --persist is set. Pure + side-effect-free (no dirs created), so it is unit-testable.
func resolvePersistPlan(cfg devConfig, m *sdk.Manifest) (persistPlan, error) {
	const op = "funcdctl dev"
	root := cfg.persistTo
	if root == "" {
		root = devPersistDir
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return persistPlan{}, fault.Wrapf(err, fault.Internal, op, "resolve persist dir %q", root)
	}

	var p persistPlan
	if cfg.persist {
		p.storeDir = filepath.Join(absRoot, "metastore")
		p.catalogDir = filepath.Join(absRoot, "catalog")
	}

	kvDir, kerr := resolveBackendDir(op, m.Dev.Backends.KV, cfg.persist, absRoot, "kv")
	if kerr != nil {
		return persistPlan{}, kerr
	}
	p.kvDir = kvDir

	blobDir, berr := resolveBackendDir(op, m.Dev.Backends.Blob, cfg.persist, absRoot, "blob")
	if berr != nil {
		return persistPlan{}, berr
	}
	p.blobDir = blobDir
	return p, nil
}

// resolveBackendDir maps one `dev.backends` value to an absolute durable dir (or "" ⇒ keep memory):
// empty ⇒ a per-service subdir of the persist root under --persist, else memory; "memory" ⇒ always memory
// (explicit ephemeral, even under --persist); anything else is a path (a leading file:// is stripped),
// resolved absolute relative to the working dir.
func resolveBackendDir(op, backend string, persist bool, absRoot, service string) (string, error) {
	switch backend {
	case "":
		if persist {
			return filepath.Join(absRoot, service), nil
		}
		return "", nil
	case "memory":
		return "", nil
	default:
		raw := strings.TrimPrefix(backend, "file://")
		if raw == "" {
			return "", fault.Invalidf(op, "dev.backends.%s %q has no path", service, backend)
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", fault.Wrapf(err, fault.Internal, op, "resolve dev.backends.%s %q", service, backend)
		}
		return abs, nil
	}
}

// buildPersistDrivers opens the durable drivers the persistPlan calls for and returns the funcd options
// that inject them, the KV + blob handles (for a restart test to drive directly), and a closer that
// releases every opened driver — invoked by the caller ONLY when the boot fails before funcd.New takes
// ownership (the platform Closes them on Shutdown otherwise). A plan with all-memory kinds is a no-op.
func buildPersistDrivers(op string, cfg devConfig, m *sdk.Manifest) (opts []funcd.Option, kv kvstore.KV, bkt blob.Bucket, closeAll func(), err error) {
	plan, perr := resolvePersistPlan(cfg, m)
	if perr != nil {
		return nil, nil, nil, nil, perr
	}
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

// plannedFunc is one function `funcdctl dev` will run from source, resolved from the path arg (a dir of
// manifests, a stem, a single file, or a workflow step). isolate ⇒ materialize into a private temp bundle
// (a single-file function, so per-function contracts never collide when several share a dir, ADR-0124);
// !isolate ⇒ run in place in srcDir (the generic-funcdctl.yaml bundle path, Phase 1/2).
type plannedFunc struct {
	m            *sdk.Manifest
	name         v1.ObjectName
	srcDir       string // the dir holding the handler source
	entry        string // the handler entry filename within srcDir
	isolate      bool
	manifestPath string // the funcdctl.yaml (its dir is the base for a dev.python/dev.node relative path)
}

// detectWorkflow reports whether path is a Workflow CRD file (`funcdctl dev workflow.yaml`, Decision 8).
// A directory or a non-existent stem is never a workflow (those are function sets); only a file whose
// top-level `kind` is Workflow qualifies. A malformed Workflow file is a hard error (the user meant it).
func detectWorkflow(op, path string) (*v1.Workflow, bool, error) {
	info, serr := os.Stat(path)
	if serr != nil || info.IsDir() {
		return nil, false, nil
	}
	data, rerr := os.ReadFile(path) //nolint:gosec // path is a user-supplied CLI argument
	if rerr != nil {
		return nil, false, nil // unreadable ⇒ let the function resolver produce the error
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	if yaml.Unmarshal(data, &probe) != nil || probe.Kind != string(v1.KindWorkflow) {
		return nil, false, nil
	}
	// The same decode as `funcdctl apply`: bare y/n keys stay strings (#63), unknown keys fail (#64).
	obj, derr := sdk.DecodeManifest(data)
	if derr != nil {
		return nil, false, fault.Invalidf(op, "parse workflow %q: %v", path, derr)
	}
	wf, ok := obj.(*v1.Workflow)
	if !ok {
		return nil, false, fault.Invalidf(op, "parse workflow %q: decoded %T, want a Workflow", path, obj)
	}
	return wf, true, nil
}

// tagStem extracts the tag stem of a workflow step's `function.image` (Decision 8): the segment after the
// last ':' (oci-layout:///…/registry:ingest → ingest), which names the `<stem>.funcdctl.yaml` beside the
// workflow. A digest-pinned or tag-less ref has no stem to resolve from.
func tagStem(op, image string) (string, error) {
	if strings.Contains(image, "@") {
		return "", fault.Invalidf(op, "image %q is digest-pinned and has no :tag to resolve a <stem>.%s from", image, manifestFileName)
	}
	idx := strings.LastIndex(image, ":")
	if idx < 0 || idx == len(image)-1 {
		return "", fault.Invalidf(op, "image %q has no :tag to resolve a <stem>.%s from", image, manifestFileName)
	}
	tag := image[idx+1:]
	if strings.ContainsAny(tag, "/@") {
		return "", fault.Invalidf(op, "image %q tag %q is not a resolvable stem", image, tag)
	}
	return tag, nil
}

// resolveDevFunctions classifies the path arg into the function set to run (Decision 9): an existing
// directory enumerates its manifests (all `<stem>.funcdctl.yaml`, else the single generic funcdctl.yaml);
// an existing file resolves that one function; a non-existent arg is treated as a stem in the cwd.
func resolveDevFunctions(op, path, entryFlag, nameFlag string) ([]plannedFunc, error) {
	info, serr := os.Stat(path)
	switch {
	case serr == nil && info.IsDir():
		return resolveDirFunctions(op, path, entryFlag, nameFlag)
	case serr == nil:
		return resolveFileFunction(op, path, entryFlag, nameFlag)
	default:
		// A stem selector (`funcdctl dev front`) always names by stem — nameFlag does not apply.
		return resolveStemFunction(op, path, entryFlag)
	}
}

// resolveDirFunctions enumerates a directory (Decision 9): every `<stem>.funcdctl.yaml` is its own
// single-file function (isolated bundle); if there are none, the single generic funcdctl.yaml is the
// Phase-1/2 in-place bundle. entryFlag applies only to that single generic function (it is ambiguous
// across many).
func resolveDirFunctions(op, dir, entryFlag, nameFlag string) ([]plannedFunc, error) {
	stems, gerr := filepath.Glob(filepath.Join(dir, "*."+manifestFileName))
	if gerr != nil {
		return nil, fault.Wrapf(gerr, fault.Internal, op, "enumerate %s manifests in %q", manifestFileName, dir)
	}
	if len(stems) > 0 {
		sort.Strings(stems)
		pfs := make([]plannedFunc, 0, len(stems))
		for _, mp := range stems {
			m, lerr := loadManifestAt(op, mp)
			if lerr != nil {
				return nil, lerr
			}
			stem := strings.TrimSuffix(filepath.Base(mp), "."+manifestFileName)
			srcDir, entry, isolate := manifestEntry(m, dir, stemEntry(stem, m.Runtime), true)
			pfs = append(pfs, plannedFunc{m: m, name: sanitizeName(stem), srcDir: srcDir, entry: entry, isolate: isolate, manifestPath: mp})
		}
		return pfs, nil
	}
	generic := filepath.Join(dir, manifestFileName)
	m, lerr := loadManifestAt(op, generic)
	if lerr != nil {
		return nil, fault.NotFoundf(op, "no %s found in %q — funcdctl dev runs a function from its manifest dir", manifestFileName, dir)
	}
	dflt := entryFlag
	if dflt == "" {
		dflt = defaultEntry(m.Runtime)
	}
	srcDir, entry, isolate := manifestEntry(m, dir, dflt, false)
	return []plannedFunc{{m: m, name: genericFunctionName(nameFlag, dir), srcDir: srcDir, entry: entry, isolate: isolate, manifestPath: generic}}, nil
}

// resolveFileFunction resolves a single existing file (Decision 9): the generic funcdctl.yaml is the
// in-place bundle (name = --name or the dir basename); any other file (a handler like front.mjs or a
// stem manifest like front.funcdctl.yaml) resolves its `<stem>.funcdctl.yaml` as an isolated single-file
// function (named by stem).
func resolveFileFunction(op, path, entryFlag, nameFlag string) ([]plannedFunc, error) {
	base := filepath.Base(path)
	dir := filepath.Dir(path)
	if base == manifestFileName {
		m, lerr := loadManifestAt(op, path)
		if lerr != nil {
			return nil, lerr
		}
		dflt := entryFlag
		if dflt == "" {
			dflt = defaultEntry(m.Runtime)
		}
		srcDir, entry, isolate := manifestEntry(m, dir, dflt, false)
		return []plannedFunc{{m: m, name: genericFunctionName(nameFlag, dir), srcDir: srcDir, entry: entry, isolate: isolate, manifestPath: path}}, nil
	}
	var stem string
	if strings.HasSuffix(base, "."+manifestFileName) {
		stem = strings.TrimSuffix(base, "."+manifestFileName)
	} else {
		stem = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return resolveStemInDir(op, dir, stem, entryFlag)
}

// resolveStemFunction treats a non-existent path arg as a stem selector (`funcdctl dev front`, Decision 9):
// it resolves `<dir>/<stem>.funcdctl.yaml` (dir defaults to the cwd) as a single-file function.
func resolveStemFunction(op, path, entryFlag string) ([]plannedFunc, error) {
	return resolveStemInDir(op, filepath.Dir(path), filepath.Base(path), entryFlag)
}

// resolveStemInDir resolves one `<stem>.funcdctl.yaml` in dir into an isolated single-file function.
func resolveStemInDir(op, dir, stem, entryFlag string) ([]plannedFunc, error) {
	mp := filepath.Join(dir, stem+"."+manifestFileName)
	m, lerr := loadManifestAt(op, mp)
	if lerr != nil {
		return nil, fault.NotFoundf(op, "no %s found in %q — funcdctl dev <stem> runs the function whose stem matches", stem+"."+manifestFileName, dir)
	}
	dflt := entryFlag
	if dflt == "" {
		dflt = stemEntry(stem, m.Runtime)
	}
	srcDir, entry, isolate := manifestEntry(m, dir, dflt, true)
	return []plannedFunc{{m: m, name: sanitizeName(stem), srcDir: srcDir, entry: entry, isolate: isolate, manifestPath: mp}}, nil
}

// loadManifestAt loads a funcdctl.yaml only if it exists (a stat gate so a missing file yields the
// resolver's own NotFound message, not sdk.LoadManifest's read error).
func loadManifestAt(op, path string) (*sdk.Manifest, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fault.NotFoundf(op, "manifest %q not found", path)
	}
	return sdk.LoadManifest(path)
}

// synthesizeResources translates the whole function set's bindings + `dev` block into the backing
// desired-state objects (ADR-0125 Decision 4), auto-provisioned and de-duplicated across functions: the
// KVStore/Bucket each data binding names (owner = the function that binds it, so a write resolves —
// existence is what makes the binding resolve, ADR-0121), the ConfigMap for each `bindings.config` name
// (inline `dev.config` values), and the Secret for each `bindings.secrets` name (values from ${ENV}, a
// missing var fails fast). Resolving secrets here — before any bundle/boot side effect — is what makes a
// missing var fail fast.
func synthesizeResources(op string, pfs []plannedFunc) ([]v1.Object, error) {
	kvStores := map[v1.ObjectName]map[string]v1.ObjectName{} // store → table → owner
	buckets := map[v1.ObjectName]map[string]v1.ObjectName{}  // bucket → prefix → owner
	for _, pf := range pfs {
		for _, b := range pf.m.Bindings.KV {
			if kvStores[b.Store] == nil {
				kvStores[b.Store] = map[string]v1.ObjectName{}
			}
			kvStores[b.Store][b.Table] = pf.name
		}
		for _, b := range pf.m.Bindings.Blob {
			if buckets[b.Bucket] == nil {
				buckets[b.Bucket] = map[string]v1.ObjectName{}
			}
			buckets[b.Bucket][b.Prefix] = pf.name
		}
	}

	// dev.catalog provider declarations (ADR-0091 consumer binding in dev), de-duplicated by catalog name
	// (first function that declares one wins). A catalog OWNS its DuckLake prefix — the catalog reconciler's
	// resolveBucketRefs requires that prefix's owner == the CatalogService name — and its other blob prefixes
	// must exist (a read layer is created owner-less unless a function writes it).
	devCatalogs := map[v1.ObjectName]sdk.DevCatalog{}
	for _, pf := range pfs {
		for name, dc := range pf.m.Dev.Catalog {
			if _, seen := devCatalogs[v1.ObjectName(name)]; !seen {
				devCatalogs[v1.ObjectName(name)] = dc
			}
		}
	}
	for name, dc := range devCatalogs {
		for _, b := range dc.Blob {
			if buckets[b.Bucket] == nil {
				buckets[b.Bucket] = map[string]v1.ObjectName{}
			}
			if _, ok := buckets[b.Bucket][b.Prefix]; !ok {
				buckets[b.Bucket][b.Prefix] = "" // exists; a read layer with no in-platform writer
			}
		}
		if buckets[dc.Catalog.Bucket] == nil {
			buckets[dc.Catalog.Bucket] = map[string]v1.ObjectName{}
		}
		buckets[dc.Catalog.Bucket][dc.Catalog.Prefix] = name // the catalog owns its DuckLake prefix
	}

	var objs []v1.Object
	for _, store := range sortedResourceNames(kvStores) {
		obj, _ := v1.NewObject(v1.KindKVStore)
		ks := obj.(*v1.KVStore)
		setMeta(&ks.ObjectMeta, store)
		for _, table := range sortedSubKeys(kvStores[store]) {
			ks.Spec.Tables = append(ks.Spec.Tables, v1.KVTable{Name: table, Owner: kvStores[store][table]})
		}
		objs = append(objs, ks)
	}
	for _, bucket := range sortedResourceNames(buckets) {
		obj, _ := v1.NewObject(v1.KindBucket)
		bk := obj.(*v1.Bucket)
		setMeta(&bk.ObjectMeta, bucket)
		for _, prefix := range sortedSubKeys(buckets[bucket]) {
			bk.Spec.Prefixes = append(bk.Spec.Prefixes, v1.BucketPrefix{Name: prefix, Owner: buckets[bucket][prefix]})
		}
		objs = append(objs, bk)
	}

	// The CatalogService per dev.catalog + its QUACK_TOKEN Secret. The token is the dev engine's fixed
	// DevQuackToken, so the consumer's resolveCatalogEnv (FUNCD_CATALOG_<ALIAS>_TOKEN) presents exactly what
	// the engine serves with. The engine's endpoint + S3 keypair are wired by the catalog reconciler.
	catalogNames := make([]v1.ObjectName, 0, len(devCatalogs))
	for name := range devCatalogs {
		catalogNames = append(catalogNames, name)
	}
	sort.Slice(catalogNames, func(i, j int) bool { return catalogNames[i] < catalogNames[j] })
	for _, name := range catalogNames {
		dc := devCatalogs[name]
		tokenSecret := v1.ObjectName(string(name) + "-quack-token")
		// The token Secret is appended BEFORE the CatalogService so it is in the store when the catalog
		// reconciler resolves engineEnv (a missing Secret would hold the engine until a 2 s requeue).
		sobj, _ := v1.NewObject(v1.KindSecret)
		s := sobj.(*v1.Secret)
		setMeta(&s.ObjectMeta, tokenSecret)
		s.Spec.Data = map[string][]byte{"QUACK_TOKEN": []byte(devengine.DevQuackToken)}
		objs = append(objs, s)

		cobj, _ := v1.NewObject(v1.KindCatalogService)
		cs := cobj.(*v1.CatalogService)
		setMeta(&cs.ObjectMeta, name)
		cs.Spec.Blob = dc.Blob
		cs.Spec.Catalog = dc.Catalog
		cs.Spec.Secrets = []v1.ObjectName{tokenSecret}
		objs = append(objs, cs)
	}

	// ConfigMaps + Secrets, de-duplicated by name across functions (the first function that names one
	// provides its inline data / env values).
	configSeen := map[v1.ObjectName]bool{}
	secretSeen := map[v1.ObjectName]bool{}
	for _, pf := range pfs {
		for _, name := range pf.m.Bindings.Config {
			if configSeen[name] {
				continue
			}
			configSeen[name] = true
			obj, _ := v1.NewObject(v1.KindConfigMap)
			cm := obj.(*v1.ConfigMap)
			setMeta(&cm.ObjectMeta, name)
			cm.Spec.Data = pf.m.Dev.Config[string(name)]
			objs = append(objs, cm)
		}
		for _, name := range pf.m.Bindings.Secrets {
			if secretSeen[name] {
				continue
			}
			secretSeen[name] = true
			obj, _ := v1.NewObject(v1.KindSecret)
			s := obj.(*v1.Secret)
			setMeta(&s.ObjectMeta, name)
			data, derr := resolveSecretData(op, string(name), pf.m.Dev.Secrets[string(name)])
			if derr != nil {
				return nil, derr
			}
			s.Spec.Data = data
			objs = append(objs, s)
		}
	}

	// Dev blob-write grant (ADR-0128 as amended 2026-07-14): instead of dropping the S3 single-writer forbid,
	// grant each dev function WRITE on every prefix in the dev namespace via ADR-0136, so it is a real member
	// of each prefix's `writers` set. This lets the developer seed a no-owner `landing` (as the first
	// function's derived keypair) and lets a producer write a binding-inferred prefix — both through the REAL
	// prod forbid. An unassigned principal is still denied. A WRITE-ONLY custom Role (not the built-in
	// `Blob Data Writer`, which also grants read) is used so reads stay strictly binding-gated in dev — the
	// "forgot to bind → read Forbidden" fidelity ADR-0125 deliberately keeps.
	if len(pfs) > 0 {
		robj, _ := v1.NewObject(v1.KindRole)
		role := robj.(*v1.Role)
		setMeta(&role.ObjectMeta, devBlobWriterRole)
		role.Spec.Actions = []string{"s3::write"} // data-plane action token (ADR-0136); write only
		objs = append(objs, role)
	}
	for _, pf := range pfs {
		obj, _ := v1.NewObject(v1.KindRolesAssignment)
		ra := obj.(*v1.RolesAssignment)
		setMeta(&ra.ObjectMeta, v1.ObjectName("dev-blob-writer-"+string(pf.name)))
		ra.Spec = v1.RolesAssignmentSpec{
			Principal: &v1.PrincipalRef{Kind: v1.PrincipalKindFunction, Name: pf.name},
			Assignments: []v1.AssignmentEntry{
				{
					RoleRef: v1.RoleRef{Kind: v1.RoleRefKindRole, Name: string(devBlobWriterRole)},
					Scope:   &v1.ScopeRef{Kind: v1.ScopeKindNamespace},
				},
			},
		}
		objs = append(objs, ra)
	}
	return objs, nil
}

// devBlobWriterRole is the name of the write-only Role `funcdctl dev` provisions to make each dev function
// a member of every prefix's `writers` set (ADR-0128 as amended → ADR-0136), without widening reads.
const devBlobWriterRole v1.ObjectName = "dev-blob-writer"

// synthesizeFunction builds the Function object for one planned function (ADR-0125): run from source
// (file:// imagePath), MinReplicas=1 so it serves immediately, with its data/config/secret bindings AND
// its `spec.links` (Phase 3 — restored so `context.invoke("<alias>", …)` resolves in-process via the UDS
// local API; an unknown alias is Forbidden by the ADR-0064/0075 default-deny, preserved in dev).
func synthesizeFunction(pf plannedFunc, imagePath string) *v1.Function {
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	setMeta(&fn.ObjectMeta, pf.name)
	fn.Spec.Runtime = pf.m.Runtime
	fn.Spec.Handler = pf.m.Handler
	fn.Spec.Image = "file://" + imagePath
	fn.Spec.Replicas = 1
	fn.Spec.Scaling = v1.Scaling{MinReplicas: 1}
	fn.Spec.KV = pf.m.Bindings.KV
	fn.Spec.Blob = pf.m.Bindings.Blob
	fn.Spec.Catalogs = pf.m.Bindings.Catalogs // so resolveCatalogEnv injects FUNCD_CATALOG_<ALIAS>_URL/_TOKEN (ADR-0091)
	fn.Spec.Config = pf.m.Bindings.Config
	fn.Spec.Secrets = pf.m.Bindings.Secrets
	fn.Spec.Links = pf.m.Bindings.Links
	return fn
}

// prepareBundle delivers the ADR-0123 contract for one function and returns the file:// image path the
// Function points at. In place (!isolate) the bundle root is srcDir and the contract is the dotfile there
// (removed on stop unless the user already committed one — never clobbered). Isolated, the single handler
// file is copied into a private temp bundle with its own contract (so several single-file functions in one
// dir never collide on the shared dotfile) and a best-effort node_modules symlink lets its imports resolve.
func prepareBundle(op string, pf plannedFunc, contractBlob []byte) (imagePath string, cleanup func(), err error) {
	src := filepath.Join(pf.srcDir, pf.entry)
	if _, serr := os.Stat(src); serr != nil {
		return "", nil, fault.NotFoundf(op, "handler entry %q not found in %q (set --entry): %v", pf.entry, pf.srcDir, serr)
	}
	if !pf.isolate {
		contractPath := filepath.Join(pf.srcDir, devContractFile)
		_, existed := os.Stat(contractPath)
		if derr := deliverBundle(op, pf, pf.srcDir, contractBlob); derr != nil {
			return "", nil, derr
		}
		cleanup = func() {}
		if existed != nil {
			cleanup = func() { _ = os.Remove(contractPath) }
		}
		return src, cleanup, nil
	}

	tmp, terr := os.MkdirTemp("", "funcdctl-dev-fn-")
	if terr != nil {
		return "", nil, fault.Wrapf(terr, fault.Internal, op, "create bundle temp dir")
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	if nm := filepath.Join(pf.srcDir, "node_modules"); dirExists(nm) {
		_ = os.Symlink(nm, filepath.Join(tmp, "node_modules")) // best-effort: let a single-file function's imports resolve
	}
	if derr := deliverBundle(op, pf, tmp, contractBlob); derr != nil {
		cleanup()
		return "", nil, derr
	}
	return filepath.Join(tmp, pf.entry), cleanup, nil
}

// deliverBundle writes one function's ADR-0123 contract into its bundle dir and, for an isolated bundle, copies
// the handler entry beside it — at boot, and again on each hot-reload.
func deliverBundle(op string, pf plannedFunc, bundleDir string, contractBlob []byte) error {
	if pf.isolate {
		data, rerr := os.ReadFile(filepath.Join(pf.srcDir, pf.entry)) //nolint:gosec // the resolved handler entry in the manifest dir
		if rerr != nil {
			return fault.Wrapf(rerr, fault.NotFound, op, "read handler entry %q", pf.entry)
		}
		if werr := os.WriteFile(filepath.Join(bundleDir, pf.entry), data, 0o600); werr != nil {
			return fault.Wrapf(werr, fault.Internal, op, "copy handler into bundle")
		}
	}
	contractPath := filepath.Join(bundleDir, devContractFile)
	if werr := os.WriteFile(contractPath, contractBlob, 0o600); werr != nil {
		return fault.Wrapf(werr, fault.Internal, op, "deliver contract to %q", contractPath)
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
		for _, l := range fn.Spec.Links {
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

// sortedResourceNames returns a resource map's ObjectName keys sorted, so synthesized resource ordering
// is deterministic (the per-binding sub-domain map value is irrelevant to the ordering).
func sortedResourceNames(m map[v1.ObjectName]map[string]v1.ObjectName) []v1.ObjectName {
	keys := make([]v1.ObjectName, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// sortedSubKeys returns a sub-domain map's (table / prefix) string keys sorted, deterministically.
func sortedSubKeys(m map[string]v1.ObjectName) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stemEntry is the conventional handler entry file for a single-file function resolved by stem
// (front.funcdctl.yaml → front.mjs / front.py), mirroring the per-function-file convention (ADR-0124).
func stemEntry(stem string, rt v1.RuntimeName) string {
	if strings.HasPrefix(string(rt), "python") {
		return stem + ".py"
	}
	return stem + ".mjs"
}

// manifestEntry resolves a manifest's bundle root + entry file. With the wrangler-style `main` set (a
// handler file relative to the manifest dir), the handler's OWN dir is the bundle root — its siblings
// (SQL, vendored deps under a bundle/) resolve and FUNCD_BUNDLE_DIR points there — so it runs IN PLACE.
// Without `main`, it's the flat stem/generic convention: defaultEntry co-located with the manifest,
// isolated per defaultIsolate.
func manifestEntry(m *sdk.Manifest, dir, defaultEntry string, defaultIsolate bool) (srcDir, entry string, isolate bool) {
	if strings.TrimSpace(m.Main) != "" {
		full := filepath.Join(dir, m.Main)
		return filepath.Dir(full), filepath.Base(full), false
	}
	return dir, defaultEntry, defaultIsolate
}

// setMeta stamps the shared namespace/resource-group and the dev ownership tag onto a synthesized resource.
func setMeta(meta *v1.ObjectMeta, name v1.ObjectName) {
	meta.Name = name
	meta.Namespace = devNamespace
	meta.ResourceGroup = devResourceGroup
	meta.Tags = v1.Tags{devManagedTag: devManagedBy}
}

// resolveSecretData resolves a dev.secrets entry's ${ENV_VAR} values from the process environment. The
// value is never taken literally from the manifest (ADR-0125 Decision 4); a referenced-but-unset var
// fails fast so a missing credential is caught at boot, not at first use.
func resolveSecretData(op, secretName string, entry map[string]string) (map[string][]byte, error) {
	if len(entry) == 0 {
		return nil, nil
	}
	data := make(map[string][]byte, len(entry))
	for key, raw := range entry {
		var missing []string
		resolved := envRef.ReplaceAllStringFunc(raw, func(ref string) string {
			name := envRef.FindStringSubmatch(ref)[1]
			v, ok := os.LookupEnv(name)
			if !ok {
				missing = append(missing, name)
				return ""
			}
			return v
		})
		if len(missing) > 0 {
			return nil, fault.Invalidf(op, "secret %q key %q references unset environment variable(s) %s", secretName, key, strings.Join(missing, ", "))
		}
		data[key] = []byte(resolved)
	}
	return data, nil
}

// resolveInterpreter resolves a manifest dev.python/dev.node value: empty ⇒ "", absolute ⇒ as-is, else
// joined against the manifest dir (so `.venv/bin/python` points at the project's virtualenv).
func resolveInterpreter(p, baseDir string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}

// devInterpreter resolves a runtime interpreter by precedence: the envKey override > the manifest's
// dev.node/dev.python (a project pins its toolchain in the committable funcdctl.yaml) > pathName on PATH.
// "" ⇒ none found.
func devInterpreter(envKey, manifestValue, baseDir, pathName string) string {
	if v := envOr(envKey, ""); v != "" {
		return v
	}
	if v := resolveInterpreter(manifestValue, baseDir); v != "" {
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
func devShimOptions(ctx context.Context, op string, dev sdk.Dev, baseDir string, needPython, needNode bool) (_ []funcd.Option, cleanup func(), err error) {
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

	node := devInterpreter("FUNCD_NODE", dev.Node, baseDir, "node")
	if node == "" && needNode {
		return nil, nil, fault.NotFoundf(op, "no node interpreter found for the node handler (need node on PATH; set FUNCD_NODE or dev.node)")
	}
	if node != "" {
		shimPath := filepath.Join(dir, "shim.mjs")
		if werr := os.WriteFile(shimPath, shimnode.Shim, 0o600); werr != nil {
			return nil, nil, fault.Wrapf(werr, fault.Internal, op, "extract node shim")
		}
		opts = append(opts, funcd.WithRuntimeShim(node, shimPath))
		haveDefault = true
	}

	python := devInterpreter("FUNCD_PYTHON", dev.Python, baseDir, "python3")
	if python == "" && needPython {
		return nil, nil, fault.NotFoundf(op, "no python interpreter found for the python handler (need python3 on PATH; set FUNCD_PYTHON or dev.python)")
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
			return nil, nil, fault.Invalidf(op, "python %s cannot load the runtime shim (needs Python ≥3.12 with fastjsonschema; set FUNCD_PYTHON or dev.python): %s", python, reason)
		}
	}

	if !haveDefault {
		return nil, nil, fault.NotFoundf(op, "no runtime found on PATH (need node or python3; set FUNCD_NODE / FUNCD_PYTHON) — funcdctl dev runs the handler from source")
	}
	return opts, cleanup, nil
}

// defaultEntry is the conventional handler entry file for a runtime family (matching `funcdctl push
// --entry`): handler.py for the python family, handler.mjs otherwise. Overridable with --entry.
func defaultEntry(rt v1.RuntimeName) string {
	if strings.HasPrefix(string(rt), "python") {
		return "handler.py"
	}
	return "handler.mjs"
}

// dnsInvalid matches characters not allowed in a DNS-1123 label (the resource-name constraint).
//
//nolint:gochecknoglobals // a compiled, immutable regexp
var dnsInvalid = regexp.MustCompile(`[^a-z0-9-]+`)

// genericFunctionName names the single generic-funcdctl.yaml function: the --name flag if given, else
// the directory basename (abs-resolved, DNS-1123). A `<stem>.funcdctl.yaml` never reaches here — it
// always names by its file stem.
func genericFunctionName(nameFlag, dir string) v1.ObjectName {
	if strings.TrimSpace(nameFlag) != "" {
		return sanitizeName(nameFlag)
	}
	// Abs-resolve first so a "." arg (running `funcdctl dev` inside the dir) still yields the real dir
	// name, not the degenerate basename of ".".
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return sanitizeName(filepath.Base(dir))
}

// sanitizeName maps an arbitrary string (a dir basename or a manifest stem) to a DNS-1123 function name:
// lowercased, invalid runs collapsed to '-', edges trimmed, empty ⇒ "dev".
func sanitizeName(s string) v1.ObjectName {
	base := strings.ToLower(s)
	base = dnsInvalid.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "dev"
	}
	return v1.ObjectName(base)
}
