package s3gateway

import (
	"strconv"
	"strings"
)

// parseRange parses an HTTP Range header value of the form "bytes=<start>-<end>" or
// "bytes=<start>-" into a (offset, length) for blob.RangeReader (length<0 = to end).
// An empty header ⇒ ranged=false (a full read). Only the single-range "bytes=" form
// DuckDB uses is supported; anything else is an error. Suffix ranges ("bytes=-N") are
// not supported by the blob seam ⇒ treated as a full read of the tail is avoided; an
// error is returned so the caller maps it to 416.
func parseRange(header string) (offset, length int64, ranged bool, err error) {
	if header == "" {
		return 0, 0, false, nil
	}
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, false, errBadRange
	}
	spec := strings.TrimPrefix(header, prefix)
	if strings.Contains(spec, ",") {
		return 0, 0, false, errBadRange // multi-range unsupported
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false, errBadRange
	}
	startStr, endStr := spec[:dash], spec[dash+1:]
	if startStr == "" {
		return 0, 0, false, errBadRange // suffix range unsupported by the blob seam
	}
	start, perr := strconv.ParseInt(startStr, 10, 64)
	if perr != nil || start < 0 {
		return 0, 0, false, errBadRange
	}
	if endStr == "" {
		return start, -1, true, nil // open-ended → to end
	}
	end, perr := strconv.ParseInt(endStr, 10, 64)
	if perr != nil || end < start {
		return 0, 0, false, errBadRange
	}
	return start, end - start + 1, true, nil
}

// errBadRange is a sentinel for an unparseable/unsupported Range header.
//
//nolint:gochecknoglobals // a package-internal sentinel error
var errBadRange = rangeError("invalid or unsupported Range header")

type rangeError string

func (e rangeError) Error() string { return string(e) }
