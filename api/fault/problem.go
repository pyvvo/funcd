package fault

import (
	"encoding/json"
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
	Invalid:           {typeURI: "urn:funcd:problem:invalid", title: "Invalid Request", status: http.StatusBadRequest},
	NotFound:          {typeURI: "urn:funcd:problem:not-found", title: "Not Found", status: http.StatusNotFound},
	Conflict:          {typeURI: "urn:funcd:problem:conflict", title: "Conflict", status: http.StatusConflict},
	Unauthorized:      {typeURI: "urn:funcd:problem:unauthorized", title: "Unauthorized", status: http.StatusUnauthorized},
	Forbidden:         {typeURI: "urn:funcd:problem:forbidden", title: "Forbidden", status: http.StatusForbidden},
	Unavailable:       {typeURI: "urn:funcd:problem:unavailable", title: "Service Unavailable", status: http.StatusServiceUnavailable},
	ResourceExhausted: {typeURI: "urn:funcd:problem:resource-exhausted", title: "Too Many Requests", status: http.StatusTooManyRequests},
	PayloadTooLarge:   {typeURI: "urn:funcd:problem:payload-too-large", title: "Content Too Large", status: http.StatusRequestEntityTooLarge},
	Internal:          {typeURI: "urn:funcd:problem:internal", title: "Internal Server Error", status: http.StatusInternalServerError},
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
		Detail: err.Error(),
	}

	// Surface detail from a ferr.Error if present.
	var ferr *Error
	// errors.As is imported via fault.go but we only use stdlib here.
	// Just use the KindOf result — the detail is already in err.Error().
	_ = ferr // keep the var for potential future use

	return p
}

// WriteProblem writes an error as an RFC 9457 application/problem+json response.
// It sets Content-Type and writes the JSON body.
func WriteProblem(w http.ResponseWriter, err error) {
	p := ToProblem(err)
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
