package funcd_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/pkg/funcd"
)

// ADR-0342: the logs reads of the objects that run an engine.

const nsPath = "/apis/funcd.io/v1alpha1/namespaces/default/"

// TestEngineLogsAnswersMatrix provokes every case the three engine logs reads list.
func TestEngineLogsAnswersMatrix(t *testing.T) {
	t.Parallel()
	e := newAnswerEnv(t, funcd.WithAddonRegistries("ghcr.io"))
	registerAddons(t, e, addonFixture("rauthy", "IdentityProvider"))
	var rows []answerRow
	for _, op := range []struct{ id, coll, name string }{
		{"getLakeLogs", nsPath + "lakes", "sales"},
		{"getSQLDatabaseLogs", nsPath + "sqldatabases", "orders"},
		{"getAddonObjectLogs", nsPath + "identityproviders", "login"},
	} {
		logs := op.coll + "/" + op.name + "/logs"
		rows = append(rows,
			answerRow{op: op.id, on: e, ask: ask{what: "the logs", method: http.MethodGet, path: logs, token: answersDev,
				detail: `"items"`}},
			answerRow{op: op.id, when: "carries no bearer token", on: e, ask: ask{what: "no credential",
				method: http.MethodGet, path: logs, detail: "missing bearer token"}},
			answerRow{op: op.id, when: "do not allow it", on: e, ask: ask{what: "another namespace's developer",
				method: http.MethodGet, path: logs, token: answersOther, detail: `read logs in "default" denied`}},
			answerRow{op: op.id, when: "The since parameter", on: e, ask: ask{what: "a since of neither form",
				method: http.MethodGet, path: logs + "?since=yesterday", token: answersDev, detail: `since "yesterday" is not`}},
			answerRow{op: op.id, when: "The severity parameter", on: e, ask: ask{what: "an unknown severity",
				method: http.MethodGet, path: logs + "?severity=loud", token: answersDev, detail: `unknown severity "loud"`}},
			answerRow{op: op.id, when: "in the path is not", on: e, ask: ask{what: "a name that is no DNS-1123 label",
				method: http.MethodGet, path: op.coll + "/Not_A_Label/logs", token: answersDev,
				detail: "path.name"}},
			answerRow{op: op.id, when: "The limit parameter", on: e, ask: ask{what: "a limit that is no integer",
				method: http.MethodGet, path: logs + "?limit=many", token: answersDev, detail: "query.limit"}},
		)
	}
	rows = append(rows, answerRow{op: "getAddonObjectLogs", when: "No Addon registers a kind of the plural", on: e,
		ask: ask{what: "a plural no Addon registers", method: http.MethodGet, path: nsPath + "gadgets/x/logs",
			token: answersDev, detail: "no Addon registers"}})
	runMatrix(t, rows)
}

// scenario: engine-logs-scope — the read rule of a Function's logs: a developer or a viewer of another namespace is
// refused with 403, while a viewer of the object's namespace and an admin read an engine's lines.
func TestScenarioEngineLogsScope(t *testing.T) {
	t.Parallel()
	p, err := funcd.New(funcd.InMemory(), funcd.WithAddonRegistries("ghcr.io"), funcd.WithCredentials(
		funcd.Credential{Token: answersAdmin, Role: "admin"},
		funcd.Credential{Token: "scope-viewer", Role: "viewer", Namespaces: []string{"default"}},
		funcd.Credential{Token: "scope-team-developer", Role: "developer", Namespaces: []string{"team-a"}},
		funcd.Credential{Token: "scope-team-viewer", Role: "viewer", Namespaces: []string{"team-a"}},
	))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	e := &answerEnv{c: answersClient(), api: "http://" + p.Addr()}
	t.Cleanup(e.c.CloseIdleConnections)
	registerAddons(t, e, addonFixture("rauthy", "IdentityProvider"))

	logs := nsPath + "identityproviders/login/logs"
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"scope-team-developer", http.StatusForbidden},
		{"scope-team-viewer", http.StatusForbidden},
		{"scope-viewer", http.StatusOK},
		{answersAdmin, http.StatusOK},
	} {
		ask{what: tc.token, method: http.MethodGet, path: logs, token: tc.token, status: tc.status}.run(t, e.c, e.api)
	}
}
