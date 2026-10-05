//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/funclog/logread"
	"github.com/pyvvo/funcd/pkg/funcd"
	"github.com/pyvvo/funcd/pkg/sdk"
)

// ADR-0168: a worker's raw stdout and stderr reach its logs at INFO and ERROR, and one log call is one bounded record.

// rawLine waits for fn's line with body and returns it.
func (h *shimRig) rawLine(t *testing.T, fn, body string) logread.Line {
	t.Helper()
	var got logread.Line
	require.Eventually(t, func() bool {
		lines, err := h.c.Logs(context.Background(), "default", v1.ObjectName(fn), sdk.LogsOptions{})
		if err != nil {
			return false
		}
		for _, l := range lines {
			if l.Body == body {
				got = l
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond, "%s logs %q", fn, body)
	return got
}

func (h *shimRig) countBody(t *testing.T, fn, body string) int {
	t.Helper()
	lines, err := h.c.Logs(context.Background(), "default", v1.ObjectName(fn), sdk.LogsOptions{})
	require.NoError(t, err)
	n := 0
	for _, l := range lines {
		if l.Body == body {
			n++
		}
	}
	return n
}

func requireRaw(t *testing.T, l logread.Line, severity string, source funclog.Source) {
	t.Helper()
	require.Equal(t, severity, l.Severity, "%q", l.Body)
	require.Equal(t, string(source), l.Source, "%q", l.Body)
	require.False(t, l.Time.IsZero(), "a raw line is stamped when read")
}

// scenario: raw-output-severity — stdout lines are INFO (source=stdout), stderr lines ERROR (source=stderr), while the
// worker runs; Node's console.log record is stored once; Python's ctx.log and print reach the logs.
func TestScenarioRawOutputSeverity(t *testing.T) {
	t.Run("nodejs22", func(t *testing.T) {
		h := newShimRig(t, "")
		h.deploy(t, "raw-node", nodeFn(`export function handle() {
  console.log('raw-a');
  process.stdout.write('raw-b\n');
  process.stderr.write('raw-c\n');
  return { ok: true };
}
`))
		waitReady(t, h.c, "raw-node")
		require.Equal(t, http.StatusOK, h.post("raw-node", `{}`).status)
		requireRaw(t, h.rawLine(t, "raw-node", "raw-b"), "INFO", funclog.SourceStdout)
		requireRaw(t, h.rawLine(t, "raw-node", "raw-c"), "ERROR", funclog.SourceStderr)
		a := h.rawLine(t, "raw-node", "raw-a")
		require.Equal(t, "INFO", a.Severity)
		require.Equal(t, 1, h.countBody(t, "raw-node", "raw-a"), "console.log is stored once, from Path B")
	})
	t.Run("python314", func(t *testing.T) {
		h := newShimRig(t, requirePython(t, false))
		h.deploy(t, "raw-py", pythonFn(`import sys


def handle(ctx, event):
    ctx.log("raw-b")
    print("raw-d")
    print("raw-c", file=sys.stderr)
    return {"ok": True}
`))
		waitReady(t, h.c, "raw-py")
		require.Equal(t, http.StatusOK, h.post("raw-py", `{}`).status)
		requireRaw(t, h.rawLine(t, "raw-py", "raw-b"), "INFO", funclog.SourceStdout)
		requireRaw(t, h.rawLine(t, "raw-py", "raw-d"), "INFO", funclog.SourceStdout)
		requireRaw(t, h.rawLine(t, "raw-py", "raw-c"), "ERROR", funclog.SourceStderr)
	})
}

// scenario: load-error-in-logs — a module that writes booting to stdout and then throws at load has ShapeValid False
// with the shim's error line, not booting, and that line is in its logs at ERROR.
func TestScenarioLoadErrorInLogs(t *testing.T) {
	h := newShimRig(t, "")
	h.deploy(t, "loadfail", nodeFn(`process.stdout.write('booting\n');
throw new Error('load-boom');
`))
	var msg string
	require.Eventually(t, func() bool {
		c, ok := h.function(t, "loadfail").Status.Conditions.Get("ShapeValid")
		msg = c.Message
		return ok && c.Status == v1.ConditionFalse && msg != ""
	}, 30*time.Second, 100*time.Millisecond, "the Function reports its load error")
	require.Contains(t, msg, "load-boom", "ShapeValid carries the shim's error line")
	require.NotContains(t, msg, "booting")
	require.Eventually(t, func() bool {
		lines, err := h.c.Logs(context.Background(), "default", "loadfail", sdk.LogsOptions{})
		if err != nil {
			return false
		}
		for _, l := range lines {
			if strings.Contains(l.Body, "load-boom") && l.Severity == "ERROR" && l.Source == string(funclog.SourceStderr) {
				return true
			}
		}
		return false
	}, 30*time.Second, 200*time.Millisecond, "the load error is in the logs at ERROR")
}

// scenario: raw-output-survives-restart — a worker prints run-1 and exits 1; its restart prints run-2; the logs hold
// both.
func TestScenarioRawOutputSurvivesRestart(t *testing.T) {
	h := newShimRig(t, "")
	marker := filepath.Join(t.TempDir(), "ran")
	h.deploy(t, "restarts", nodeFn(fmt.Sprintf(`import { existsSync, writeFileSync } from 'node:fs';
const run = existsSync(%[1]q) ? 2 : 1;
writeFileSync(%[1]q, 'ran');
process.stdout.write('run-' + run + '\n');
export function handle() {
  if (run === 1) setTimeout(() => process.exit(1), 20);
  return { run };
}
`, marker)))
	waitReady(t, h.c, "restarts")
	require.Equal(t, http.StatusOK, h.post("restarts", `{}`).status)
	requireRaw(t, h.rawLine(t, "restarts", "run-1"), "INFO", funclog.SourceStdout)
	requireRaw(t, h.rawLine(t, "restarts", "run-2"), "INFO", funclog.SourceStdout)
}

// cutRecord waits for fn's record with body and requires it cut at bound.
func (h *shimRig) cutRecord(t *testing.T, fn, body, severity string, bound int) map[string]string {
	t.Helper()
	attrs := h.logLine(t, fn, func(l logread.Line) bool { return l.Body == body && l.Severity == severity })
	require.Equal(t, "true", attrs["truncated"], "the record is marked cut: %d attrs", len(attrs))
	kept, err := strconv.Atoi(attrs["keptBytes"])
	require.NoError(t, err)
	require.Positive(t, kept)
	require.LessOrEqual(t, kept, bound)
	encoded, err := json.Marshal(attrs)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), bound, "the stored attrs fit the bound")
	return attrs
}

// scenario: record-cut-at-bound — at the default bound, a 3.5 MB value gives one record marked cut, with keptBytes at
// most 65536 and sev and body intact.
func TestScenarioRecordCutAtBound(t *testing.T) {
	t.Run("nodejs22", func(t *testing.T) {
		h := newShimRig(t, "")
		h.deploy(t, "cut-node", nodeFn(`export function handle() {
  let obj = { leaf: 'x'.repeat(40) };
  for (let i = 0; i < 16; i++) obj = { a: obj, b: obj };
  console.log('big', obj);
  return { ok: true };
}
`))
		waitReady(t, h.c, "cut-node")
		require.Equal(t, http.StatusOK, h.post("cut-node", `{}`).status)
		h.cutRecord(t, "cut-node", "big", "INFO", funclog.DefaultMaxRecordBytes)
		require.Equal(t, 1, h.countBody(t, "cut-node", "big"), "one log call is one record")
	})
	t.Run("python314", func(t *testing.T) {
		h := newShimRig(t, requirePython(t, false))
		h.deploy(t, "cut-py", pythonFn(`import logging


def handle(ctx, event):
    obj = {"leaf": "x" * 40}
    for _ in range(16):
        obj = {"a": obj, "b": obj}
    logging.error("big", extra={"obj": obj})
    return {"ok": True}
`))
		waitReady(t, h.c, "cut-py")
		require.Equal(t, http.StatusOK, h.post("cut-py", `{}`).status)
		h.cutRecord(t, "cut-py", "big", "ERROR", funclog.DefaultMaxRecordBytes)
	})
}

// scenario: record-bound-reaches-shim — with funclog.maxRecordBytes 8192 the worker's env carries it and a 100 KB
// value is cut at 8192.
func TestScenarioRecordBoundReachesShim(t *testing.T) {
	h := newShimRig(t, "", funcd.WithFunclogMaxRecordBytes(8192))
	h.deploy(t, "bound", nodeFn(`export function handle() {
  console.log('payload', { blob: 'y'.repeat(100000) });
  return { bound: process.env.FUNCD_FUNCLOG_MAX_RECORD_BYTES ?? '' };
}
`))
	waitReady(t, h.c, "bound")
	r := h.post("bound", `{}`)
	require.Equal(t, http.StatusOK, r.status, r.body)
	require.JSONEq(t, `{"bound":"8192"}`, r.body)
	attrs := h.cutRecord(t, "bound", "payload", "INFO", 8192)
	require.Greater(t, len(attrs["blob"]), 7000, "the cut keeps what fits")
}

// scenario: pooled-raw-output — a raw line of a pool worker is stored under each member of the pool: a's stderr line
// is ERROR in a's and b's logs, p's print is INFO in p's and q's logs.
func TestScenarioPooledRawOutput(t *testing.T) {
	forPoolLangs(t, func(t *testing.T, l poolLang) {
		h := newShimRig(t, l.python)
		writer := "export function handle() { process.stderr.write('pooled-x\\n'); return { ok: true }; }\n"
		severity, source := "ERROR", funclog.SourceStderr
		if l.python != "" {
			writer = "def handle(ctx, event):\n    print(\"pooled-x\")\n    return {\"ok\": True}\n"
			severity, source = "INFO", funclog.SourceStdout
		}
		h.deploy(t, "a", l.fn(writer).pooled("raw"))
		h.deploy(t, "b", l.fn(l.quiet).pooled("raw"))
		waitReady(t, h.c, "a", "b")
		h.samePool(t, "a", "b")
		require.True(t, h.call(t, "a").OK)
		for _, member := range []string{"a", "b"} {
			requireRaw(t, h.rawLine(t, member, "pooled-x"), severity, source)
		}
	})
}
