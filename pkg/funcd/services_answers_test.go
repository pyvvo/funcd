//go:build !e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/platform/quarantine"
	"github.com/pyvvo/funcd/internal/provider"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// The answers of the Bucket, SQL, Lake, Secrets and Configuration features' operations (ADR-0329), one row per case of
// each, through the platform's own servers.

// fixtureMeta is the type and the metadata of object name of kind k, in default.
func fixtureMeta(k v1.Kind, name v1.ObjectName) (v1.TypeMeta, v1.ObjectMeta) {
	return v1.TypeMeta{APIVersion: k.GVK().APIVersion(), Kind: k},
		v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"}
}

// bucketFixture is Bucket name of default, with prefixes that have no owner.
func bucketFixture(name v1.ObjectName, prefixes ...string) *v1.Bucket {
	b := &v1.Bucket{}
	b.TypeMeta, b.ObjectMeta = fixtureMeta(v1.KindBucket, name)
	for _, p := range prefixes {
		b.Spec.Prefixes = append(b.Spec.Prefixes, v1.BucketPrefix{Name: p})
	}
	return b
}

func sqlFixture(name v1.ObjectName) *v1.SQLDatabase {
	d := &v1.SQLDatabase{}
	d.TypeMeta, d.ObjectMeta = fixtureMeta(v1.KindSQLDatabase, name)
	return d
}

// lakeFixture is Lake name of default, located on the prefix gold of the Bucket lakehouse, which it declares by ref.
func lakeFixture(name v1.ObjectName) *v1.Lake {
	l := &v1.Lake{}
	l.TypeMeta, l.ObjectMeta = fixtureMeta(v1.KindLake, name)
	l.Spec.Buckets = []v1.BucketEntry{{Ref: "lakehouse"}}
	l.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "gold", Bucket: "lakehouse", Prefix: "gold"}}
	l.Spec.Location = v1.LakeLocation{Bucket: "lakehouse", Prefix: "gold"}
	return l
}

func secretFixture(name v1.ObjectName, data map[string][]byte) *v1.Secret {
	s := &v1.Secret{Spec: v1.SecretSpec{Data: data}}
	s.TypeMeta, s.ObjectMeta = fixtureMeta(v1.KindSecret, name)
	return s
}

func configMapFixture(name v1.ObjectName, data map[string]string) *v1.ConfigMap {
	c := &v1.ConfigMap{Spec: v1.ConfigMapSpec{Data: data}}
	c.TypeMeta, c.ObjectMeta = fixtureMeta(v1.KindConfigMap, name)
	return c
}

// phaseAt is the phase of the object at path, read with the admin's token, "" when it cannot be read.
func phaseAt(c *http.Client, base, path string) v1.Phase {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+answersAdmin)
	resp, err := c.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var obj struct {
		Status struct {
			Phase v1.Phase `json:"phase"`
		} `json:"status"`
	}
	if json.NewDecoder(resp.Body).Decode(&obj) != nil {
		return ""
	}
	return obj.Status.Phase
}

// The answers of the Bucket feature's operations, one row per case of each.
func TestBucketAnswersMatrix(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mem, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	e := newAnswerEnv(t, funcd.WithBlob(mem))
	for _, b := range []*v1.Bucket{bucketFixture("b", "raw"), bucketFixture("bound", "p", "q"),
		bucketFixture("full", "p", "q"), bucketFixture("cataloged", "p", "q")} {
		create(t, e.c, e.api, b)
	}
	binder := function("binder")
	binder.Spec.Buckets = []v1.BucketEntry{{Ref: "bound"}}
	binder.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "b", Bucket: "bound", Prefix: "p"}}
	maker := function("maker")
	maker.Spec.Buckets = []v1.BucketEntry{{Name: "owned", BucketSpec: v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "p"}}}}}
	create(t, e.c, e.api, binder)
	create(t, e.c, e.api, maker)
	const buckets = "/apis/funcd.io/v1alpha1/namespaces/default/buckets"
	require.Eventually(t, func() bool {
		return statusOf(e.c, http.MethodGet, e.api+buckets+"/owned", answersDev) == http.StatusOK
	}, 10*time.Second, 20*time.Millisecond, "the Function maker makes the Bucket owned")
	// The keys the S3 gateway and a Lake's engine write a prefix's data under (ADR-0080, ADR-0293).
	require.NoError(t, mem.Put(ctx, "s3/default/full/p/object", []byte("o"), blob.PutOptions{}))
	require.NoError(t, mem.Put(ctx, "lake-catalogs/default/cataloged/p/_ducklake/catalog.db", []byte("c"), blob.PutOptions{}))
	capped := newAnswerEnv(t, funcd.WithBucketQuotaForTest(1))
	create(t, capped.c, capped.api, bucketFixture("first", "p"))

	// dropP is Bucket name without its prefix p.
	dropP := func(name v1.ObjectName) string { return jsonOf(t, bucketFixture(name, "q")) }
	const catalog = "still holds its Lake catalog funcd.lake-catalogs/cataloged/p/_ducklake/catalog.db"
	rows := slices.Concat(
		kindFixture{kind: "Bucket", kinds: "Buckets", path: "buckets", name: "b", json: jsonOf(t, bucketFixture("b", "raw")),
			broken: jsonOf(t, bucketFixture("b", "raw", "raw")), detail: `spec.prefixes name "raw" is duplicated`}.sharedRows(e),
		[]answerRow{
			{op: "createBucket", when: "already holds 100 Buckets", on: capped, ask: ask{what: "a Bucket past the cap",
				method: http.MethodPost, path: buckets, token: answersDev, body: jsonOf(t, bucketFixture("second", "p")),
				detail: "already holds the maximum 1 Buckets"}},

			{op: "replaceBucket", when: "that made the Bucket", on: e, ask: ask{what: "a Bucket that a live Function made",
				method: http.MethodPut, path: buckets + "/owned", token: answersDev, body: jsonOf(t, bucketFixture("owned", "p")),
				detail: "is managed by Function/maker"}},
			{op: "replaceBucket", when: "spec.bindings.blob still names", on: e, ask: ask{
				what: "a dropped prefix that a binding names", method: http.MethodPut, path: buckets + "/bound",
				token: answersDev, body: dropP("bound"), detail: `prefix "p" of Bucket "bound" is still bound by function "binder"`}},
			{op: "replaceBucket", when: "still holds objects", on: e, ask: ask{what: "a dropped prefix that holds objects",
				method: http.MethodPut, path: buckets + "/full", token: answersDev, body: dropP("full"),
				detail: `prefix "p" of Bucket "full" still holds objects`}},
			{op: "replaceBucket", when: "still holds a Lake's catalog", on: e, ask: ask{
				what: "a dropped prefix that holds a Lake's catalog", method: http.MethodPut, path: buckets + "/cataloged",
				token: answersDev, body: dropP("cataloged"), detail: catalog}},

			{op: "deleteBucket", when: "spec.bindings.blob still names", on: e, ask: ask{what: "a Bucket that a binding names",
				method: http.MethodDelete, path: buckets + "/bound", token: answersDev,
				detail: `Bucket "bound" is bound by function "binder"`}},
			{op: "deleteBucket", when: "still holds objects", on: e, ask: ask{what: "a Bucket whose prefix holds objects",
				method: http.MethodDelete, path: buckets + "/full", token: answersDev,
				detail: `Bucket "full" prefix "p" still holds objects`}},
			{op: "deleteBucket", when: "still holds a Lake's catalog", on: e, ask: ask{
				what: "a Bucket whose prefix holds a Lake's catalog", method: http.MethodDelete, path: buckets + "/cataloged",
				token: answersDev, detail: catalog}},
		},
	)
	runMatrix(t, rows)
}

// sqlFixtures is a SQL engine runtime for the SQL feature's answers: the engine of each database that ready names is
// Ready at its address, and every other engine stays Pending. List names the databases that withData holds, as data
// on disk.
type sqlFixtures struct {
	ready    map[v1.ObjectName]string
	mu       sync.Mutex
	withData []provider.ProviderRef
}

func (s *sqlFixtures) Converge(_ context.Context, spec provider.ProviderSpec) (provider.ProviderStatus, error) {
	if addr, ok := s.ready[spec.Ref.Name]; ok {
		return provider.ProviderStatus{Running: 1, Ready: true, Address: addr}, nil
	}
	return provider.ProviderStatus{Reason: "Starting"}, nil
}

func (*sqlFixtures) Teardown(context.Context, provider.ProviderRef) error { return nil }
func (*sqlFixtures) Reclaim(context.Context, provider.ProviderRef) error  { return nil }

func (s *sqlFixtures) List(context.Context) ([]provider.ProviderRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.withData), nil
}

func (*sqlFixtures) Quarantine() quarantine.Store {
	return provider.NewDirQuarantine("", v1.KindSQLDatabase, nil, nil)
}

// keepData gives database name of default data on disk. It runs after the platform's start, whose reclaim would
// quarantine the data of a database that does not exist.
func (s *sqlFixtures) keepData(name v1.ObjectName) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.withData = append(s.withData, provider.ProviderRef{Namespace: "default", Name: name})
}

// engineAt starts an engine that answers each request with h, and returns its address.
func engineAt(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// The answers of the SQL feature's operations, one row per case of each.
func TestSQLAnswersMatrix(t *testing.T) {
	t.Parallel()
	// The Ready engines: tables's counts one table, silent's closes each connection with no answer, and failing's
	// answers 503.
	engines := &sqlFixtures{ready: map[v1.ObjectName]string{
		"tables": engineAt(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"results":[{"columns":["count(*)"],"types":["integer"],"values":[[1]]}]}`)
		}),
		"silent": engineAt(t, func(w http.ResponseWriter, _ *http.Request) {
			if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
				_ = conn.Close()
			}
		}),
		"failing": engineAt(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }),
	}}
	e := newAnswerEnv(t, funcd.WithSQLProviderRuntime(engines))
	for _, d := range []*v1.SQLDatabase{sqlFixture("db"), sqlFixture("bound"), sqlFixture("tables"), sqlFixture("silent"),
		sqlFixture("failing"), sqlFixture("pending")} {
		create(t, e.c, e.api, d)
	}
	engines.keepData("pending")
	binder := function("binder")
	binder.Spec.SQLDatabases = []v1.SQLDatabaseEntry{{Ref: "bound"}}
	binder.Spec.Bindings.SQL = []v1.FunctionSQL{{Alias: "d", Database: "bound"}}
	maker := function("maker")
	maker.Spec.SQLDatabases = []v1.SQLDatabaseEntry{{Name: "owned"}}
	create(t, e.c, e.api, binder)
	create(t, e.c, e.api, maker)
	const dbs = "/apis/funcd.io/v1alpha1/namespaces/default/sqldatabases"
	require.Eventually(t, func() bool {
		return statusOf(e.c, http.MethodGet, e.api+dbs+"/owned", answersDev) == http.StatusOK
	}, 10*time.Second, 20*time.Millisecond, "the Function maker makes the SQLDatabase owned")
	for _, name := range []string{"tables", "silent", "failing"} {
		require.Eventually(t, func() bool { return phaseAt(e.c, e.api, dbs+"/"+name) == v1.PhaseReady },
			10*time.Second, 20*time.Millisecond, "the engine of %s is Ready", name)
	}
	capped := newAnswerEnv(t, funcd.WithSQLDatabaseQuota(1), funcd.WithSQLProviderRuntime(&sqlFixtures{}))
	create(t, capped.c, capped.api, sqlFixture("first"))

	broken := sqlFixture("db")
	broken.Spec.Quota = v1.SizeQuota{MaxSize: "1Gi", MaxBytes: 1}
	rows := slices.Concat(
		kindFixture{kind: "SQLDatabase", kinds: "SQLDatabases", path: "sqldatabases", name: "db",
			json: jsonOf(t, sqlFixture("db")), broken: jsonOf(t, broken), detail: "set maxSize or maxBytes, not both"}.sharedRows(e),
		[]answerRow{
			{op: "createSQLDatabase", when: "sql.maxDatabasesPerNamespace", on: capped, ask: ask{
				what: "a database past the cap", method: http.MethodPost, path: dbs, token: answersDev,
				body: jsonOf(t, sqlFixture("second")), detail: "already holds the maximum 1 SQLDatabases"}},

			{op: "replaceSQLDatabase", when: "that made the SQLDatabase", on: e, ask: ask{
				what: "a database that a live Function made", method: http.MethodPut, path: dbs + "/owned",
				token: answersDev, body: jsonOf(t, sqlFixture("owned")), detail: "is managed by Function/maker"}},

			{op: "deleteSQLDatabase", when: "spec.bindings.sql still names", on: e, ask: ask{
				what: "a database that a binding names", method: http.MethodDelete, path: dbs + "/bound", token: answersDev,
				detail: `SQLDatabase "bound" is bound by function "binder"`}},
			{op: "deleteSQLDatabase", when: "still holds a table", on: e, ask: ask{what: "a database that holds a table",
				method: http.MethodDelete, path: dbs + "/tables", token: answersDev,
				detail: `SQLDatabase "tables" still holds tables`}},
			{op: "deleteSQLDatabase", when: "is not Ready and the database has data", on: e, ask: ask{
				what: "a database with data whose engine is not Ready", method: http.MethodDelete, path: dbs + "/pending",
				token: answersDev, detail: "the engine of default/pending is not Ready, so its data cannot be checked"}},
			{op: "deleteSQLDatabase", when: "does not answer", on: e, ask: ask{what: "a database whose engine does not answer",
				method: http.MethodDelete, path: dbs + "/silent", token: answersDev,
				detail: "the database's engine does not answer"}},
			{op: "deleteSQLDatabase", when: "answers with an error", on: e, ask: ask{
				what: "a database whose engine answers with an error", method: http.MethodDelete, path: dbs + "/failing",
				token: answersDev, detail: "the engine answered 503"}},
		},
	)
	runMatrix(t, rows)
}

// The answers of the Lake feature's operations, one row per case of each.
func TestLakeAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t)
	create(t, e.c, e.api, lakeFixture("lake"))

	const lakes = "/apis/funcd.io/v1alpha1/namespaces/default/lakes"
	broken := lakeFixture("lake")
	broken.Spec.Location.Prefix = "silver"
	undeclared := func(name v1.ObjectName) string {
		l := lakeFixture(name)
		l.Spec.Buckets = nil
		return jsonOf(t, l)
	}
	const notDeclared = `spec.bindings.blob[0]: Bucket "lakehouse" is not declared in spec.buckets`
	rows := slices.Concat(
		kindFixture{kind: "Lake", kinds: "Lakes", path: "lakes", name: "lake", json: jsonOf(t, lakeFixture("lake")),
			broken: jsonOf(t, broken), detail: "must be one of spec.bindings.blob"}.sharedRows(e),
		[]answerRow{
			{op: "createLake", when: "missing from spec.buckets", on: e, ask: ask{what: "a binding to an undeclared Bucket",
				method: http.MethodPost, path: lakes, token: answersDev, body: undeclared("undeclared"), detail: notDeclared}},
			{op: "replaceLake", when: "missing from spec.buckets", on: e, ask: ask{what: "a binding to an undeclared Bucket",
				method: http.MethodPut, path: lakes + "/lake", token: answersDev, body: undeclared("lake"), detail: notDeclared}},
		},
	)
	runMatrix(t, rows)
}

// The answers of the Secrets feature's operations, one row per case of each: a Secret's operations have only their
// verbs' cases.
func TestSecretsAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t)
	create(t, e.c, e.api, secretFixture("s", map[string][]byte{"TOKEN": []byte("v1")}))

	runMatrix(t, kindFixture{kind: "Secret", kinds: "Secrets", path: "secrets", name: "s",
		json:   jsonOf(t, secretFixture("s", map[string][]byte{"TOKEN": []byte("v1")})),
		broken: jsonOf(t, secretFixture("s", map[string][]byte{"not-env": []byte("v1")})),
		detail: `spec.data key "not-env" is not an env-var name`}.sharedRows(e))
}

// The answers of the Configuration feature's operations, one row per case of each.
func TestConfigurationAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t)
	create(t, e.c, e.api, configMapFixture("cfg", map[string]string{"GREETING": "hello"}))

	// The KVStore marker migration's completion record, which funcd writes at its start (ADR-0180).
	const record = "/apis/funcd.io/v1alpha1/namespaces/funcd-system/configmaps"
	reserved := configMapFixture("kvstore-marker-migration", map[string]string{"COMPLETED_AT": "now"})
	reserved.Namespace = "funcd-system"
	rows := slices.Concat(
		kindFixture{kind: "ConfigMap", kinds: "ConfigMaps", path: "configmaps", name: "cfg",
			json:   jsonOf(t, configMapFixture("cfg", map[string]string{"GREETING": "hello"})),
			broken: jsonOf(t, configMapFixture("cfg", map[string]string{"not-env": "hello"})),
			detail: `spec.data key "not-env" is not an env-var name`}.sharedRows(e),
		[]answerRow{
			{op: "createConfigMap", when: "keeps for itself", on: e, ask: ask{what: "the record funcd keeps",
				method: http.MethodPost, path: record, token: answersAdmin, body: jsonOf(t, reserved),
				detail: "is reserved for the platform"}},
			{op: "replaceConfigMap", when: "keeps for itself", on: e, ask: ask{what: "the record funcd keeps",
				method: http.MethodPut, path: record + "/kvstore-marker-migration", token: answersAdmin,
				body: jsonOf(t, reserved), detail: "is reserved for the platform"}},
			{op: "deleteConfigMap", when: "keeps for itself", on: e, ask: ask{what: "the record funcd keeps",
				method: http.MethodDelete, path: record + "/kvstore-marker-migration", token: answersAdmin,
				detail: "is reserved for the platform"}},
		},
	)
	runMatrix(t, rows)
}
