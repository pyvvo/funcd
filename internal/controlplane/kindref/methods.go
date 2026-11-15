package kindref

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/kvstore"
	"github.com/pyvvo/funcd/internal/reference"
	"github.com/pyvvo/funcd/internal/workernode/local"
)

// methodEndpoint is the local API endpoint that a ctx method calls, and the causes of that endpoint that the method
// does not surface: those its shim returns as a value and those it never provokes (ADR-0330 Decision 8).
type methodEndpoint struct {
	Endpoint string              // a pattern of local.Endpoints, or "" for a method that calls no endpoint
	Except   []*fault.KnownCause // the endpoint's causes that the method does not surface
}

// methods maps a language ("TypeScript", "Python") and a method ("kv.put", "kv.put_if_absent"; a method of the
// context itself has no client, "log") to its endpoint.
//
//nolint:gochecknoglobals // an immutable lookup table
var methods = methodTable()

// methodTable builds methods from what each shim's method sends: a KV method sends one condition at most, on one line,
// with a well-formed body, and its shim returns a missing key's 404 and a condition's 412 as a value.
func methodTable() map[string]map[string]methodEndpoint {
	const (
		kvKey, kvList     = "GET /kv/{binding}/{key...}", "GET /kv/{binding}"
		kvPut, kvDelete   = "PUT /kv/{binding}/{key...}", "DELETE /kv/{binding}/{key...}"
		blobKey, blobList = "GET /blob/{binding}/{key...}", "GET /blob/{binding}"
		blobPut, blobDel  = "PUT /blob/{binding}/{key...}", "DELETE /blob/{binding}/{key...}"
		sqlQuery, sqlExec = "POST /sql/{binding}/query", "POST /sql/{binding}/execute"
		secretKey, secret = "GET /secret/{key}", "GET /secret"
		secretPut, secDel = "PUT /secret/{binding}/{key}", "DELETE /secret/{binding}/{key}"
	)
	conditions := []*fault.KnownCause{local.ErrKeyAbsent, local.ErrVersionMismatch, local.ErrIfMatchRepeated}
	writes := slices.Concat(conditions, []*fault.KnownCause{local.ErrIfNoneMatchRepeated, local.ErrConditionsBoth,
		local.ErrBodyUnreadable, local.ErrIfNoneMatchNotStar})
	read := methodEndpoint{Endpoint: kvKey, Except: []*fault.KnownCause{local.ErrKeyNotFound}}
	put := methodEndpoint{Endpoint: kvPut, Except: slices.Concat(writes, []*fault.KnownCause{local.ErrIfMatchMalformed})}
	compareAndSet := methodEndpoint{Endpoint: kvPut, Except: writes}
	del := methodEndpoint{Endpoint: kvDelete, Except: slices.Concat(conditions,
		[]*fault.KnownCause{local.ErrDeleteIfNoneMatch, local.ErrIfMatchMalformed, kvstore.ErrNoConditional})}
	delIf := methodEndpoint{Endpoint: kvDelete, Except: slices.Concat(conditions, []*fault.KnownCause{local.ErrDeleteIfNoneMatch})}
	at := func(endpoint string) methodEndpoint { return methodEndpoint{Endpoint: endpoint} }
	return map[string]map[string]methodEndpoint{
		"TypeScript": {
			"log": {}, "invoke": at("POST /invoke/{alias}"), "fetch": at("/fetch/"), "remainingMs": {},
			"kv.get": read, "kv.getEntry": read, "kv.getText": read, "kv.getJSON": read,
			"kv.put": put, "kv.putIfAbsent": put, "kv.compareAndSet": compareAndSet,
			"kv.del": del, "kv.delIf": delIf, "kv.list": at(kvList),
			"blob.get": at(blobKey), "blob.put": at(blobPut), "blob.del": at(blobDel), "blob.list": at(blobList),
			"blob.signedUrl": at(blobKey),
			"sql.query":      at(sqlQuery), "sql.queryValues": at(sqlQuery), "sql.execute": at(sqlExec),
			"config.get": {}, "config.list": {},
			"secret.get": at(secretKey), "secret.list": at(secret), "secret.put": at(secretPut), "secret.del": at(secDel),
			"lake.get": {},
		},
		"Python": {
			"log": {}, "invoke": at("POST /invoke/{alias}"), "remaining_ms": {},
			"kv.get": read, "kv.get_entry": read, "kv.get_str": read, "kv.get_json": read,
			"kv.put": put, "kv.put_if_absent": put, "kv.compare_and_set": compareAndSet,
			"kv.delete": del, "kv.delete_if": delIf, "kv.list": at(kvList),
			"blob.get": at(blobKey), "blob.put": at(blobPut), "blob.delete": at(blobDel), "blob.list": at(blobList),
			"blob.signed_url": at(blobKey),
			"sql.query":       at(sqlQuery), "sql.query_values": at(sqlQuery), "sql.execute": at(sqlExec),
			"config.get": {}, "config.list": {},
			"secret.get": at(secretKey), "secret.list": at(secret), "secret.put": at(secretPut), "secret.delete": at(secDel),
			"lake.get": {},
		},
	}
}

// handlerDocs is what a Handler API section is written from: the shims, the table of their methods and the local
// API's endpoints.
type handlerDocs struct {
	langs     []language
	methods   map[string]map[string]methodEndpoint
	endpoints []local.Endpoint
}

// methodKey is a method's key in methods: "<client>.<method>", or the method alone for the context's own.
func methodKey(client, method string) string {
	if client == "" {
		return method
	}
	return client + "." + method
}

// check fails, naming it, on a method of c that has no entry in the language's table, on an entry that names an
// endpoint the local API lacks or excepts a cause that its endpoint does not declare, and on an entry of c's methods
// that c no longer has.
func (h handlerDocs) check(lang string, c handlerClient) error {
	table := h.methods[lang]
	for _, m := range c.Methods {
		key := methodKey(c.Name, m.Name)
		entry, ok := table[key]
		if !ok {
			return fmt.Errorf("kindref: the %s method %s has no entry in kindref's table of methods", lang, key)
		}
		if entry.Endpoint == "" {
			continue
		}
		i := slices.IndexFunc(h.endpoints, func(e local.Endpoint) bool { return e.Pattern == entry.Endpoint })
		if i < 0 {
			return fmt.Errorf("kindref: the %s method %s calls %q, which is no endpoint of the local API", lang, key,
				entry.Endpoint)
		}
		for _, cause := range entry.Except {
			if !slices.Contains(h.endpoints[i].Causes, cause) {
				return fmt.Errorf("kindref: the %s method %s excepts %s, which %s does not declare", lang, key, cause.Name(),
					entry.Endpoint)
			}
		}
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		client, method, found := strings.Cut(key, ".")
		if !found {
			client, method = "", key
		}
		if client == c.Name && !slices.ContainsFunc(c.Methods, func(m handlerMethod) bool { return m.Name == method }) {
			return fmt.Errorf("kindref: kindref's table maps the %s method %s, which the shim does not have", lang, key)
		}
	}
	return nil
}

// failures writes what follows c's table in lang: once, that a method calling funcd can fail with 500, when one of
// c's methods calls the local API; then one row for each cause that a method surfaces from its described endpoint.
func (h handlerDocs) failures(lang string, c handlerClient) []string {
	var calls bool
	var rows []string
	var done []string
	for _, m := range c.Methods {
		if slices.Contains(done, m.Name) {
			continue
		}
		done = append(done, m.Name)
		entry := h.methods[lang][methodKey(c.Name, m.Name)]
		if entry.Endpoint == "" {
			continue
		}
		calls = true
		i := slices.IndexFunc(h.endpoints, func(e local.Endpoint) bool { return e.Pattern == entry.Endpoint })
		causes := slices.DeleteFunc(slices.Clone(h.endpoints[i].Causes), func(cause *fault.KnownCause) bool {
			return slices.Contains(entry.Except, cause)
		})
		slices.SortStableFunc(causes, func(a, b *fault.KnownCause) int { return cmp.Compare(statusOf(a), statusOf(b)) })
		for _, cause := range causes {
			p := fault.ToProblem(cause.Errorf("", ""))
			rows = append(rows, "| `"+m.Name+"` | "+strconv.Itoa(p.Status)+" | `"+p.Type+"` | "+reference.Cell(cause.When())+
				" | "+reference.Cell(cause.Fix())+" |")
		}
	}
	if !calls {
		return nil
	}
	internal := fault.ToProblem(fault.Internalf("", ""))
	lines := []string{"", reference.Cell(fmt.Sprintf("Each method here that calls funcd can also fail with the status %d "+
		"and the problem type `%s`, when funcd meets an error that it did not expect; funcd logs its cause.",
		internal.Status, internal.Type))}
	if len(rows) == 0 {
		return lines
	}
	lines = append(lines, "", "When funcd refuses a call, the method fails with an error that holds funcd's status and "+
		"problem type. This table gives them for each method, with when each happens and what to do.", "",
		"| Method | Status | Problem type | When | What to do |", "| --- | --- | --- | --- | --- |")
	return append(lines, rows...)
}

func statusOf(c *fault.KnownCause) int { return fault.StatusOf(c.Errorf("", "")) }
