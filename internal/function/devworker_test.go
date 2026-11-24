package function_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/function"
	"github.com/pyvvo/funcd/internal/runtime"
)

// ADR-0346: under funcdctl dev, a worker on an Addon's runtime is built in the container shape for dev's image side.

// devImages is the fake runtime as funcdctl dev's runtime.Pair presents it (ADR-0346 Decision 1): it runs images or
// says why not, and its container workers reach funcd's listeners at host.
type devImages struct {
	*fakeRuntime
	runs      bool
	why, host string
}

func (d devImages) RunsImages() (bool, string) { return d.runs, d.why }

func (d devImages) EndpointHost() string { return d.host }

// onDevImages runs the harness's workers on devImages, with the local API on socks.
func onDevImages(runs bool, why, host string, socks *socketLog) func(*function.Deps) {
	return func(d *function.Deps) {
		d.Runtime = devImages{fakeRuntime: d.Runtime.(*fakeRuntime), runs: runs, why: why, host: host}
		d.InvokeSockets = socks
	}
}

// ADR-0346 Decisions 2, 5 and 6: on an image runtime, a worker on an Addon's runtime runs its Revision's image in the
// container shape, with the local API on a listener of its own and funcd's endpoints by the host it reaches them by,
// only in the keys the hooks mark, and no host path, as the decider ruled on 2026-11-24 for dev's DuckDB extension
// folder; a Function on funcd's runtimes beside it keeps the process shape and sets no Image.
func TestDevAddonWorkerSpec(t *testing.T) {
	t.Parallel()
	const extDir = "/opt/duckdb-ext"
	for _, host := range []string{"funcd.host", runtime.LoopbackHost} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			socks := &socketLog{}
			h := newShimHarness(t, http.StatusOK, false, onDevImages(true, "", host, socks), withS3Gateway, func(d *function.Deps) {
				d.LakeMaster, d.LakeExtensionDir = []byte("dev-worker-spec-master-secret"), extDir
			})
			h.putAddon(t, denoImageA, v1.WorkloadFunction)
			storeBucket(t, h)
			storeReadyLake(t, h.st)
			obj, _ := v1.NewObject(v1.KindConfigMap)
			cm := obj.(*v1.ConfigMap)
			cm.Name, cm.Namespace, cm.ResourceGroup = "app", "default", "rg1"
			cm.Spec.Data = map[string]string{"UPSTREAM": "http://127.0.0.1:7777"}
			_, err := h.st.Create(context.Background(), cm)
			require.NoError(t, err)
			bind := func(fn *v1.Function) {
				bindBucket(fn)
				bindLake(fn)
				fn.Spec.Bindings.Config = []v1.ObjectName{"app"}
			}
			h.create(t, "greeter", func(fn *v1.Function) { onDeno2(fn); bind(fn) })
			h.create(t, "plain", bind)
			h.reconcile(t, "greeter")
			h.reconcile(t, "plain")
			h.requireReady(t, "greeter")
			h.requireReady(t, "plain")

			id := runtime.NewInstanceID("default", "greeter", "greeter-1", 0)
			spec := h.rt.specOf(id)
			require.Equal(t, denoImageA, spec.Image, "the worker runs the image its Revision recorded")
			require.Empty(t, spec.Command, "the image's shim is its entrypoint")
			require.Equal(t, []runtime.Mount{{Source: filepath.Dir(h.artifact), Target: "/var/funcd/artifact", ReadOnly: true}},
				spec.Mounts, "the artifact's folder, read-only, which the driver copies into the worker's volume")
			require.Equal(t, "/var/funcd/artifact/handler.mjs", spec.Env["FUNCD_ARTIFACT"])
			require.NotContains(t, spec.Env, "FUNCD_INVOKE_SOCKET", "no socket crosses into the container")
			require.Equal(t, "http://"+host+":40001", spec.Env["FUNCD_INVOKE_URL"])
			require.Equal(t, "token-of-"+string(id), spec.Env["FUNCD_INVOKE_TOKEN"])
			require.Equal(t, "http://"+host+":9000", spec.Env["AWS_ENDPOINT_URL_S3"], "a marked endpoint key")
			require.Equal(t, host+":1", spec.Env["FUNCD_LAKE_LAKE_URL"], "the Lake hook's endpoint key, marked too")
			require.NotContains(t, spec.Env, "DUCKDB_EXTENSION_DIRECTORY", "a container worker takes no host path")
			require.Equal(t, "http://127.0.0.1:7777", spec.Env["UPSTREAM"], "a config key is the developer's, never rewritten")
			if host == runtime.LoopbackHost {
				require.NotContains(t, spec.Env, "FUNCD_PORT", "on the host's network the shim binds the loopback")
			} else {
				require.Equal(t, "8080", spec.Env["FUNCD_PORT"], "on an engine in a VM the shim binds the published port")
			}
			require.Contains(t, socks.log(), "listen default/greeter "+string(id))

			plain := h.rt.specOf(runtime.NewInstanceID("default", "plain", "plain-1", 0))
			require.Empty(t, plain.Image, "a process-mode spec sets no Image, so Pair runs it on the process driver")
			require.NotEmpty(t, plain.Command)
			require.Contains(t, plain.Env, "FUNCD_INVOKE_SOCKET")
			require.NotContains(t, plain.Env, "FUNCD_INVOKE_URL")
			require.Equal(t, "http://127.0.0.1:9000", plain.Env["AWS_ENDPOINT_URL_S3"])
			require.Equal(t, "127.0.0.1:1", plain.Env["FUNCD_LAKE_LAKE_URL"])
			require.Equal(t, extDir, plain.Env["DUCKDB_EXTENSION_DIRECTORY"], "a process worker reads dev's folder")
		})
	}
}

// ADR-0346 Decision 1: when dev's image side runs no image, a Function on an Addon's runtime fails RuntimeUnavailable
// with the reason the image side gives, and no worker is created.
func TestDevAddonWorkerGateNamesWhy(t *testing.T) {
	t.Parallel()
	const why = "no container engine answered: docker: connection refused; nerdctl: not found"
	h := newShimHarness(t, http.StatusOK, false, onDevImages(false, why, "funcd.host", &socketLog{}))
	h.putAddon(t, denoImageA, v1.WorkloadFunction)
	h.create(t, "greeter", onDeno2)
	h.create(t, "plain", func(*v1.Function) {})
	h.reconcile(t, "greeter")
	h.reconcile(t, "plain")

	h.requireUnavailable(t, "greeter", `runtime "deno2" is not available on this node: it is an Addon's image, which no `+
		`worker driver of this server runs: `+why)
	h.requireReady(t, "plain")
	require.Empty(t, h.rt.specOf(runtime.NewInstanceID("default", "greeter", "greeter-1", 0)).Name, "no worker is created")
}

// ADR-0346 Decision 6: a worker's listener goes when its Create fails, when the worker of a revision that no longer
// serves is retired, and with its Function's own Remove.
func TestDevAddonWorkerListenerGoes(t *testing.T) {
	t.Parallel()
	socks := &socketLog{}
	h := newShimHarness(t, http.StatusOK, false, onDevImages(true, "", "funcd.host", socks))
	h.putAddon(t, denoImageA, v1.WorkloadFunction)
	h.rt.failImage(denoImageA, notEmbedded(denoImageA))
	h.create(t, "greeter", onDeno2)
	h.reconcile(t, "greeter")
	id := string(runtime.NewInstanceID("default", "greeter", "greeter-1", 0))
	require.Equal(t, []string{"listen default/greeter " + id, "release " + id}, socks.log(), "a failed Create releases it")

	h.rt.failImage(denoImageA, nil)
	h.reconcile(t, "greeter")
	h.requireReady(t, "greeter")
	served := len(socks.log())
	h.apply(t, "greeter", func(fn *v1.Function) { fn.Spec.Handler = "handle2" })
	require.Eventually(t, func() bool {
		h.reconcile(t, "greeter")
		fn := h.getFn(t, "greeter")
		return fn.Status.ServingRevision == "greeter-2" && fn.Status.DrainingRevision == ""
	}, 5*time.Second, 10*time.Millisecond, "greeter-1 drains")
	require.Contains(t, socks.log()[served:], "release "+id, "the retired worker's listener goes with it")

	require.NoError(t, h.st.Delete(context.Background(), v1.KindFunction.GVK(), "default", "greeter", ""))
	h.reconcile(t, "greeter")
	require.Contains(t, socks.log()[served:], "remove default/greeter", "the Function's Remove takes its workers' listeners")
}

// ADR-0346 Decision 8: a Job's run on an Addon's runtime runs as such a worker, on a listener of the run's own, which
// goes with the run's socket.
func TestDevAddonWorkerJob(t *testing.T) {
	t.Parallel()
	socks := &socketLog{}
	h := newExecRig(t, 1<<20, onDevImages(true, "", "funcd.host", socks))
	h.putAddon(t, denoImageA, v1.WorkloadJob)
	h.readyJob(t, func(s *v1.JobSpec) { s.Runtime = "deno2" })
	h.serveRun(t, "hello-1", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) })
	h.run(t, "hello-1", `{}`, nil)
	h.pass(t, "hello-1")
	requireEnd(t, h.end(t, "hello-1"), v1.JobRunSucceeded, "")

	specs := h.createdFor("hello-1", "hello-v1")
	require.Len(t, specs, 1)
	require.Equal(t, denoImageA, specs[0].Image)
	require.Empty(t, specs[0].Command)
	require.Equal(t, "http://funcd.host:40001", specs[0].Env["FUNCD_INVOKE_URL"])
	id := string(runtime.NewInstanceID("default", "hello-1", "hello-v1", 0))
	require.Equal(t, []string{"listen default/hello/hello-1 " + id, "remove default/hello/hello-1"}, socks.log())
}

// ADR-0346 Decision 2: no process-mode worker spec sets Image, a pool worker's included, so Pair runs every one of
// them on the process driver.
func TestProcessModeSpecsSetNoImage(t *testing.T) {
	t.Parallel()
	h := newShimHarness(t, http.StatusOK, false, withNodePool)
	for _, n := range []string{"a", "b"} {
		h.create(t, n, func(fn *v1.Function) { fn.Spec.Pooling.Worker = "w1" })
	}
	h.create(t, "solo", func(*v1.Function) {})
	h.pools(t, "a", "b")
	h.reconcile(t, "solo")
	specs := h.rt.allSpecs()
	require.Len(t, specs, 2, "the pool worker and solo's")
	for _, s := range specs {
		require.Empty(t, s.Image, "%s sets no Image", s.Name)
		require.NotEmpty(t, s.Command, "%s runs a shim command", s.Name)
	}
}
