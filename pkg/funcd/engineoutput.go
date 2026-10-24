package funcd

import (
	"bufio"
	"strconv"

	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

// logEngineOutput sends each line an engine worker writes to the daemon's log at info (ADR-0276). Engines write routine
// lines to stderr, so the stream is an attribute, not the level.
func (p *Platform) logEngineOutput(spec runtime.WorkerSpec, out *workerpipe.Output) {
	for _, src := range []struct {
		stream workerpipe.Stream
		name   string
	}{{workerpipe.Stdout, "stdout"}, {workerpipe.Stderr, "stderr"}} {
		r := out.Reader(src.stream)
		p.logRoutes.start(r, func() {
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 0, 64*1024), funclog.MaxLineBytes)
			for sc.Scan() {
				p.logger.Info("engine output", "stream", src.name, "kind", string(spec.OwnerKind),
					"namespace", string(spec.Namespace), "name", string(spec.Name), "replica", strconv.Itoa(spec.Replica),
					"line", sc.Text())
			}
		})
	}
}
