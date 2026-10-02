package secrets

import (
	"log/slog"
	"strings"
	"unicode/utf8"
)

// IsReservedKey reports whether an env key is reserved by the runtime shim contract
// (FUNCD_ARTIFACT/HANDLER/PORT/POOL_MANIFEST/PORTFILE, …): a FUNCD_-prefixed key may never be
// shadowed by resolved Secret/ConfigMap Data (ADR-0057, reserved-env-not-overridable). A prefix
// guard (not an explicit set) keeps every current and future reserved key protected. This is the
// single definition — the internal/function + internal/services/catalog copies are deleted (ADR-0092).
func IsReservedKey(k string) bool { return strings.HasPrefix(k, "FUNCD_") }

// MergeEnvGuarded merges src into dst, dropping (and logging via log, if non-nil) any key that
// IsReservedKey. It is the one guarded-merge reused by the Function reconciler, the CatalogService
// controller, and provider.ResolveEnv (ADR-0092). A nil log is tolerated (the drop is silent).
// It mutates only dst.
func MergeEnvGuarded(dst, src map[string]string, log *slog.Logger) {
	for k, v := range src {
		if IsReservedKey(k) {
			if log != nil {
				log.Warn("dropping resolved env key that collides with a reserved FUNCD_ key", "key", k)
			}
			continue
		}
		dst[k] = v
	}
}

// EnvValueProblem says why v cannot be delivered as a worker env value, or returns "" if it can:
// a value that is not valid UTF-8 would reach the worker as U+FFFD, and exec rejects a NUL byte.
// It is the one value check for resolved Secret and ConfigMap data.
func EnvValueProblem(v string) string {
	switch {
	case !utf8.ValidString(v):
		return "is not valid UTF-8"
	case strings.IndexByte(v, 0) >= 0:
		return "contains a NUL byte"
	}
	return ""
}
