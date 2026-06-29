package compact

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Raw/compacted object naming under the shared logs/ prefix (ADR-0081 sink + ADR-0083 compaction):
//
//	raw: logs/<ns>/<fn>/<date>/<sealNano>-<replica>.otlp.jsonl   (funclog sink segmentKey)
//	compacted: logs/<ns>/<fn>/<date>/<windowStart>.parquet             (this compactor)
//
// Both are 5 slash-separated parts; the suffix disambiguates them. A listing of logs/ returns every
// object across all <date> dirs (the blob.Bucket.List is a flat recursive listing), so a window that
// straddles a UTC-midnight date boundary is still discovered intact.
const (
	logsPrefix      = "logs/"
	rawSuffix       = ".otlp.jsonl"
	compactedSuffix = ".parquet"
)

// rawObject is a parsed funclog raw key. sealNano is the sink's SEAL time (not event time): it is
// monotonic w.r.t. real time and at most segmentMaxAge (~10s) behind the data — which is why windowing on
// it with a now>=windowStart+Window close needs no grace period.
type rawObject struct {
	key      string
	ns       string
	fn       string
	sealNano int64
}

// parseRawKey decodes a raw object key. ok=false for anything that is not a well-formed raw object
// (a compacted .parquet, or a malformed name) — the caller skips it.
func parseRawKey(key string) (rawObject, bool) {
	if !strings.HasPrefix(key, logsPrefix) || !strings.HasSuffix(key, rawSuffix) {
		return rawObject{}, false
	}
	parts := strings.Split(key, "/")
	if len(parts) != 5 { // logs / ns / fn / date / base
		return rawObject{}, false
	}
	ns, fn := parts[1], parts[2]
	if ns == "" || fn == "" {
		return rawObject{}, false
	}
	base := strings.TrimSuffix(parts[4], rawSuffix) // <sealNano>-<replica>
	dash := strings.IndexByte(base, '-')
	if dash <= 0 {
		return rawObject{}, false
	}
	nano, err := strconv.ParseInt(base[:dash], 10, 64)
	if err != nil || nano <= 0 {
		return rawObject{}, false
	}
	return rawObject{key: key, ns: ns, fn: fn, sealNano: nano}, true
}

// windowStartNano floors sealNano to the start of its window. A non-positive window yields sealNano itself.
func windowStartNano(sealNano int64, window time.Duration) int64 {
	w := int64(window)
	if w <= 0 {
		return sealNano
	}
	return (sealNano / w) * w
}

// compactedKey is the deterministic Hive-partitioned compacted key for one (ns, fn, windowStart):
// logs/<ns>/<fn>/<date>/<windowStart>.parquet, date = the UTC date of windowStart. Deterministic by design
// — a retry over the same window re-Puts the same key (idempotent overwrite, no duplicate object).
func compactedKey(ns, fn string, windowStart int64) string {
	date := time.Unix(0, windowStart).UTC().Format("2006-01-02")
	return fmt.Sprintf("%s%s/%s/%s/%d%s", logsPrefix, ns, fn, date, windowStart, compactedSuffix)
}

// parseCompactedWindowStart extracts the windowStart nanos from a compacted key (for retention pruning).
// ok=false if the key is not a well-formed compacted object.
func parseCompactedWindowStart(key string) (int64, bool) {
	if !strings.HasPrefix(key, logsPrefix) || !strings.HasSuffix(key, compactedSuffix) {
		return 0, false
	}
	parts := strings.Split(key, "/")
	if len(parts) != 5 {
		return 0, false
	}
	nano, err := strconv.ParseInt(strings.TrimSuffix(parts[4], compactedSuffix), 10, 64)
	if err != nil || nano <= 0 {
		return 0, false
	}
	return nano, true
}
