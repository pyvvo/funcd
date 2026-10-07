//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/contract"
	"github.com/pyvvo/funcd/internal/funclog/compact"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/internal/testkit/langmod"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// shimRig is an embedded platform on the process driver that runs functions on the embedded language shims,
// solo and pooled, pulled from an OCI layout with the contract `funcdctl push` stores. It holds the blob
// bucket the captured function logs land in, the runtime, and counts the host's unreadable-record warnings.
type shimRig struct {
	c          *sdk.Client
	dp         string
	bucket     blob.Bucket
	rt         runtime.Runtime
	layout     string
	unreadable *atomic.Int64
}

// newShimRig boots the rig with the Node shims and, when python is not "", the Python shims for the python*
// family. Node-gated.
func newShimRig(t *testing.T, python string, extra ...funcd.Option) *shimRig {
	t.Helper()
	h, opts := shimRigBase(t, python)
	p, err := funcd.New(append(opts, extra...)...)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("platform Run did not return after cancel")
		}
	})
	h.connect(t, p)
	return h
}

// shimRigBase builds a rig's fresh substrate and the options that assemble its platform.
func shimRigBase(t *testing.T, python string) (*shimRig, []funcd.Option) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the embedded-shim lane")
	}
	ctx := context.Background()
	bucket, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	unreadable := &atomic.Int64{}
	logger := slog.New(warnCounter{
		unreadable: unreadable,
		next:       slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}),
	})
	rt := process.New()
	opts := []funcd.Option{
		funcd.WithBlob(bucket),
		funcd.WithBus(messaging),
		funcd.WithStore(store.New(memory.New())),
		funcd.WithRuntime(rt),
		funcd.WithGateway(embedded.New()),
		funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, "default"),
		funcd.WithRuntimeShim(node, langmod.NodeShim(t)),
		funcd.WithPoolShim(node, langmod.PoolShim(t)),
		funcd.WithPoolLimit(4),
		funcd.WithArtifactStore(shortDataDir(t)),
		funcd.WithFunclog(300*time.Millisecond, 0),
		funcd.WithLogger(logger),
	}
	if python != "" {
		shimEntry, poolEntry := langmod.PythonShim(t)
		opts = append(opts,
			funcd.WithRuntimeShimFor("python", python, shimEntry),
			funcd.WithPoolShimFor("python", python, poolEntry))
	}
	return &shimRig{bucket: bucket, rt: rt, layout: t.TempDir(), unreadable: unreadable}, opts
}

// connect points the rig's clients at running platform p.
func (h *shimRig) connect(t *testing.T, p *funcd.Platform) {
	t.Helper()
	c, err := sdk.New("http://"+p.Addr(), sdk.WithToken(funcd.DevToken))
	require.NoError(t, err)
	h.c, h.dp = c, "http://"+p.DataPlaneAddr()
}

// shortDataDir is a platform dir outside t.TempDir(), whose macOS path overruns the Unix socket path limit
// (issue #41).
func shortDataDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "funcd")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// requirePython returns an interpreter that can load the Python shim (3.12 or later, 3.14 or later for the
// pool host, with fastjsonschema): FUNCD_PYTHON, else python3.14 or python3 on PATH. Without one it skips, but
// fails in CI (CI set), whose dev shell pins one: a skip there would hide the lane (issue #544).
func requirePython(t *testing.T, pool bool) string {
	t.Helper()
	minor := 12
	if pool {
		minor = 14
	}
	check := fmt.Sprintf("import sys, fastjsonschema; raise SystemExit(0 if sys.version_info >= (3, %d) else 1)", minor)
	for _, name := range []string{os.Getenv("FUNCD_PYTHON"), "python3.14", "python3"} {
		if name == "" {
			continue
		}
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-c", check).Run() == nil {
			return path
		}
	}
	missing := fmt.Sprintf("no Python 3.%d or later with fastjsonschema (set FUNCD_PYTHON)", minor)
	if os.Getenv("CI") != "" {
		t.Fatal(missing + "; CI must run the Python shim lane")
	}
	t.Skip(missing + "; skipping the Python shim lane")
	return ""
}

type warnCounter struct {
	unreadable *atomic.Int64
	next       slog.Handler
}

func (w warnCounter) Enabled(ctx context.Context, l slog.Level) bool { return w.next.Enabled(ctx, l) }

func (w warnCounter) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "funclog: skipping unreadable record" {
		w.unreadable.Add(1)
	}
	return w.next.Handle(ctx, r)
}

func (w warnCounter) WithAttrs(as []slog.Attr) slog.Handler {
	return warnCounter{unreadable: w.unreadable, next: w.next.WithAttrs(as)}
}

func (w warnCounter) WithGroup(name string) slog.Handler {
	return warnCounter{unreadable: w.unreadable, next: w.next.WithGroup(name)}
}

// shimFn is one function to deploy: its source, its runtime, its {input, output} contract ("" is the Json
// form {}), its pool worker ("" runs it solo), its spec.timeout and its links.
type shimFn struct {
	runtime, ext, src string
	input, output     string
	worker            string
	timeout           time.Duration
	links             []v1.FunctionLink
	change            func(*v1.Function)
}

func nodeFn(src string) shimFn   { return shimFn{runtime: "nodejs22", ext: ".mjs", src: src} }
func pythonFn(src string) shimFn { return shimFn{runtime: "python314", ext: ".py", src: src} }

func (f shimFn) pooled(worker string) shimFn { f.worker = worker; return f }

func (f shimFn) withTimeout(d time.Duration) shimFn { f.timeout = d; return f }

// with sets change to edit the Function before it is applied.
func (f shimFn) with(change func(*v1.Function)) shimFn { f.change = change; return f }

func (f shimFn) withContract(input, output string) shimFn {
	f.input, f.output = input, output
	return f
}

// deploy gates the contract and pushes the source as `funcdctl push` does, then applies the Function.
func (h *shimRig) deploy(t *testing.T, name string, f shimFn) {
	t.Helper()
	side := func(s string) []byte {
		if s == "" {
			s = `{}`
		}
		require.NoError(t, contract.Check([]byte(s)), "%s contract is in the funcd profile", name)
		return []byte(s)
	}
	contractBlob, err := artifact.ContractBlob(side(f.input), side(f.output))
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), name+f.ext)
	require.NoError(t, os.WriteFile(file, []byte(f.src), 0o600))
	ref := "oci-layout://" + h.layout + ":" + name
	digest, err := artifact.Push(context.Background(), ref, file, contractBlob, "", "")
	require.NoError(t, err)
	obj, _ := v1.NewObject(v1.KindFunction)
	fn := obj.(*v1.Function)
	fn.Name, fn.Namespace, fn.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	fn.Spec.Runtime, fn.Spec.Handler = v1.RuntimeName(f.runtime), "handle"
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
	fn.Spec.Replicas, fn.Spec.Scaling.MinReplicas = 1, 1
	fn.Spec.Pooling.Worker = f.worker
	fn.Spec.Timeout = v1.Duration(f.timeout)
	fn.Spec.Links = f.links
	if f.change != nil {
		f.change(fn)
	}
	_, err = h.c.Apply(context.Background(), fn)
	require.NoError(t, err)
}

type reply struct {
	status int
	ctype  string
	body   string
}

// post invokes a function over the data plane. A transport error is status -1, so it is safe off the test
// goroutine.
func (h *shimRig) post(name, body string) reply { return h.postWithin(name, body, 30*time.Second) }

// postWithin is post with a client that waits up to d.
func (h *shimRig) postWithin(name, body string, d time.Duration) reply {
	cl := &http.Client{Timeout: d}
	resp, err := cl.Post(h.dp+"/function/"+name, "application/json", strings.NewReader(body))
	if err != nil {
		return reply{status: -1, body: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return reply{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type"), body: string(b)}
}

// postConcurrently sends n copies of body at once; the returned func waits for their replies.
func (h *shimRig) postConcurrently(name, body string, n int) func() []reply {
	replies := make([]reply, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replies[i] = h.post(name, body)
		}()
	}
	return func() []reply {
		wg.Wait()
		return replies
	}
}

// inFlight returns a file a slow handler appends one byte to when it starts, and a wait until n handlers have
// started, so the call that faults the worker goes out only once the slow calls are in flight on it.
func inFlight(t *testing.T) (string, func(n int)) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "started")
	return marker, func(n int) {
		t.Helper()
		require.Eventually(t, func() bool {
			fi, err := os.Stat(marker)
			return err == nil && fi.Size() == int64(n)
		}, 15*time.Second, 20*time.Millisecond, "%d slow calls are in flight", n)
	}
}

// replyFields are the reply-body fields the tests read.
type replyFields struct {
	PID    int    `json:"pid"`
	Caught string `json:"caught"`
	Error  string `json:"error"`
}

func decodeReply(t *testing.T, r reply) replyFields {
	t.Helper()
	var out replyFields
	require.NoErrorf(t, json.Unmarshal([]byte(r.body), &out), "reply %d is a JSON object: %q", r.status, r.body)
	return out
}

// countLogBodies counts the persisted log records whose body starts with prefix. It takes no *testing.T: it
// runs in an Eventually tick that can outlive the test.
func (h *shimRig) countLogBodies(prefix string) (int, error) {
	ctx := context.Background()
	objs, err := h.bucket.List(ctx, "logs/")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range objs {
		data, err := h.bucket.Get(ctx, o.Key)
		if err != nil {
			return 0, err
		}
		rows, err := compact.DecodeJSONL(data)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			if strings.HasPrefix(r.Body, prefix) {
				n++
			}
		}
	}
	return n, nil
}

// logLine waits for the first stored log line of fn that match accepts, as `funcdctl logs` reads it.
func (h *shimRig) logLine(t *testing.T, fn string, match func(logread.Line) bool) map[string]string {
	t.Helper()
	var attrs map[string]string
	require.Eventually(t, func() bool {
		lines, err := h.c.Logs(context.Background(), "default", v1.ObjectName(fn), sdk.LogsOptions{})
		if err != nil {
			return false
		}
		for _, l := range lines {
			if match(l) {
				attrs = map[string]string{}
				return len(l.Attrs) == 0 || json.Unmarshal(l.Attrs, &attrs) == nil
			}
		}
		return false
	}, 15*time.Second, 200*time.Millisecond, "%s logs the record", fn)
	return attrs
}

// requireAllLogsPersisted invokes every function rounds times, all at once each round, and requires each
// function's want records in the store with no unreadable record on the host's log channel.
func (h *shimRig) requireAllLogsPersisted(t *testing.T, fns []string, rounds, want int) {
	t.Helper()
	for range rounds {
		waits := make([]func() []reply, 0, len(fns))
		for _, fn := range fns {
			waits = append(waits, h.postConcurrently(fn, `{}`, 1))
		}
		for i, wait := range waits {
			r := wait()[0]
			require.Equalf(t, http.StatusOK, r.status, "%s: %s", fns[i], r.body)
		}
	}
	for _, fn := range fns {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			n, err := h.countLogBodies(fn + "-")
			assert.NoError(c, err)
			assert.Equal(c, want, n, "every record %s logged is persisted", fn)
		}, 15*time.Second, 300*time.Millisecond)
	}
	require.Zero(t, h.unreadable.Load(), "no log record reached the host spliced into another")
}

// Issue #81 (TypeScript half): pooled Node workers share the fd 3 log pipe, and records longer than PIPE_BUF
// written concurrently spliced into each other, so the host dropped them as unreadable.
func TestIssue81_PooledNodeConcurrentLongLogsAllPersist(t *testing.T) {
	h := newShimRig(t, "")
	fns := []string{"nlog-a", "nlog-b"}
	for _, fn := range fns {
		h.deploy(t, fn, nodeFn(fmt.Sprintf(`const pad = 'x'.repeat(4096);
export function handle() {
  for (let i = 0; i < 1000; i++) console.log('%s-' + i, pad);
  return { ok: true };
}
`, fn)).pooled("w81"))
	}
	waitReady(t, h.c, fns...)
	h.requireAllLogsPersisted(t, fns, 2, 2000)
}

// Issue #81 (Python half): pooled Python subinterpreters share the fd 3 log pipe the same way.
func TestIssue81_PooledPythonConcurrentLongLogsAllPersist(t *testing.T) {
	h := newShimRig(t, requirePython(t, true))
	fns := []string{"plog-a", "plog-b"}
	for _, fn := range fns {
		h.deploy(t, fn, pythonFn(fmt.Sprintf(`import logging

PAD = "x" * 4096


def handle(ctx, event):
    for i in range(1000):
        logging.info("%s-%%d %%s", i, PAD)
    return {"ok": True}
`, fn)).pooled("w81"))
	}
	waitReady(t, h.c, fns...)
	h.requireAllLogsPersisted(t, fns, 2, 2000)
}

// Issue #82 (TypeScript half): console.error(new Error(...)) stored attrs.args as "[{}]", losing the
// error's message and stack.
func TestIssue82_NodeConsoleErrorKeepsMessageAndStack(t *testing.T) {
	h := newShimRig(t, "")
	h.deploy(t, "nerr", nodeFn(`export function handle() {
  console.error(new Error('issue82-detail'));
  return { ok: true };
}
`))
	waitReady(t, h.c, "nerr")
	require.Equal(t, http.StatusOK, h.post("nerr", `{}`).status)
	attrs := h.logLine(t, "nerr", func(l logread.Line) bool { return l.Severity == "ERROR" })
	args := attrs["args"]
	require.Contains(t, args, `"message":"issue82-detail"`, "attrs.args keeps the error's message: %v", attrs)
	require.Contains(t, args, `"stack":"Error: issue82-detail\n    at `, "attrs.args keeps the error's stack: %v", attrs)
}

// Issue #82 (Python half): a logging call stored neither record.args nor the traceback of
// logging.exception.
func TestIssue82_PythonLogKeepsArgsAndTraceback(t *testing.T) {
	h := newShimRig(t, requirePython(t, false))
	h.deploy(t, "perr", pythonFn(`import logging


def handle(ctx, event):
    logging.info("issue82 %s", "arg-value")
    try:
        raise ValueError("issue82-boom")
    except ValueError:
        logging.exception("issue82-caught")
    return {"ok": True}
`))
	waitReady(t, h.c, "perr")
	require.Equal(t, http.StatusOK, h.post("perr", `{}`).status)
	info := h.logLine(t, "perr", func(l logread.Line) bool { return l.Body == "issue82 arg-value" })
	require.Equal(t, `["arg-value"]`, info["args"], "attrs.args keeps the logging call's args: %v", info)
	caught := h.logLine(t, "perr", func(l logread.Line) bool { return l.Body == "issue82-caught" })
	trace := caught["exception.stacktrace"]
	require.Contains(t, trace, "Traceback (most recent call last)", "the record keeps the traceback: %v", caught)
	require.Contains(t, trace, "ValueError: issue82-boom", "the record keeps the exception: %v", caught)
}

// Issue #129: the Python worker failed to compile the in-profile format uuid, so the Function went Failed
// (ShapeInvalid) instead of validating it. It reproduces with the runtime image's fastjsonschema 2.21.2;
// fastjsonschema 2.22 ships the uuid format itself.
func TestIssue129_PythonUUIDContractServesAndValidates(t *testing.T) {
	h := newShimRig(t, requirePython(t, false))
	h.deploy(t, "puuid", pythonFn(`def handle(ctx, event):
    return {"id": event["data"]["id"]}
`).withContract(`{"type":"object","properties":{"id":{"type":"string","format":"uuid"}},"required":["id"],"additionalProperties":false}`, ""))
	waitReady(t, h.c, "puuid")
	ok := h.post("puuid", `{"id":"123e4567-e89b-12d3-a456-426614174000"}`)
	require.Equal(t, http.StatusOK, ok.status, "a valid uuid runs the handler: %s", ok.body)
	bad := h.post("puuid", `{"id":"123e4567-e89b-12d3-a456"}`)
	require.Equal(t, http.StatusUnprocessableEntity, bad.status, "a malformed uuid violates the contract: %s", bad.body)
}

// Issue #130: a linked callee's 2xx reply that is not JSON threw outside context.invoke's promise and killed
// the caller's worker with every call in flight on it.
func TestIssue130_InvokeOfNonJSONReplyRejectsCatchably(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping the embedded-shim lane")
	}
	// A stand-in runtime whose shim answers every call 200 with a body that is not JSON.
	rawShim := filepath.Join(t.TempDir(), "rawshim.mjs")
	require.NoError(t, os.WriteFile(rawShim, []byte(`import { writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
const server = createServer((req, res) => {
  req.resume();
  req.on('end', () => {
    if (req.url.startsWith('/health/')) return res.end('ok');
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end('{"x": NaN}');
  });
});
server.listen(0, '127.0.0.1', () => writeFileSync(process.env.FUNCD_PORTFILE, String(server.address().port)));
`), 0o600))
	h := newShimRig(t, "", funcd.WithRuntimeShimFor("rawhttp", node, rawShim))
	h.deploy(t, "rawcallee", shimFn{runtime: "rawhttp1", ext: ".mjs", src: "export {};\n"})
	caller := nodeFn(`import { appendFileSync } from 'node:fs';
export async function handle(ctx, e) {
  if (e.data && e.data.slow) {
    appendFileSync(e.data.started, '.');
    await new Promise((r) => setTimeout(r, 1500));
  }
  if (e.data && e.data.invoke) {
    try {
      await ctx.invoke('callee', {});
      return { caught: '', pid: process.pid };
    } catch (err) {
      return { caught: String(err), pid: process.pid };
    }
  }
  return { pid: process.pid };
}
`)
	caller.links = []v1.FunctionLink{{Alias: "callee", Target: "rawcallee"}}
	h.deploy(t, "ncaller", caller)
	waitReady(t, h.c, "rawcallee", "ncaller")

	pid := decodeReply(t, h.post("ncaller", `{}`)).PID
	require.NotZero(t, pid)
	started, waitStarted := inFlight(t)
	slow := h.postConcurrently("ncaller", fmt.Sprintf(`{"slow":true,"started":%q}`, started), 1)
	waitStarted(1)
	got := h.post("ncaller", `{"invoke":true}`)
	require.Equal(t, http.StatusOK, got.status, "the caller's catch handles the rejection: %s", got.body)
	out := decodeReply(t, got)
	require.NotEmpty(t, out.Caught, "context.invoke rejects on a 2xx reply that is not JSON: %s", got.body)
	require.Equal(t, pid, out.PID)
	s := slow()[0]
	require.Equal(t, http.StatusOK, s.status, "a concurrent call on the caller's worker is answered: %s", s.body)
	require.Equal(t, pid, decodeReply(t, s).PID)
	require.Equal(t, pid, decodeReply(t, h.post("ncaller", `{}`)).PID, "the caller's worker was not replaced")
}

// Issue #131: the Python shim dropped the connection (an empty 502) for a result json cannot encode and for
// SystemExit, accepted NaN request bodies, and sent NaN in a 200 body, which is not JSON.
func TestIssue131_PythonRepliesAreJSON(t *testing.T) {
	py := requirePython(t, false)
	h := newShimRig(t, py)
	src := pythonFn(`import math


def handle(ctx, event):
    kind = event["data"].get("kind")
    if kind == "set":
        return {"s": {1, 2}}
    if kind == "exit":
        raise SystemExit(1)
    if kind == "nan":
        return {"x": math.nan}
    return {"ok": True}
`)
	h.deploy(t, "pyreply", src)
	names := []string{"pyreply"}
	if pythonCanPool(py) {
		h.deploy(t, "pyreply-pooled", src.pooled("w131"))
		names = append(names, "pyreply-pooled")
	}
	waitReady(t, h.c, names...)
	for _, name := range names {
		for _, kind := range []string{"set", "exit"} {
			r := h.post(name, fmt.Sprintf(`{"kind":%q}`, kind))
			require.Equalf(t, http.StatusInternalServerError, r.status, "%s %s: a handler failure is a 500: %q", name, kind, r.body)
			require.Equal(t, "application/json", r.ctype, "%s %s", name, kind)
			require.NotEmpty(t, decodeReply(t, r).Error, "%s %s: the 500 carries {error}", name, kind)
		}
		for _, body := range []string{`NaN`, `{"data": NaN}`} {
			r := h.post(name, body)
			require.Equalf(t, http.StatusBadRequest, r.status, "%s: %s is not JSON: %q", name, body, r.body)
		}
		r := h.post(name, `{"kind":"nan"}`)
		require.Equal(t, http.StatusOK, r.status, "%s nan: %q", name, r.body)
		require.JSONEq(t, `{"x":null}`, r.body, "%s: NaN is sent as null", name)
	}
}

// pythonCanPool reports whether py can run the Python pool host (3.14 or later).
func pythonCanPool(py string) bool {
	return exec.Command(py, "-c", "import sys; raise SystemExit(0 if sys.version_info >= (3, 14) else 1)").Run() == nil
}

// Issue #132: in the solo Node shim, a rejection the handler left unhandled or a throw from its callback
// after it returned exited the worker, cutting off every call in flight on it.
func TestIssue132_StrayNodeFaultKeepsSoloWorkerServing(t *testing.T) {
	h := newShimRig(t, "")
	h.deploy(t, "nstray", nodeFn(`import { appendFileSync } from 'node:fs';
export async function handle(_c, e) {
  const kind = e.data && e.data.kind;
  if (kind === 'slow') {
    appendFileSync(e.data.started, '.');
    await new Promise((r) => setTimeout(r, 1500));
  }
  if (kind === 'unhandled') Promise.reject(new Error('nobody awaits this'));
  if (kind === 'uncaught') setTimeout(() => { throw new Error('thrown after return'); }, 10);
  return { pid: process.pid };
}
`))
	waitReady(t, h.c, "nstray")
	pid := decodeReply(t, h.post("nstray", `{}`)).PID
	require.NotZero(t, pid)
	for _, kind := range []string{"unhandled", "uncaught"} {
		started, waitStarted := inFlight(t)
		slow := h.postConcurrently("nstray", fmt.Sprintf(`{"kind":"slow","started":%q}`, started), 2)
		waitStarted(2)
		trigger := h.post("nstray", fmt.Sprintf(`{"kind":%q}`, kind))
		require.Equal(t, http.StatusOK, trigger.status, "%s: %s", kind, trigger.body)
		for _, r := range slow() {
			require.Equalf(t, http.StatusOK, r.status, "%s: a concurrent call is answered: %q", kind, r.body)
			require.Equal(t, pid, decodeReply(t, r).PID, kind)
		}
		require.Equal(t, pid, decodeReply(t, h.post("nstray", `{}`)).PID, "%s: the worker was not replaced", kind)
	}
}

// Issue #133: the Node shim ignored the contract's format keyword, so a garbage value passed an in-profile
// email or uuid format.
func TestIssue133_NodeContractEnforcesFormat(t *testing.T) {
	h := newShimRig(t, "")
	f := nodeFn("export function handle(_c, e) { return { got: e.data }; }\n").
		withContract(`{"type":"object","properties":{"email":{"type":"string","format":"email"},"id":{"type":"string","format":"uuid"}},"required":["email","id"],"additionalProperties":false}`, "")
	h.deploy(t, "nformat", f)
	h.deploy(t, "nformat-pooled", f.pooled("w133"))
	waitReady(t, h.c, "nformat", "nformat-pooled")
	const id = "123e4567-e89b-12d3-a456-426614174000"
	for _, name := range []string{"nformat", "nformat-pooled"} {
		ok := h.post(name, `{"email":"a@example.com","id":"`+id+`"}`)
		require.Equal(t, http.StatusOK, ok.status, "%s: valid values run the handler: %s", name, ok.body)
		for _, body := range []string{`{"email":"not a valid value","id":"` + id + `"}`, `{"email":"a@example.com","id":"not-a-uuid"}`} {
			r := h.post(name, body)
			require.Equalf(t, http.StatusUnprocessableEntity, r.status, "%s: %s violates the format: %s", name, body, r.body)
		}
	}
}

// Issue #185: the Node shim answered 422 "must be null" to a void-input call whose envelope had no data;
// ADR-0090 accepts absent or null data.
func TestIssue185_NodeVoidInputAcceptsAbsentData(t *testing.T) {
	h := newShimRig(t, "")
	f := nodeFn("export function handle() { return { ran: true }; }\n").withContract(`{"type":"null"}`, "")
	h.deploy(t, "nvoid", f)
	h.deploy(t, "nvoid-pooled", f.pooled("w185"))
	waitReady(t, h.c, "nvoid", "nvoid-pooled")
	for _, name := range []string{"nvoid", "nvoid-pooled"} {
		for _, body := range []string{``, `{"data":null}`, `{"specversion":"1.0","id":"x","type":"t","source":"s"}`} {
			r := h.post(name, body)
			require.Equalf(t, http.StatusOK, r.status, "%s: %q has no data, which a void input accepts: %s", name, body, r.body)
		}
	}
}

// Issue #186: the Node shim checked the output contract against the handler's JS value, so a NaN in a
// required number field passed and went out as null with 200.
func TestIssue186_NodeOutputContractChecksTheSentJSON(t *testing.T) {
	h := newShimRig(t, "")
	f := nodeFn("export function handle() { return { n: 0 / 0 }; }\n").
		withContract("", `{"type":"object","properties":{"n":{"type":"number"}},"required":["n"],"additionalProperties":false}`)
	h.deploy(t, "nnan", f)
	h.deploy(t, "nnan-pooled", f.pooled("w186"))
	waitReady(t, h.c, "nnan", "nnan-pooled")
	for _, name := range []string{"nnan", "nnan-pooled"} {
		r := h.post(name, `{}`)
		require.Equalf(t, http.StatusInternalServerError, r.status, "%s: null for a required number violates the output contract: %s", name, r.body)
	}
}

// Issue #183: a pooled Python handler's os.chdir or os.umask changed the working directory and the file
// mode of every other handler in its pool.
func TestIssue183_PythonPoolKeepsProcessStateFromSiblings(t *testing.T) {
	h := newShimRig(t, requirePython(t, true))
	h.deploy(t, "pmutator", pythonFn(`import os


def handle(ctx, event):
    refused = []
    for call in (lambda: os.chdir("/"), lambda: os.umask(0o077)):
        try:
            call()
        except RuntimeError:
            refused.append(True)
    return {"refused": len(refused)}
`).pooled("w183"))
	h.deploy(t, "pobserver", pythonFn(`import os
import tempfile


def handle(ctx, event):
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "probe")
        os.close(os.open(path, os.O_CREAT | os.O_WRONLY, 0o666))
        return {"cwd": os.getcwd(), "mode": os.stat(path).st_mode & 0o777}
`).pooled("w183"))
	waitReady(t, h.c, "pmutator", "pobserver")
	before := h.post("pobserver", `{}`)
	require.Equal(t, http.StatusOK, before.status, before.body)
	m := h.post("pmutator", `{}`)
	require.Equal(t, http.StatusOK, m.status, m.body)
	require.JSONEq(t, `{"refused":2}`, m.body, "the pool refuses chdir and umask")
	require.JSONEq(t, before.body, h.post("pobserver", `{}`).body, "the sibling's cwd and file mode are unchanged")
}

// Issue #188: the Python shim called an async def handle without awaiting it, so the handler never ran.
func TestIssue188_PythonAsyncHandlerIsAwaited(t *testing.T) {
	py := requirePython(t, false)
	h := newShimRig(t, py)
	f := pythonFn(`import asyncio


async def handle(ctx, event):
    await asyncio.sleep(0)
    return {"awaited": True}
`)
	h.deploy(t, "pasync", f)
	names := []string{"pasync"}
	if pythonCanPool(py) {
		h.deploy(t, "pasync-pooled", f.pooled("w188"))
		names = append(names, "pasync-pooled")
	}
	waitReady(t, h.c, names...)
	for _, name := range names {
		r := h.post(name, `{}`)
		require.Equal(t, http.StatusOK, r.status, "%s: %q", name, r.body)
		require.JSONEq(t, `{"awaited":true}`, r.body, name)
	}
}
