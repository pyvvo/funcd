//go:build dev

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/lake/embedengine"
)

const lateLakeToken = `apiVersion: funcd.io/v1alpha1
kind: Secret
metadata:
  name: late-lake-quack-token
spec:
  type: Opaque
  data:
    QUACK_TOKEN: bGF0ZQ==
`

const lateLake = `apiVersion: funcd.io/v1alpha1
kind: Lake
metadata:
  name: late-lake
spec:
  buckets:
    - name: late-lakehouse
      prefixes:
        - name: lake
  secrets:
    - name: late-lake-quack-token
      keys:
        - QUACK_TOKEN
  bindings:
    blob:
      - alias: lake
        bucket: late-lakehouse
        prefix: lake
    secret:
      - late-lake-quack-token
  location:
    bucket: late-lakehouse
    prefix: lake
`

// requireLakeOnDevEngine waits until dev's own engine has Lake name: Ready on a build that embeds the engine, else
// Pending on its not-bundled reason, as a Lake present at start is (ADR-0130 scenario dev-catalog-not-bundled).
func requireLakeOnDevEngine(t *testing.T, inst *devInstance, name v1.ObjectName) {
	t.Helper()
	want := "LakeEngineNotBundled"
	if embedengine.Bundled() {
		want = string(v1.PhaseReady)
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		obj, err := inst.client.Get(context.Background(), v1.KindLake, devNamespace, name)
		if !assert.NoError(c, err) {
			return
		}
		lake := obj.(*v1.Lake)
		got := string(lake.Status.Phase)
		if lake.Status.Phase != v1.PhaseReady {
			cond, _ := lake.Status.Conditions.Get("Ready")
			got = string(cond.Reason)
		}
		assert.Equal(c, want, got)
	}, 60*time.Second, 200*time.Millisecond, "dev's engine has Lake %s", name)
}

// A Lake file added while dev runs a folder gets dev's engine, as one present at start does.
func TestIssue1409_FolderDevServesALakeAddedWhileRunning(t *testing.T) {
	requireRuntime(t)
	files := nodeFunction("api", "", "export function handle() { return {}; }\n")
	files["token.yaml"] = lateLakeToken
	dir := devProject(t, files)
	inst, err := tryStartDev(t, dir)
	require.NoError(t, err)
	writeProject(t, dir, map[string]string{"lake.yaml": lateLake})
	requireLakeOnDevEngine(t, inst, "late-lake")
}

// A values change that adds the todo App's Lake while dev runs it gets dev's engine (ADR-0253 scenario app-dev-reload).
func TestIssue1409_AppDevServesALakeAddedWhileRunning(t *testing.T) {
	requireApp(t)
	t.Setenv("TODO_API_TOKEN", todoToken)
	root := appCopy(t, "todo")
	later := filepath.Join(root, "values/later.yaml")
	require.NoError(t, os.WriteFile(later, nil, 0o600))
	r := startApp(t, root, todoCfg(func(c *devConfig) {
		c.values = []string{"values/later.yaml"}
		c.apply = []string{"dr/lake-fixtures.yaml"}
	}))
	r.serves(t)
	require.NoError(t, os.WriteFile(later, []byte("lake: true\n"), 0o600))
	requireLakeOnDevEngine(t, r.inst, "todo-lake")
}
