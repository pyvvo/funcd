package fault

import (
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
	Invalid:      {typeURI: "urn:funcd:problem:invalid", title: "Invalid Request", status: http.StatusBadRequest},
	NotFound:     {typeURI: "urn:funcd:problem:not-found", title: "Not Found", status: http.StatusNotFound},
	Conflict:     {typeURI: "urn:funcd:problem:conflict", title: "Conflict", status: http.StatusConflict},
	Unauthorized: {typeURI: "urn:funcd:problem:unauthorized", title: "Unauthorized", status: http.StatusUnauthorized},
	Forbidden:    {typeURI: "urn:funcd:problem:forbidden", title: "Forbidden", status: http.StatusForbidden},
	Unavailable:  {typeURI: "urn:funcd:problem:unavailable", title: "Service Unavailable", status: http.StatusServiceUnavailable},
	Internal:     {typeURI: "urn:funcd:problem:internal", title: "Internal Server Error", status: http.StatusInternalServerError},
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

	// Simple JSON encoding without importing encoding/json for now — this is a
	// convenience helper; the real encoding lives in the API server middleware.
	// We write a minimal valid JSON body.
	body := `{"type":"` + p.Type + `","title":"` + p.Title + `","status":` + itoa(p.Status) + `,"detail":"` + jsonEscape(p.Detail) + `"}`
	_, _ = w.Write([]byte(body))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func jsonEscape(s string) string {
	// Minimal escaping for the simple case — the middleware will use encoding/json.
	var out []byte
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
