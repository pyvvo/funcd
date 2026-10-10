package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// timerManifest is EventSource team-a/jobs with one timer event whose schedule keys are given as YAML lines.
func timerManifest(schedule ...string) string {
	return "apiVersion: funcd.io/v1alpha1\nkind: EventSource\nmetadata:\n  name: jobs\n  namespace: team-a\n" +
		"  resourceGroup: rg1\nspec:\n  timer:\n    events:\n      - name: report\n        " +
		strings.Join(schedule, "\n        ") + "\n"
}

// sendEventSource sends a raw EventSource body with one timer event, as a client other than funcdctl would: a POST
// to the collection, or a PUT of team-a/jobs.
func sendEventSource(t *testing.T, url, method, event string) (int, string) {
	t.Helper()
	body := `{"apiVersion":"funcd.io/v1alpha1","kind":"EventSource","metadata":{"name":"jobs","namespace":"team-a",` +
		`"resourceGroup":"rg1"},"spec":{"timer":{"events":[` + event + `]}}}`
	target := url + "/apis/funcd.io/v1alpha1/namespaces/team-a/eventsources"
	if method == http.MethodPut {
		target += "/jobs"
	}
	req, err := http.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+devToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func storedEvents(t *testing.T, c *sdk.Client) []v1.TimerEvent {
	t.Helper()
	obj, err := c.Get(context.Background(), v1.KindEventSource, "team-a", "jobs")
	if fault.KindOf(err) == fault.NotFound {
		return nil
	}
	require.NoError(t, err)
	return obj.(*v1.EventSource).Spec.Timer.Events
}

// scenario: cron-invalid-refused (ADR-0211) — funcdctl apply refuses each bad schedule before sending, and a raw
// create or update gets 400 urn:funcd:problem:invalid naming the field path (and the grammar for an expression),
// with nothing stored.
func TestScenarioCronInvalidRefused(t *testing.T) {
	t.Parallel()
	const path = "spec.timer.events[0]"
	for _, tc := range []struct {
		name  string
		yaml  []string
		json  string
		wants []string
	}{
		{"4 fields", []string{`cron: "0 2 * *"`}, `"cron":"0 2 * *"`, []string{path + ".cron", cron.Grammar}},
		{"seconds", []string{`cron: "0 0 2 * * *"`}, `"cron":"0 0 2 * * *"`, []string{path + ".cron", cron.Grammar}},
		{"@every", []string{`cron: "@every 5m"`}, `"cron":"@every 5m"`, []string{path + ".cron", cron.Grammar}},
		{"unknown macro", []string{`cron: "@5minutes"`}, `"cron":"@5minutes"`, []string{path + ".cron", cron.Grammar}},
		{"weekday name", []string{`cron: "0 2 * * MON"`}, `"cron":"0 2 * * MON"`, []string{path + ".cron", cron.Grammar}},
		{"n/s", []string{`cron: "5/10 * * * *"`}, `"cron":"5/10 * * * *"`, []string{path + ".cron", cron.Grammar}},
		{"minute 60", []string{`cron: "60 * * * *"`}, `"cron":"60 * * * *"`, []string{path + ".cron", cron.Grammar}},
		{"never fires", []string{`cron: "0 0 30 2 *"`}, `"cron":"0 0 30 2 *"`, []string{path + ".cron", cron.Grammar}},
		{"unknown zone", []string{`cron: "0 2 * * *"`, "timeZone: Mars/Base"}, `"cron":"0 2 * * *","timeZone":"Mars/Base"`,
			[]string{path + `.timeZone "Mars/Base"`}},
		{"Local zone", []string{`cron: "0 2 * * *"`, "timeZone: Local"}, `"cron":"0 2 * * *","timeZone":"Local"`,
			[]string{path + `.timeZone "Local"`}},
		{"interval and cron", []string{"interval: 1h", `cron: "0 2 * * *"`}, `"interval":"1h","cron":"0 2 * * *"`,
			[]string{path + ` ("report") must set exactly one schedule (interval or cron), got 2`}},
		{"neither", []string{"timeZone: UTC"}, `"timeZone":"UTC"`,
			[]string{path + ` ("report") must set exactly one schedule (interval or cron), got 0`}},
		{"timeZone with interval", []string{"interval: 1h", "timeZone: Europe/Paris"}, `"interval":"1h","timeZone":"Europe/Paris"`,
			[]string{path + ".timeZone is set without cron"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, url := newClientURL(t)
			var out bytes.Buffer
			err := execCLI(&out, nil, "apply", "-f", writeManifest(t, timerManifest(tc.yaml...)))
			require.Equal(t, fault.Invalid, fault.KindOf(err), "%v", err)
			for _, w := range tc.wants {
				require.ErrorContains(t, err, w)
			}

			event := `{"name":"report",` + tc.json + `}`
			code, body := sendEventSource(t, url, http.MethodPost, event)
			require.Equal(t, http.StatusBadRequest, code, body)
			require.Contains(t, body, "urn:funcd:problem:invalid")
			for _, w := range tc.wants {
				require.Contains(t, body, strings.ReplaceAll(w, `"`, `\"`))
			}
			require.Nil(t, storedEvents(t, c))

			code, body = sendEventSource(t, url, http.MethodPost, `{"name":"report","cron":"0 2 * * *"}`)
			require.Equal(t, http.StatusOK, code, body)
			code, body = sendEventSource(t, url, http.MethodPut, event)
			require.Equal(t, http.StatusBadRequest, code, body)
			require.Contains(t, body, "urn:funcd:problem:invalid")
			require.Equal(t, []v1.TimerEvent{{Name: "report", Cron: "0 2 * * *"}}, storedEvents(t, c))
		})
	}
}

// A cron event applies with funcdctl and reads back as written.
func TestCronEventApplies(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "apply", "-f", writeManifest(t, timerManifest(`cron: "0 2 * * *"`, "timeZone: Europe/Paris"))))
	require.Equal(t, []v1.TimerEvent{{Name: "report", Cron: "0 2 * * *", TimeZone: "Europe/Paris"}}, storedEvents(t, c))
}
