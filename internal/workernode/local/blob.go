package local

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/quota"
)

// maxBlobBytes caps a blob value buffered in memory (a DoS guard on the local API, ADR-0127). Larger
// than the KV cap (blob objects are parquet/PDF, not small values) but bounded — v1 is bytes-in-memory,
// so streaming (which lifts this cap) is the named v2 follow-up.
const maxBlobBytes = 64 << 20 // 64 MiB

// Blob is the function-facing blob port the worker-node local API routes to — the services/blob.Facade
// satisfies it. The caller's namespace + function are supplied by the handler (connection-scoped from the
// sandbox's fixed Ref), NEVER read from the request: a function can only reach its own namespace's blob,
// and its binding resolution is keyed by its trustworthy fixed function identity (ADR-0127/0073).
type Blob interface {
	Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) ([]byte, bool, error)
	Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, data []byte) error
	Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error
	List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, prefix string) ([]string, error)
	SignedURL(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, opts blob.SignOptions) (string, error)
}

// minSignExpiry and maxSignExpiry bound a presign lifetime (ADR-0198): S3 signs whole seconds; SigV4 caps at 7 days.
const minSignExpiry, maxSignExpiry = time.Second, 168 * time.Hour

// BlobFacet serves the blob verbs under /blob over b (ADR-0127).
func BlobFacet(b Blob) Facet {
	return Facet{segment: "blob", register: func(mux facetMux, caller Ref, logger *slog.Logger) {
		registerBlob(mux, caller, b, logger)
	}}
}

// registerBlob adds the blob verbs to mux — GET/PUT/DELETE /blob/{binding}/{key...}, GET /blob/{binding}
// (list, ?prefix=…), and GET /blob/{binding}/{key...}?sign=1 (a presigned URL; ?method= exactly GET, PUT or
// DELETE, ?expiry= an ADR-0194 duration in whole seconds from 1s to 168h, ADR-0198) — routed to b with the
// sandbox's fixed namespace + function (ADR-0127/0073). Errors are RFC 9457 (binding/owner denial → 403, missing
// object → 404, over a cap → 413, bad input → 400, engine → 500); a bad method or expiry is 400 before the PDP. Each
// endpoint declares its known causes (ADR-0330): a write to a missing Bucket is denied before its lookup, and funcd's
// file store refuses a key on a read, a write or a delete, but not on a sign, which opens no file.
func registerBlob(mux facetMux, caller Ref, b Blob, logger *slog.Logger) {
	ns := caller.Namespace
	fn := caller.Function
	resolve := []*fault.KnownCause{blob.ErrCallerMissing, blob.ErrNoBinding}
	keyRules := []*fault.KnownCause{blob.ErrKeyNotUTF8, blob.ErrKeyReserved, blob.ErrKeyUnstorable}
	get := slices.Concat(resolve, []*fault.KnownCause{blob.ErrReadDenied, blob.ErrWriteDenied, blob.ErrBucketMissing,
		blob.ErrObjectNotFound, ErrSignMethodInvalid, ErrSignExpiryInvalid, blob.ErrSignPutCapped, blob.ErrSignUnsupported},
		keyRules)
	put := slices.Concat([]*fault.KnownCause{ErrBodyTooLarge, ErrBodyUnreadable}, resolve,
		[]*fault.KnownCause{blob.ErrWriteDenied, blob.ErrObjectOverCap, quota.ErrFloorReached, quota.ErrExceeded,
			quota.ErrUnreadable}, keyRules)
	del := slices.Concat(resolve, []*fault.KnownCause{blob.ErrWriteDenied, blob.ErrObjectNotFound}, keyRules)
	list := slices.Concat(resolve, []*fault.KnownCause{blob.ErrReadDenied, blob.ErrBucketMissing})

	handle(mux, logger, "GET /blob/{binding}/{key...}", get, func(w http.ResponseWriter, r *http.Request) error {
		const op = "workernode.local.blob.get"
		binding := r.PathValue("binding")
		key := r.PathValue("key")
		if r.URL.Query().Get("sign") != "" {
			opts, err := signOptsFromQuery(r.URL.Query())
			if err != nil {
				return err
			}
			signed, err := b.SignedURL(r.Context(), ns, fn, binding, key, opts)
			if err != nil {
				logger.Warn("blob sign denied/failed", "caller", caller.String(), "binding", binding, "err", err.Error())
				return err
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = io.WriteString(w, signed)
			return nil
		}
		v, found, err := b.Get(r.Context(), ns, fn, binding, key)
		if err != nil {
			logger.Warn("blob get denied/failed", "caller", caller.String(), "binding", binding, "err", err.Error())
			return err
		}
		if !found {
			return blob.ErrObjectNotFound.Errorf(op, "object %q not found in binding %q", key, binding)
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(v)
		return nil
	})

	handle(mux, logger, "PUT /blob/{binding}/{key...}", put, func(w http.ResponseWriter, r *http.Request) error {
		const op = "workernode.local.blob.put"
		body, err := readBody(w, r, op, maxBlobBytes)
		if err != nil {
			return err
		}
		if err := b.Put(r.Context(), ns, fn, r.PathValue("binding"), r.PathValue("key"), body); err != nil {
			logger.Warn("blob put denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})

	handle(mux, logger, "DELETE /blob/{binding}/{key...}", del, func(w http.ResponseWriter, r *http.Request) error {
		if err := b.Delete(r.Context(), ns, fn, r.PathValue("binding"), r.PathValue("key")); err != nil {
			logger.Warn("blob delete denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	})

	handle(mux, logger, "GET /blob/{binding}", list, func(w http.ResponseWriter, r *http.Request) error {
		keys, err := b.List(r.Context(), ns, fn, r.PathValue("binding"), r.URL.Query().Get("prefix"))
		if err != nil {
			logger.Warn("blob list denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
			return err
		}
		if keys == nil {
			keys = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keys)
		return nil
	})
}

// signOptsFromQuery reads ?method= and ?expiry= (ADR-0198): an absent method is blob.SignGet, an absent expiry is
// zero (the driver default), and a present value outside the rules is fault.Invalid.
func signOptsFromQuery(q url.Values) (blob.SignOptions, error) {
	const op = "workernode.local.blob.sign"
	opts := blob.SignOptions{Method: blob.SignGet}
	if q.Has("method") {
		switch m := q.Get("method"); m {
		case string(blob.SignGet), string(blob.SignPut), string(blob.SignDelete):
			opts.Method = blob.SignMethod(m)
		default:
			return blob.SignOptions{}, ErrSignMethodInvalid.Errorf(op, "method %q is not GET, PUT or DELETE", m)
		}
	}
	if !q.Has("expiry") {
		return opts, nil
	}
	e := q.Get("expiry")
	d, err := v1.ParseDuration(e)
	if err != nil {
		return blob.SignOptions{}, ErrSignExpiryInvalid.Errorf(op, "expiry %q is not a duration string such as 10m or 1h30m", e)
	}
	opts.Expiry = time.Duration(d)
	if opts.Expiry%time.Second != 0 || opts.Expiry < minSignExpiry || opts.Expiry > maxSignExpiry {
		return blob.SignOptions{}, ErrSignExpiryInvalid.Errorf(op, "expiry %q must be a whole number of seconds from 1s to 168h", e)
	}
	return opts, nil
}
