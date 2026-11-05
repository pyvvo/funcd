package fault

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Problem is an RFC 9457 application/problem+json body.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance,omitempty"`
}

// kindProblem maps each Kind to its problem+json type URI and HTTP status.
//
//nolint:gochecknoglobals // lookup table, effectively constant
var kindProblem = map[Kind]struct {
	typeURI string
	title   string
	status  int
}{
	Invalid:             {typeURI: "urn:funcd:problem:invalid", title: "Invalid Request", status: http.StatusBadRequest},
	NotFound:            {typeURI: "urn:funcd:problem:not-found", title: "Not Found", status: http.StatusNotFound},
	Conflict:            {typeURI: "urn:funcd:problem:conflict", title: "Conflict", status: http.StatusConflict},
	Unauthorized:        {typeURI: "urn:funcd:problem:unauthorized", title: "Unauthorized", status: http.StatusUnauthorized},
	Forbidden:           {typeURI: "urn:funcd:problem:forbidden", title: "Forbidden", status: http.StatusForbidden},
	Unavailable:         {typeURI: "urn:funcd:problem:unavailable", title: "Service Unavailable", status: http.StatusServiceUnavailable},
	ResourceExhausted:   {typeURI: "urn:funcd:problem:resource-exhausted", title: "Too Many Requests", status: http.StatusTooManyRequests},
	PayloadTooLarge:     {typeURI: "urn:funcd:problem:payload-too-large", title: "Content Too Large", status: http.StatusRequestEntityTooLarge},
	InsufficientStorage: {typeURI: "urn:funcd:problem:insufficient-storage", title: "Insufficient Storage", status: http.StatusInsufficientStorage},
	DeadlineExceeded:    {typeURI: "urn:funcd:problem:deadline-exceeded", title: "Gateway Timeout", status: http.StatusGatewayTimeout},
	Internal:            {typeURI: "urn:funcd:problem:internal", title: "Internal Server Error", status: http.StatusInternalServerError},
}

// ToProblem maps an error to an RFC 9457 Problem using KindOf to select the
// type URI and status. This is the single site where HTTP status codes are
// decided for the platform.
func ToProblem(err error) Problem {
	k := KindOf(err)
	info, ok := kindProblem[k]
	if !ok {
		info = kindProblem[Internal]
	}

	p := Problem{
		Type:   info.typeURI,
		Title:  info.title,
		Status: info.status,
		Detail: detailOf(err, info.status),
	}

	return p
}

// detailOf is the detail of err's problem. A 4xx is the caller's to correct, so it keeps the whole text. A 500 carries
// the operation and its message, the part ADR-0002 calls safe to surface, or a fixed text when it has no message. Any
// other 5xx also keeps the faults of its own kind beneath it, which name a state a caller reads (ADR-0169's "is Failed
// (<reason>)", a failed replay's answer). The rest goes to funcd's log (LogCauses).
func detailOf(err error, status int) string {
	if status < http.StatusInternalServerError {
		return err.Error()
	}
	var ferr *Error
	if !errors.As(err, &ferr) || ferr.Msg == "" {
		return "internal error: the cause is in funcd's log"
	}
	if status == http.StatusInternalServerError {
		return ferr.Op + ": " + ferr.Msg
	}
	return faultText(ferr, ferr.Kind)
}

// faultText is the operation and message of each fault in err's chain, down to the first cause that is not a fault of
// kind k.
func faultText(err error, k Kind) string {
	var ferr *Error
	if !errors.As(err, &ferr) || ferr.Kind != k {
		return ""
	}
	if rest := faultText(ferr.Err, k); rest != "" {
		return ferr.Op + ": " + ferr.Msg + ": " + rest
	}
	return ferr.Op + ": " + ferr.Msg
}

// StatusOf returns the HTTP status WriteProblem writes for err.
func StatusOf(err error) int {
	return ToProblem(err).Status
}

// WriteProblem writes an error as an RFC 9457 application/problem+json response.
// It sets Content-Type and writes the JSON body.
func WriteProblem(w http.ResponseWriter, err error) {
	LogCause(w, err)
	p := ToProblem(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// LogCauses is the middleware of an HTTP operation's owner, which logs a failure once (ADR-0002 §3): through logger, it
// logs the error behind each problem whose detail leaves part of it out. It sits inside every other ResponseWriter
// wrapper, because LogCause finds it on the writer the handler holds.
func LogCauses(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(&causeWriter{ResponseWriter: w, r: r, logger: logger}, r)
		})
	}
}

// causeWriter carries the owner's logger to WriteProblem, which has only the ResponseWriter. Unwrap keeps
// http.ResponseController reaching the writer beneath it.
type causeWriter struct {
	http.ResponseWriter
	r      *http.Request
	logger *slog.Logger
}

func (c *causeWriter) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// LogCause logs err through the logger LogCauses put on w when err's problem detail leaves part of it out: a 500 at
// Error, any other status at Warn. WriteProblem calls it, and so does a transport that renders ToProblem itself
// (ADR-0005 §4). Without LogCauses it does nothing: a handler outside it logs what it is handed before it answers.
func LogCause(w http.ResponseWriter, err error) {
	c, ok := w.(*causeWriter)
	if !ok {
		return
	}
	p := ToProblem(err)
	if p.Detail == err.Error() {
		return
	}
	level := slog.LevelWarn
	if p.Status == http.StatusInternalServerError {
		level = slog.LevelError
	}
	c.logger.Log(c.r.Context(), level, "request failed", "status", p.Status, "error", err)
}
