package local

import (
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/auth"
)

// Endpoint is one endpoint of the local API and the known causes of the errors it answers (ADR-0330 Decision 4). An
// error of kind internal is implicit on every endpoint and never declared.
type Endpoint struct {
	Pattern string              // a ServeMux pattern, e.g. "PUT /kv/{binding}/{key...}", or "/fetch/" for fetch
	Causes  []*fault.KnownCause // nil: not yet described; empty: it answers no error
}

// Endpoints returns every endpoint of the local API: the six facets', registered over nil ports, then invoke, health
// and fetch (ADR-0330 Decision 8). It serves nothing.
func Endpoints() []Endpoint {
	var list endpointList
	logger := slog.New(slog.DiscardHandler)
	for _, f := range []Facet{KVFacet(nil), BlobFacet(nil), SQLFacet(nil), SecretFacet(nil), AddonFacet(nil, AddonLimits{}),
		JobFacet(nil, nil, nil)} {
		f.register(&list, Ref{}, logger)
	}
	registerInvoke(&list, logger, Ref{}, auth.Identity{}, nil, nil, nil)
	registerHealth(&list, logger, Ref{}, nil)
	return append(list, Endpoint{Pattern: fetchPattern, Causes: fetchCauses()})
}

// endpointList is the facetMux that Endpoints registers on: handle declares each endpoint to it with its causes, and a
// pattern that a facet registers past handle is an endpoint not described.
type endpointList []Endpoint

func (l *endpointList) HandleFunc(pattern string, _ func(http.ResponseWriter, *http.Request)) {
	*l = append(*l, Endpoint{Pattern: pattern})
}

func (l *endpointList) declare(pattern string, causes []*fault.KnownCause) {
	*l = append(*l, Endpoint{Pattern: pattern, Causes: causes})
}

// fetchPattern is fetch's endpoint, served around the mux (ADR-0243 Decision 6).
const fetchPattern = "/fetch/"

// fetchCauses are the causes of fetch's errors, which NewHandler's wrap and Endpoints both read: those funcd raises
// itself, since the target's reply that fetch forwards is no error of the endpoint.
func fetchCauses() []*fault.KnownCause {
	return slices.Concat([]*fault.KnownCause{ErrFetchPathMalformed}, callCauses(), []*fault.KnownCause{ErrTargetNotHTTP})
}

// handle registers h at pattern on mux through the interceptor, with the causes of the errors the endpoint answers.
// On the list that Endpoints registers on, it declares the endpoint instead.
func handle(mux facetMux, logger *slog.Logger, pattern string, causes []*fault.KnownCause,
	h func(http.ResponseWriter, *http.Request) error) {
	if d, ok := mux.(interface {
		declare(pattern string, causes []*fault.KnownCause)
	}); ok {
		d.declare(pattern, causes)
		return
	}
	mux.HandleFunc(pattern, wrap(logger, pattern, causes, h))
}

// wrap is the interceptor, the one place that answers an error of the local API (ADR-0330 Decision 3): h returns its
// error before it writes a status, and wrap answers it as a problem, keeping the headers h set, as a 412's ETag. When
// the endpoint is described and the error, not internal, has no cause, a cause it does not declare, or another kind
// than its cause's, the answer is the same and funcd logs a warning, once in the process (Decision 6).
func wrap(logger *slog.Logger, pattern string, causes []*fault.KnownCause,
	h func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := h(w, r)
		if err == nil {
			return
		}
		cause, kind := fault.KnownCauseOf(err), fault.KindOf(err)
		if record := errorRecorder.Load(); record != nil {
			(*record)(pattern, cause, kind)
		}
		if causes != nil && kind != fault.Internal {
			warnUndeclared(logger, pattern, causes, err, cause, kind)
		}
		fault.WriteProblem(w, err)
	}
}

// undeclared is one endpoint's error that funcd has logged: its cause, or its kind when it has none or when the kind
// is not its cause's.
type undeclared struct {
	pattern string
	cause   *fault.KnownCause
	kind    fault.Kind
}

// warned holds each undeclared error logged so far.
var warned sync.Map //nolint:gochecknoglobals // one warning per endpoint and cause in the process, whichever handler answers

func warnUndeclared(logger *slog.Logger, pattern string, causes []*fault.KnownCause, err error, cause *fault.KnownCause,
	kind fault.Kind) {
	key, msg := undeclared{pattern: pattern, cause: cause, kind: kind}, ""
	switch {
	case cause == nil:
		msg = "the local API answered an error with no known cause"
	case !slices.Contains(causes, cause):
		key.kind, msg = "", "the local API answered a known cause that its endpoint does not declare"
	case cause.Kind() != kind:
		msg = "the local API answered a known cause with another kind than its own"
	default:
		return
	}
	if _, seen := warned.LoadOrStore(key, true); seen {
		return
	}
	name := ""
	if cause != nil {
		name = cause.Name()
	}
	logger.Warn(msg, "endpoint", pattern, "cause", name, "kind", string(kind), "err", err.Error())
}

// errorRecorder is the production side of SetErrorRecorderForTest, which wrap reads.
var errorRecorder atomic.Pointer[func(pattern string, cause *fault.KnownCause, kind fault.Kind)] //nolint:gochecknoglobals // set only by SetErrorRecorderForTest

// SetErrorRecorderForTest is the test binary's hook (ADR-0330 Decision 7): from then on, each error that the
// interceptor of any handler in the process answers reaches record as its endpoint's pattern, its known cause (nil for
// none) and its kind. nil stops it. Only a test calls it: this package's tests refuse a call from any other file.
func SetErrorRecorderForTest(record func(pattern string, cause *fault.KnownCause, kind fault.Kind)) {
	if record == nil {
		errorRecorder.Store(nil)
		return
	}
	errorRecorder.Store(&record)
}
