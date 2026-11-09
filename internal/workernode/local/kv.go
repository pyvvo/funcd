package local

import (
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/kvstore"
)

// maxKVBytes caps a KV put body (a DoS guard on the local API). It is the KVStore value-cap ceiling, so
// every cap a store can declare is servable.
const maxKVBytes = v1.MaxValueBytesLimit

// KV is the function-facing KV port the worker-node local API routes to — the services/kv.Facade
// satisfies it. The caller's namespace + function are supplied by the handler (connection-scoped from
// the sandbox's fixed Ref), NEVER read from the request: a function can only reach its own namespace's
// KV, and its binding resolution is keyed by its trustworthy fixed function identity (ADR-0073).
type KV interface {
	GetEntry(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) (kvstore.Entry, bool, error)
	Put(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, value []byte) error
	PutIf(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string, value []byte, w kvstore.Write) (kvstore.Result, error)
	Delete(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key string) error
	DeleteIf(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, key, version string) (kvstore.Result, error)
	List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, binding, prefix string) ([]string, error)
}

// KVFacet serves the KV verbs under /kv over kv (ADR-0069).
func KVFacet(kv KV) Facet {
	return Facet{segment: "kv", register: func(mux facetMux, caller Ref, logger *slog.Logger) {
		registerKV(mux, caller, kv, logger)
	}}
}

// registerKV adds the KV verbs to mux — GET/PUT/DELETE /kv/{binding}/{key...} and GET /kv/{binding} (list,
// ?prefix=…) — routed to kv with the sandbox's fixed namespace + function (ADR-0069/0073). A GET of a key and a
// conditional write answer the key's version as a strong ETag. A PUT with If-None-Match: * creates (201), one with
// If-Match: "<version>" replaces, a DELETE with If-Match deletes, each only while its condition holds, else 412 with
// the current version's ETag when the key exists; ?ttl=<seconds> makes a PUT's key expire. Errors are RFC 9457 (the
// Facade's binding/owner denial → 403, missing key → 404, over a cap → 413, bad input → 400, engine error → 500).
func registerKV(mux facetMux, caller Ref, kv KV, logger *slog.Logger) {
	ns := caller.Namespace
	fn := caller.Function
	denied := func(verb string, r *http.Request, err error) {
		logger.Warn("kv "+verb+" denied/failed", "caller", caller.String(), "binding", r.PathValue("binding"), "err", err.Error())
	}

	mux.HandleFunc("GET /kv/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.kv.get"
		e, found, err := kv.GetEntry(r.Context(), ns, fn, r.PathValue("binding"), r.PathValue("key"))
		if err != nil {
			denied("get", r, err)
			fault.WriteProblem(w, err)
			return
		}
		if !found {
			fault.WriteProblem(w, fault.NotFoundf(op, "key %q not found in binding %q", r.PathValue("key"), r.PathValue("binding")))
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", etag(e.Version))
		_, _ = w.Write(e.Value)
	})

	mux.HandleFunc("PUT /kv/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.kv.put"
		cond, err := putCondition(op, r)
		if err != nil {
			fault.WriteProblem(w, err)
			return
		}
		body, err := readBody(w, r, op, maxKVBytes)
		if err != nil {
			fault.WriteProblem(w, err)
			return
		}
		binding, key := r.PathValue("binding"), r.PathValue("key")
		if cond == (kvstore.Write{}) {
			if err := kv.Put(r.Context(), ns, fn, binding, key, body); err != nil {
				denied("put", r, err)
				fault.WriteProblem(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		res, err := kv.PutIf(r.Context(), ns, fn, binding, key, body, cond)
		if err != nil {
			denied("put", r, err)
			fault.WriteProblem(w, err)
			return
		}
		status := http.StatusNoContent
		if cond.IfAbsent {
			status = http.StatusCreated
		}
		answerConditional(w, op, res, status, binding, key)
	})

	mux.HandleFunc("DELETE /kv/{binding}/{key...}", func(w http.ResponseWriter, r *http.Request) {
		const op = "workernode.local.kv.delete"
		binding, key := r.PathValue("binding"), r.PathValue("key")
		version, err := deleteCondition(op, r)
		if err != nil {
			fault.WriteProblem(w, err)
			return
		}
		if version == "" {
			if err := kv.Delete(r.Context(), ns, fn, binding, key); err != nil {
				denied("delete", r, err)
				fault.WriteProblem(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		res, err := kv.DeleteIf(r.Context(), ns, fn, binding, key, version)
		if err != nil {
			denied("delete", r, err)
			fault.WriteProblem(w, err)
			return
		}
		answerConditional(w, op, res, http.StatusNoContent, binding, key)
	})

	mux.HandleFunc("GET /kv/{binding}", func(w http.ResponseWriter, r *http.Request) {
		keys, err := kv.List(r.Context(), ns, fn, r.PathValue("binding"), r.URL.Query().Get("prefix"))
		if err != nil {
			denied("list", r, err)
			fault.WriteProblem(w, err)
			return
		}
		if keys == nil {
			keys = []string{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keys)
	})
}

// answerConditional answers a conditional write: status with the new version's ETag when it was stored, else 412
// with the current version's ETag when the key exists.
func answerConditional(w http.ResponseWriter, op string, res kvstore.Result, status int, binding, key string) {
	if res.Version != "" {
		w.Header().Set("ETag", etag(res.Version))
	}
	if res.Done {
		w.WriteHeader(status)
		return
	}
	if res.Version == "" {
		fault.WriteProblem(w, fault.PreconditionFailedf(op, "key %q does not exist in binding %q", key, binding))
		return
	}
	fault.WriteProblem(w, fault.PreconditionFailedf(op, "key %q of binding %q is at version %s", key, binding, res.Version))
}

func etag(version string) string { return `"` + version + `"` }

// putCondition reads a PUT's If-None-Match: *, If-Match: "<version>" and ?ttl=<seconds>; the zero Write is a plain put.
func putCondition(op string, r *http.Request) (kvstore.Write, error) {
	var w kvstore.Write
	switch none := r.Header.Values("If-None-Match"); {
	case len(none) > 1:
		return w, fault.Invalidf(op, "If-None-Match is sent on %d lines; send one *", len(none))
	case len(none) == 1 && strings.TrimSpace(none[0]) != "*":
		return w, fault.Invalidf(op, "If-None-Match %s: a put takes only *, which creates the key", none[0])
	case len(none) == 1:
		w.IfAbsent = true
	}
	version, err := ifMatch(op, r)
	if err != nil {
		return w, err
	}
	if w.IfAbsent && version != "" {
		return w, fault.Invalidf(op, "send If-None-Match or If-Match, not both")
	}
	w.IfVersion = version
	if raw := r.URL.Query().Get("ttl"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 1 || n > math.MaxInt64/int64(time.Second) {
			return w, fault.Invalidf(op, "ttl %q is not a whole number of seconds from 1", raw)
		}
		w.TTL = time.Duration(n) * time.Second
	}
	return w, nil
}

// deleteCondition reads a DELETE's If-Match: "<version>"; "" is a plain delete.
func deleteCondition(op string, r *http.Request) (string, error) {
	if len(r.Header.Values("If-None-Match")) > 0 {
		return "", fault.Invalidf(op, "a delete takes If-Match, not If-None-Match")
	}
	return ifMatch(op, r)
}

// ifMatch reads one strong entity-tag "<version>", the version a GET or a write answered, under the control plane's
// rules for the tag (ADR-0210); "" when the header is absent. A version is opaque: one this key never had loses.
func ifMatch(op string, r *http.Request) (string, error) {
	lines := r.Header.Values("If-Match")
	switch len(lines) {
	case 0:
		return "", nil
	case 1:
	default:
		return "", fault.Invalidf(op, "If-Match is sent on %d lines; send one entity-tag", len(lines))
	}
	v := strings.TrimSpace(lines[0])
	tag, ok := strings.CutPrefix(v, `"`)
	if ok {
		tag, ok = strings.CutSuffix(tag, `"`)
	}
	if !ok || tag == "" || strings.ContainsFunc(tag, func(c rune) bool { return c < 0x21 || c == '"' || c == 0x7f }) {
		return "", fault.Invalidf(op, "If-Match %s is not one strong entity-tag \"<version>\" of the key", v)
	}
	return tag, nil
}
