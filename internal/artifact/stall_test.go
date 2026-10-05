package artifact_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// stallWait is how long a call may take against the stalling registry: the bound on one wait for response headers,
// one retry and a margin.
const stallWait = 30 * time.Second

// A registry that accepts a request and never answers ends the call after a bounded wait, so the reconcile that made
// it fails and the controller's only worker moves on to the other objects (#697). Not parallel: the registry client
// trusts the test registry through http.DefaultTransport, which it copies.
func TestIssue697_StalledRegistryCallEnds(t *testing.T) {
	host, requests := stallingRegistry(t)
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	m := artifact.NewOrasMaterializer(t.TempDir(), "")
	for repo, call := range map[string]func(ref string) error{
		"resolve":   func(ref string) error { _, err := m.Resolve(context.Background(), ref); return err },
		"platforms": func(ref string) error { _, err := m.Platforms(context.Background(), ref, digest); return err },
		"materialize": func(ref string) error {
			fn := &v1.Function{}
			fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
			_, err := m.Materialize(context.Background(), fn)
			return err
		},
		"site": func(ref string) error { _, err := artifact.ResolveSite(context.Background(), ref); return err },
		"contract": func(ref string) error {
			_, _, err := artifact.InspectContract(context.Background(), ref, digest)
			return err
		},
	} {
		t.Run(repo, func(t *testing.T) {
			t.Parallel()
			done := make(chan error, 1)
			go func() { done <- call(host + "/" + repo + "/fn:latest") }()
			select {
			case err := <-done:
				require.Error(t, err)
				require.Equal(t, 2, requests(repo), "the stalled request is abandoned and retried")
			case <-time.After(stallWait):
				t.Fatalf("the call still waits on the stalled registry after %s", stallWait)
			}
		})
	}

	t.Run("platform", func(t *testing.T) {
		t.Parallel()
		dir, err := os.MkdirTemp("", "funcd")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		st := store.New(memory.New())
		p, err := funcd.New(funcd.InMemory(), funcd.WithStore(st), funcd.WithRuntimeShim("/bin/sh", "-c", "sleep 600"),
			funcd.WithArtifactStore(filepath.Join(dir, "artifacts")))
		require.NoError(t, err)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- p.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			<-done
			_ = p.Shutdown(context.Background())
		})
		create := func(kind v1.Kind, name v1.ObjectName, spec func(v1.Object)) func() v1.Phase {
			obj, _ := v1.NewObject(kind)
			meta := obj.GetObjectMeta()
			meta.Name, meta.Namespace, meta.ResourceGroup = name, "default", "rg1"
			spec(obj)
			_, cerr := st.Create(ctx, obj)
			require.NoError(t, cerr)
			return func() v1.Phase {
				got, gerr := st.Get(ctx, kind.GVK(), "default", name)
				require.NoError(t, gerr)
				return got.(v1.StatusObject).GetStatus().Phase
			}
		}
		kvStore := func(o v1.Object) { o.(*v1.KVStore).Spec.Tables = []v1.KVTable{{Name: "t"}} }

		before := create(v1.KindKVStore, "before", kvStore)
		require.Eventually(t, func() bool { return before() == v1.PhaseReady }, 5*time.Second, 10*time.Millisecond)
		fn := create(v1.KindFunction, "fn", func(o v1.Object) {
			f := o.(*v1.Function)
			f.Spec.Runtime, f.Spec.Handler, f.Spec.Image = "nodejs22", "handle", host+"/platform/fn:latest"
		})
		require.Eventually(t, func() bool { return requests("platform") == 1 }, 5*time.Second, 10*time.Millisecond,
			"the Function's reconcile waits on the registry")
		after := create(v1.KindKVStore, "after", kvStore)
		require.Eventually(t, func() bool { return after() == v1.PhaseReady }, stallWait, 50*time.Millisecond,
			"a KVStore applied while a Function's reconcile waits on a stalled registry never reconciles")
		require.Eventually(t, func() bool { return fn() == v1.PhaseFailed }, 5*time.Second, 10*time.Millisecond,
			"the Function reports its failed pull")
	})
}

// stallingRegistry serves a registry over TLS that the registry client trusts: the first request for a repository
// waits, unanswered, until the test ends, and a later one is answered 404. requests counts a repository's requests.
func stallingRegistry(t *testing.T) (host string, requests func(repo string) int) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	var mu sync.Mutex
	counts := map[string]int{}
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repo, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")
		mu.Lock()
		counts[repo]++
		first := counts[repo] == 1
		mu.Unlock()
		if !first {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	srv.StartTLS()
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	tr := http.DefaultTransport.(*http.Transport)
	trust := tr.TLSClientConfig
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() { tr.TLSClientConfig = trust })
	return strings.TrimPrefix(srv.URL, "https://"), func(repo string) int {
		mu.Lock()
		defer mu.Unlock()
		return counts[repo]
	}
}
