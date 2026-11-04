package funcd

import (
	"bufio"
	"strconv"

	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

const engineOutputBuf = 64 * 1024

// logEngineOutput sends each line an engine worker writes to the daemon's log at info (ADR-0276). Engines write routine
// lines to stderr, so the stream is an attribute, not the level.
func (e *executor) logEngineOutput(spec runtime.WorkerSpec, out *workerpipe.Output) {
	for _, src := range []struct {
		stream workerpipe.Stream
		name   string
	}{{workerpipe.Stdout, "stdout"}, {workerpipe.Stderr, "stderr"}} {
		r := out.Reader(src.stream)
		e.logRoutes.start(r, func() {
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 0, engineOutputBuf), funclog.MaxLineBytes)
			for sc.Scan() {
				e.cfg.logger.Info("engine output", "stream", src.name, "kind", string(spec.OwnerKind),
					"namespace", string(spec.Namespace), "name", string(spec.Name), "replica", strconv.Itoa(spec.Replica),
					"line", sc.Text())
			}
		})
	}
}
