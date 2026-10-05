//go:build dev

package devengine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/catalog/embedengine"
	"github.com/pyvvo/funcd/internal/runtime/procreg"
)

// crashedRunEnv makes the test binary run a runtime with a state dir that converges one fake engine, until killed.
const crashedRunEnv = "DEVENGINE_TEST_CRASHED_RUN"

func runUntilKilled(dir string) int {
	exe, err := os.Executable()
	if err != nil {
		return 2
	}
	if err := os.Setenv(fakeEngineEnv, "1"); err != nil {
		return 2
	}
	r, err := New(nil, WithStateDir(dir))
	if err != nil {
		return 2
	}
	r.bundled = func() bool { return true }
	r.extract = func(d string) (embedengine.Paths, error) { return embedengine.Paths{DuckDB: exe, ExtensionDir: d}, nil }
	if _, err := r.Converge(context.Background(), testSpec()); err != nil {
		return 2
	}
	select {}
}

func savedEngines(t *testing.T, dir string) []procreg.Entry {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "engines.json"))
	if err != nil {
		return nil
	}
	var out []procreg.Entry
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decode engines.json: %v", err)
	}
	return out
}

// A runtime with a state dir saves each engine it starts, with the funcd-engine-<id> token in its -init argv, and the
// next New on that dir reaps what a killed run left: the engine is gone, so is its engine dir (ADR-0167).
func TestStateDirReapsEnginesOfACrashedRun(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), crashedRunEnv+"="+dir)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the first run: %v", err)
	}
	var saved []procreg.Entry
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if saved = savedEngines(t, dir); len(saved) == 1 && procreg.Alive(saved[0]) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first run saved no live engine: %+v", saved)
		}
	}
	engine := saved[0]
	if len(engine.Files) != 1 || filepath.Base(engine.Files[0])[:len(engine.Token)] != engine.Token {
		t.Fatalf("the engine dir %v is not named after the token %q", engine.Files, engine.Token)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	r := mustNew(t, WithStateDir(dir))
	t.Cleanup(r.StopAll)
	if procreg.Alive(engine) {
		t.Fatalf("engine %d of the killed run still runs", engine.PID)
	}
	if _, err := os.Stat(engine.Files[0]); !os.IsNotExist(err) {
		t.Fatalf("engine dir %s survived the reap: %v", engine.Files[0], err)
	}
	if left := savedEngines(t, dir); len(left) != 0 {
		t.Fatalf("the registry still names %+v", left)
	}
	if _, err := New(nil, WithStateDir(dir)); err == nil {
		t.Fatal("a second runtime opened the registry another one holds")
	}
}
