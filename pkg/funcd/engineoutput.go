package funcd

import (
	"bufio"
	"context"
	"strconv"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/internal/runtime"
	"github.com/pyvvo/funcd/internal/runtime/workerpipe"
)

const engineOutputBuf = 64 * 1024

// logEngineOutput reads each stream of an engine worker once (ADR-0342 Decision 1). Each line goes to the daemon's log
// at info (ADR-0276): engines write routine lines to stderr, so the stream is an attribute, not the level. The lines of
// a Lake's, a SQLDatabase's or an add-on object's engine are kept in funclog too, at INFO, and sealed at the stream's end.
func (e *executor) logEngineOutput(spec runtime.WorkerSpec, out *workerpipe.Output) {
	res, keep := engineResource(spec)
	for _, src := range []struct {
		stream workerpipe.Stream
		source funclog.Source
	}{{workerpipe.Stdout, funclog.SourceStdout}, {workerpipe.Stderr, funclog.SourceStderr}} {
		r := out.Reader(src.stream)
		e.logRoutes.start(r, func() {
			sc := bufio.NewScanner(r)
			sc.Buffer(make([]byte, 0, engineOutputBuf), funclog.MaxLineBytes)
			for sc.Scan() {
				e.cfg.logger.Info("engine output", "stream", string(src.source), "kind", string(spec.OwnerKind),
					"namespace", string(spec.Namespace), "name", string(spec.Name), "replica", strconv.Itoa(spec.Replica),
					"line", sc.Text())
				if keep {
					e.keepEngineLine(res, src.source, sc.Text())
				}
			}
			if keep && e.logSink != nil {
				_, _ = e.logSink.Flush(context.Background(), res)
			}
		})
	}
}

// engineResource is the funclog identity of an engine worker's lines and whether they are kept: those of the engines of
// Lakes, SQLDatabases and add-on objects, the add-on's under the kind its object is served as (ADR-0342 Decisions 1
// and 2). Another kind's engine keeps the daemon's log only.
func engineResource(spec runtime.WorkerSpec) (funclog.Resource, bool) {
	kind := spec.OwnerKind
	switch kind {
	case v1.KindLake, v1.KindSQLDatabase:
	case v1.KindAddonObject:
		kind = spec.ServedKind
	default:
		return funclog.Resource{}, false
	}
	if kind == "" {
		return funclog.Resource{}, false
	}
	return funclog.Resource{Namespace: string(spec.Namespace), Kind: string(kind), Name: string(spec.Name),
		Replica: strconv.Itoa(spec.Replica)}, true
}

// followDevLines makes a runtime that reads its engines' output itself, one of dev's, hand each line to keep, under kind
// when the spec names none: dev's add-on runtimes (ADR-0342 Decision 5) and its Lake and SQL engine runners (ADR-0343).
func followDevLines(rt provider.Runtime, kind v1.Kind, keep func(spec provider.ProviderSpec, stream, line string)) {
	lf, ok := rt.(provider.LineFollower)
	if !ok || keep == nil {
		return
	}
	lf.FollowLines(func(spec provider.ProviderSpec, stream, line string) {
		if spec.ServedKind == "" {
			spec.ServedKind = kind
		}
		keep(spec, stream, line)
	})
}

// keepDevEngineLine keeps a line that one of dev's runtimes reads from an engine (ADR-0342 Decision 5, ADR-0343): in
// funclog only, past dev's printing tee and never in the daemon's log, which is dev's terminal, so a failed append is
// dropped unlogged.
func (e *executor) keepDevEngineLine(spec provider.ProviderSpec, stream, line string) {
	if spec.ServedKind == "" || e.logSink == nil {
		return
	}
	res := funclog.Resource{Namespace: string(spec.Ref.Namespace), Kind: string(spec.ServedKind),
		Name: string(spec.Ref.Name), Replica: "0"}
	_ = e.logSink.Append(context.Background(), res, engineEntry(funclog.Source(stream), line))
}

// keepEngineLine appends one engine line to funclog's sink, past funcdctl dev's printing tee.
func (e *executor) keepEngineLine(res funclog.Resource, src funclog.Source, line string) {
	if e.logSink == nil {
		return
	}
	if err := e.logSink.Append(context.Background(), res, engineEntry(src, line)); err != nil {
		e.cfg.logger.Warn("funclog: append failed", "kind", res.Kind, "name", res.Name, "error", err)
	}
}

// engineEntry is an engine's line as a raw record: stamped when read, at INFO for both streams (ADR-0342 Decision 1).
func engineEntry(src funclog.Source, line string) funclog.Entry {
	return funclog.Entry{Time: time.Now(), Severity: funclog.SevInfo, Body: line, Source: src}
}
