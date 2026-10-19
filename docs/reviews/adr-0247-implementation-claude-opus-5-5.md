## Verdict: pass — 0 blockers, 0 majors, 1 minor fixed before merge  (ADR-0247 implementation, model: claude-opus-5-5)

This reviews ADR-0247's implementation, built in four repos:

- pyvvo/funcd#1099, head `91fd2c3b`, merged as `cd914428`. The ADR's range is `61b5d195` (push and types),
  `b821c4e7` (the lanes), `2c25a49b` (dev) and `98bf3bb0` (the review fix); `7c455dc1` moves the lanes for ADR-0250
  and ADR-0251. The PR also carries ADR-0249, ADR-0250 and ADR-0251 (their own reports) and `cb8f9075` and `91fd2c3b`, which pins the
  language repos.
- pyvvo/funcd-typescript#79 (the Vite plugin and the examples), released in `v0.19.0`, and its fix #86 in `v0.19.2`.
- pyvvo/funcd-python#92 (funcd-bundle and the examples), released in `v0.14.0`.
- pyvvo/funcd-functions#19 (the apps' layout and the round-2 bundlers), released in `v0.19.0`.

The review found one Minor, attributed to the model, fixed before the merge:

- `functionDocument` (`cmd/funcdctl/contract.go`) skipped a `*.yaml` file whose documents failed to decode. A typo in
  `function.yaml` therefore produced types without the binding types instead of an error, while `dev` stopped on the
  same file. `98bf3bb0` moves the per-document decode into the untagged `resourceDocuments`, which both use, and
  `TestScenarioTypesFromContract/broken-function` fails without it.

The sql-examples lane also found a defect before the merge, which this review of pyvvo/funcd-typescript#79 had passed.
#79 moved sql-notes' migrate entry from `src/migrate.ts` to `src/notes-migrate/main.ts`, but kept
`import.meta.glob('../drizzle/...')`. That now resolves to `src/drizzle`, so the rebuilt bundle inlined no migration
and migrate answered 500. pyvvo/funcd-typescript#86 (`v0.19.2`) points the glob at `../../drizzle/` and adds a test
that the committed bundle inlines every migration and the journal. No other example uses `import.meta.glob`, and
the todo App's migrate globs resolve correctly.

### ✅ Verified correct (keep it)

- **Checks**:
  - The builder's `scripts/agent/gate.sh` log shows every step passed on `91fd2c3b`: audit, `just ci-full` with
    e2e (123 packages `ok`, 0 lint issues with and without `-tags dev`), linux, linux-tests and tree-unchanged.
  - On #1099, `changes`, `pr-title`, `ci` and `e2e` passed on `91fd2c3b`.
  - With `-race` on `f92b6320`, through a `go.work` on the round-2 language-repo heads, the review ran the 13 funcd
    scenario tests (9 with `-tags dev`); all pass.
- **Contracts**: `ContractFile`, `LoadContract`, `GenerateTypes` (`pkg/sdk`) and `funcd.WithWorkflowRuntimeResolver`
  match the ADR's signatures; `funcdctl dev` passes its runtimes to it (`cmd/funcdctl/dev.go:924`).
- **Decisions 1-3**:
  - `LoadContract` takes exactly one of the two variants and decodes it strictly.
  - `push` reads the contract file beside a file build, or at a bundle's root, and gates both sides
    (`contract.Check`). For an `http` contract it bakes none and removes a stale `__funcd_contract.json`. The runtime
    comes from the contract, and `--schema` and `--runtime` are gone.
  - `types -f` reads the contract file and the Function named after it in the same folder, and refuses a runtime or
    protocol mismatch.
- **Decision 4**: `loadDevFolder` reads the folder's top-level `*.yaml` only. It skips contract files, non-funcd
  documents and FuncdConfig, and stops on an App (#1067), two documents of one kind and name, a namespace other than
  default, and a file that does not parse.
- **Decision 6**: a node build is `<name>.mjs` in the folder or `dist/`, both being an error, with its contract file
  beside it. A python build runs in place from its `[tool.funcd-bundle.functions]` entry, else `src/main.py`, else
  `<name>.py`.
- **Decisions 7-8**: a Workflow image step builds from its tag's stem as `<workflow>-<step>`, and runs through the
  materializer. A fingerprint of the contract file and the sources becomes `spec.imageDigest`, so a rebuild rolls a
  revision, and a folder change reloads.
- **Decisions 10-12**:
  - The Vite plugin requires one contract file per function, copies it beside `<name>.mjs` and removes a stale copy
    of the other variant. It watches the contract file and defaults to `src/main.ts`.
  - funcd-bundle drops PyYAML, reads `[tool.funcd-bundle.functions]` (default `src/main.py`) and copies the contract
    to the bundle's root.
  - A `funcdctl.yaml` fails in both bundlers, `push`, `types` and `dev`, naming the files that replace it.
- **No `funcdctl.yaml` remains**: there are none in funcd at `f92b6320`, funcd-typescript at `1633541d`,
  funcd-python at `v0.14.0`, or funcd-functions after #19 and #21.
- **Lanes**: every lane passes on the released pins, from the XML files the review read:
  - duckdb (11 cases), egress (5), env-echo (16), fn-to-fn (4), funclog (16), http-api (3), kv-counter (3),
    metastore (2), s3 (8), sql (9) and workflow (14), on `cb8f9075`;
  - sql-examples (4) and app-todo (50), on `91fd2c3b`;
  - none has a failure or an error. After `cb8f9075`, the PR changes only tests, the App part-root helper (which
    app-todo reruns) and the funcd-typescript pin (which sql-examples reruns).
- **Scenarios**: each of the 14 has its test, named after it. 13 are in `cmd/funcdctl`, and `bundlers-copy-the-contract`
  is in both bundlers' suites (`vite-plugin/test`, `bundle/tests/test_bundle.py`).
- **Dependencies**: BurntSushi/toml (MIT, named in the ADR) moves from indirect to direct. Nothing else is added.
- **Tracking**: ADR-0247 is at `Reviewing`, and only its status line changed; F160 is at `reviewing`. The PR
  description carries `Fixes #1069`.

### Definition of Done

17 / 17 items hold:

- the ADR's Review checklist, 4 of 4;
- its done-when, 4 counted: `just ci-full` (with the `-tags dev` lint), every Lima lane, the three language repos'
  CI, and no `funcdctl.yaml` in the four repos;
- the generic DoD, 9 applicable: suite green, scenarios, real behavior, Contracts, tree, conventions, dependencies,
  scope, tracking. The per-driver contract suites do not apply, since no port changed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0247 (implementation) → pass, 0/0/1, 1 model-attributed (fixed before merge),
DoD 17/17. See docs/reviews/model-scorecard.md.

### Recommendation

#1099 merged and closed #1069. This review stamps ADR-0247 `Implemented` and F160 `implemented`.
