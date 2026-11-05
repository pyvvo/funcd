## Verdict: pass — 0 blockers, 0 majors, findings folded  (pyvvo/funcd-python fix, model: claude-opus-5-5)

This reviews a pyvvo/funcd-python change: PR pyvvo/funcd-python#114, four commits that squash into one. It has no issue, so
the `TestIssue<N>` name and the `Fixes #N` line do not apply. Three independent rounds reviewed it, each by a fresh agent that
ran it, and the producing session folded each round's findings. This report is condensed from them.

The behavior it claims: a request's body is read before it is answered, in the solo shim and in the pool host, so the next
request on a kept-alive connection starts where this one ends. `read_body` frames a chunked body as well as a `Content-Length`
one, as Node's HTTP parser does, and a chunk or trailer line that Node's parser refuses answers 400 and closes.

| Round | Reviewed | Verdict | Findings, all model-attributed and folded |
|---|---|---|---|
| 1 | a GET reads its request body | pass | 3 Minors: an unpinned `return` in both `do_GET`, no `/` row, a helper's name |
| 2 | chunked bodies | changes requested | 1 Major: chunk and trailer lines that Node's parser refuses were taken. 3 Minors: surviving mutants, an empty `Transfer-Encoding`, three statements in the text |
| 3 | the rework | pass | 3 Minors: a GET with no version, three character classes wider than the RFC's, surviving mutants |

### ✅ Verified correct (keep it)
- **The tests fail without the change.** With the earlier source over the working copy and the tests kept, the new tests fail on
  their own assertions, and they pass with the change. The empty `Transfer-Encoding` test pins that the new branch leaves such a
  request as it was.
- **Mutants.** Round 3 ran 61 on the parser: 45 failed a test, 4 change no behavior and 12 survived. Rows were then added for the
  survivors, and the producing session re-ran seven mutants of that kind, each of which now fails a test. One, a size bound that
  changes only when a 400 comes, is accepted.
- **The same bytes on Python and Node.** 336 raw-socket rows ran against real `python -m funcd_shim` and
  `python -m funcd_shim.pool` processes and the Node shim funcd pins, on Node v22 and v23. Solo equals pool on every row, and the rows
  of round 2's Major answer 400 and close, as on Node. The rows that still differ answer differently without changing the framing
  (a line ends at the same CRLF on both sides), or are routed below.
- **It is a bug.** It breaks ADR-0049 Decision 2 and its "Contract fidelity" constraint, and ADR-0050 Decision 1 for the pool. ADR-0134
  and ADR-0181 accept that the edge sends a GET with an envelope, and none of them accepts a shim that leaves a body on the connection.
  ADR-0041 keeps upstream connections open for reuse. No issue, card, row or test holds it.
- **Not a change to the funcd <-> shim contract, so no funcd ADR is needed.** No `FUNCD_*` variable, endpoint, socket, log or span
  changed, and funcd's probes send no body. The constants are no new policy: `_TOKEN`, `_QUOTED`, `_CHUNK_LINE` and `_TRAILER_LINE`
  are grammar, `_TRAILER_REFUSED` and `1 << 64` are parse rules Node's parser has too, and `_MAX_CHUNK_LINE` matches Node's 16 KiB.
- **The regular expressions against the RFC.** `_TOKEN` is RFC 9110 §5.6.2's `tchar`. `_CHUNK_LINE` is RFC 9112 §7.1.1's `chunk-ext`
  with its `BWS` removed, a choice of Node over the RFC that ADR-0049 sets. `_TRAILER_LINE` follows RFC 9112 §5.1.
- **Siblings: none.** Only `shim.py` and `pool.py` serve HTTP, and `pool.py` imports `read_body`. A method with no `do_` handler gets the
  stdlib's 501 with `Connection: close`, so nothing stays on the connection.
- **Reuse, scope and conventions.** `re` is the stdlib's and the fix reuses `read_body`. No test was weakened or deleted. `version.txt`,
  `CHANGELOG.md`, `uv.lock` and `embed.go` are untouched.
- **Checks.** `just ci` exits 0, and the PR's checks conclude success. funcd's Python-related e2e tests, `cmd/funcd`, `cmd/funcdctl` and
  `TestWorkerReadsEveryCallsBody` pass against the change through a local `go.work`.

### Routed (not this work's)
- **Other methods and the 404 body** · destination: `docs/behavior/calls.md`, "Another method on a Python worker", `uncovered`.
- **An empty line before a request line, and a `Content-Length` body cut short** · destination: the decider and a later change to
  `read_body`. Verdict: `uncovered`.

### Not done
- Lanes and the funcd gate: the session driving this work runs them over all its branches. Round 3's raw rows ran on `httptest.NewServer`, not
  on `pkg/funcd`'s listeners. ADR-0013, ADR-0016, ADR-0037, ADR-0041, ADR-0044 and ADR-0050 were searched, not all read whole.

### Definition of Done
Round 3 counted 10 / 12 (the `/fix-review` fix checklist, adapted to Python: no `-race`, and no issue number in the test name or the
commit; item 13 does not apply, because no design spec under `docs/design` models the shim's framing). Its misses, items 4 and 12,
are its Minors, folded since.

### Model scorecard
claude-opus-5-5 on this pyvvo/funcd-python fix: round 1 pass, 0/0/3; round 2 changes requested, 0/1/3; round 3 pass, 0/0/3.
