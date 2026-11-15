package funcd

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/pyvvo/funcd/internal/controlplane"
)

// answerRecorder is the production side of the test hook of ADR-0329 Decision 4, read by New, which wraps both servers
// when it is set: each answer the control plane or the gateway writes reaches it as the operation and the status.
var answerRecorder atomic.Pointer[func(operationID string, status int)] //nolint:gochecknoglobals // set only by SetAnswerRecorderForTest

// recordControlPlane returns the control plane's handler wrapped outermost by the recorder, so the 401 that the
// authentication middleware writes is seen too, or h itself when no recorder is set.
func recordControlPlane(h http.Handler) http.Handler {
	record := answerRecorder.Load()
	if record == nil {
		return h
	}
	return recordAnswers(h, func(r *http.Request) string { return controlplane.OperationOf(h, r) }, *record)
}

// recordGateway returns the gateway's handler wrapped outermost by the recorder, or h itself when none is set.
func recordGateway(h http.Handler) http.Handler {
	record := answerRecorder.Load()
	if record == nil {
		return h
	}
	return recordAnswers(h, gatewayOperation, *record)
}

// gatewayOperation resolves a request against the gateway's one documented operation, invokeFunction: POST
// /function/{name}.
func gatewayOperation(r *http.Request) string {
	name, ok := strings.CutPrefix(r.URL.Path, "/function/")
	if r.Method != http.MethodPost || !ok || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return "invokeFunction"
}

// recordAnswers wraps h so the status of each answer it writes reaches record under the operation that resolve finds
// for the request. An answer that resolves to no operation is not recorded, nor is one whose connection the handler
// takes over, since its status never passes the writer.
func recordAnswers(h http.Handler, resolve func(*http.Request) string, record func(string, int)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := resolve(r)
		if id == "" {
			h.ServeHTTP(w, r)
			return
		}
		aw := &answerWriter{ResponseWriter: w}
		h.ServeHTTP(aw, r)
		if !aw.hijacked {
			if aw.status == 0 {
				aw.status = http.StatusOK
			}
			record(id, aw.status)
		}
	})
}

// answerWriter keeps the status of the answer written through it. It forwards Flush and Hijack, which the edge's
// wrappers look for, and Unwrap, which http.ResponseController follows, so streams and upgrades still work.
type answerWriter struct {
	http.ResponseWriter
	status   int
	hijacked bool
}

func (a *answerWriter) WriteHeader(code int) {
	if a.status == 0 && (code >= http.StatusOK || code == http.StatusSwitchingProtocols) {
		a.status = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *answerWriter) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	return a.ResponseWriter.Write(b)
}

func (a *answerWriter) Flush() {
	if a.status == 0 {
		a.status = http.StatusOK
	}
	_ = http.NewResponseController(a.ResponseWriter).Flush()
}

func (a *answerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, brw, err := http.NewResponseController(a.ResponseWriter).Hijack()
	if err == nil {
		a.hijacked = true
	}
	return conn, brw, err
}

func (a *answerWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }
