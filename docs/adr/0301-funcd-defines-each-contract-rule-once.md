# ADR-0301: funcd defines each contract rule once, in its contract package

- **Status**: Reviewing 2026-10-30; Accepted 2026-10-30 by an `adr-batch` run the decider started ("then adr-batch till completion");
  judged twice: round 1's findings folded, round 2 found no Blocker
- **Date**: 2026-10-30
- **Deciders**: green-0-rabbit
- **Tags**: contract, validation, json-schema
- **Realizes**: [FEAT-0001/F88](../feat/0001-feat-v1.1.md) (runtime-compiled I/O validators — advertised == enforced)
- **Refines**: [ADR-0058](0058-contract-codegen-from-code-types.md): its profile row names the `format` values and
  allows `pattern` without saying what they accept, and its 422/500 bodies carry `details` without a shape. This ADR
  fixes both. [ADR-0150](0150-int64-contract-range.md)'s `int64` range is kept unchanged.
- **Relates to**: [ADR-0123](0123-runtime-compiled-io-validators.md) (advertised == enforced) ·
  [ADR-0037](0037-typescript-hono-runtime-shim.md) and [ADR-0049](0049-python-runtime-shim.md) (the shims, unchanged
  here) · issue [#1339](https://github.com/pyvvo/funcd/issues/1339)

## Context & Need

The engine that checks a contract decides what its rules mean, and funcd defines none of them. The Node shim checks
with ajv and ajv-formats 3.0.1, the Python shim with fastjsonschema 2.22.2, and the profile only lists the `format`
names (`internal/contract/contract.go:134-138`). So the same contract accepts different data on each runtime (#1339):

| Contract | Value | Node shim | Python shim |
|---|---|---|---|
| `format: date-time` | `"2026-02-30T10:00:00Z"` | 422 | 204 (the handler ran) |
| `format: uuid` | `"urn:uuid:550e8400-e29b-41d4-a716-446655440000"` | 204 | 422 |
| `pattern: ^\d+$` | `"١٢٣"` (Arabic-Indic digits) | 422 | 204 |
| two fields fail | `{"a": 1, "b": "x"}` | 422 with two ajv error objects | 422 with one string |

The causes are in the engines. ajv-formats lets a `uuid` carry `urn:uuid:`
([formats.ts#L73](https://github.com/ajv-validator/ajv-formats/blob/v3.0.1/src/formats.ts#L73)); fastjsonschema's
format expressions do not check the calendar
([draft04.py#L28-L34](https://github.com/horejsek/python-fastjsonschema/blob/v2.22.2/fastjsonschema/draft04.py#L28-L34)),
and Python's `re` reads `\d` as any Unicode digit. The Node shim lists every error
([contract.ts#L27-L39](https://github.com/pyvvo/funcd-typescript/blob/4a1ef558a936fc840c86d65336b304ba7affc1b4/shim/src/contract.ts#L27-L39)),
the Python shim the first one as a string
([contract.py#L90-L98](https://github.com/pyvvo/funcd-python/blob/b26a0cc9b862e348dae450ad3266f123cccce881/shim/src/funcd_shim/contract.py#L90-L98)).

The purpose: one definition of each rule, owned by funcd and pinned by tests: what each profile format and a `pattern`
accept, and what a 422 or 500 lists. Its callers are funcd's profile gate now (`contract.Check`, run by
`funcdctl push`, `funcdctl types` and the bundle gate) and the call-time check later, which a following ADR places and
which closes #1339.

## Scenarios

Each scenario runs on the contract package (`Compile`, then `Validate`) unless it names push.

- `scenario: date-time-follows-rfc3339` — Given a side `{"type": "string", "format": "date-time"}`, When the value is
  `"2026-10-30T10:00:00Z"` or `"2026-10-30T10:00:00.5+02:00"`, Then it passes; When it is `"2026-02-30T10:00:00Z"`,
  `"2026-10-30T25:00:00Z"` or `"2026-10-30T10:00:00"`, Then `Validate` returns one detail with path `""`.
- `scenario: uuid-is-the-string-form` — Given `format: uuid`, Then `"550E8400-E29B-41D4-A716-446655440000"` passes and
  `"urn:uuid:550e8400-e29b-41d4-a716-446655440000"` returns one detail.
- `scenario: email-is-an-rfc5321-mailbox` — Given `format: email`, Then `"ops@localhost"` passes and
  `"a b@example.com"` returns one detail.
- `scenario: uri-has-a-scheme-and-is-ascii` — Given `format: uri`, Then `"https://example.com/a?b=c"` and
  `"https://example.com/a#b"` pass, and `"http://例え.テスト/"`, `"http://example.com/a b"` and `"/relative/path"`
  each return one detail.
- `scenario: integer-formats-keep-their-ranges` — Given `format: int32`, Then 2147483647 and −2147483648 pass and
  2147483648 and −2147483649 fail; Given `format: int64`, Then ±9007199254740991 pass and ±9007199254740992 fail
  (ADR-0150).
- `scenario: pattern-follows-ecma262` — Given `pattern: ^\d+$`, Then `"123"` passes, and `"١٢٣"` and `"123\n"` fail;
  Given `pattern: ^\w+$`, Then `"café"` fails; Given `pattern: ^(?!admin$)[a-z]+$`, Then `"alice"` passes and
  `"admin"` fails.
- `scenario: a-runaway-pattern-match-fails-the-value` — Given `pattern: ^(a+)+$` and the default match timeout, When
  the value is 40 `a` followed by `!`, Then `Validate` returns within 1 s with one detail at path `""`.
- `scenario: every-error-is-listed` — Given a closed object whose required properties are `a` (string) and `b`
  (integer), When the value is `{"a": 1, "b": "x"}`, Then `Validate` returns two details, paths `/a` then `/b`, each
  with a message; When it is `{"a": "x"}`, Then one detail at `/b`; When it is `{"a": "x", "b": 1, "c": true}`,
  Then one detail at `/c`.
- `scenario: a-union-reports-its-tagged-variant` — Given a discriminated `oneOf` on `kind` whose variants are
  `{"kind": "a", "x": integer}` and `{"kind": "b", "y": string}`, When the value is `{"kind": "a", "x": "1"}`, Then
  one detail at `/x`; When it is `{"kind": "c"}` or has no `kind`, Then one detail at `""`; Given two variants that
  both accept `{"kind": "a"}`, When the value is `{"kind": "a"}`, Then one detail at `""`.
- `scenario: push-gates-patterns` — Given a contract side with `pattern: "[z-a]"`, When `funcdctl push`,
  `funcdctl types` or the bundle gate runs `contract.Check` on it, Then it fails with Invalid naming the pattern's
  schema location and nothing is pushed; Given `pattern: ^(?!admin$)[a-z]+$` instead, Then `contract.Check` passes.

## Scope

In: the meaning of the profile formats (`date-time`, `uuid`, `email`, `uri`, `int32`, `int64`) and of `pattern`; the
shape of `details` in a 422 (input) and a 500 (output); the Go API that compiles a contract side with these rules and
validates a document; `contract.Check` compiling each side with it.

Out:
- where the call-time check runs (a per-worker Go front or funcd's gateway), its wiring, its config keys, and the
  budget of one request: the following ADR, which closes #1339;
- the Node and Python shims, unchanged until that ADR replaces their check;
- funcd's Go `CheckInput` for Workflow inputs, which checks types only (`internal/controlplane/admission/workflowrun.go:89`,
  `internal/workflow/engine.go:304`), as in ADR-0150;
- App values schemas ([ADR-0217](0217-app-templates-values-and-rendering.md)), which keep the engine's defaults
  (`internal/app/template/values.go:133-134`: RE2 patterns, formats as annotations);
- a new format, and the `error` string of a 422 or 500 (unchanged).

## Constraints & Decision drivers

- ADR-0058 Decision 2: a contract means the same on every runtime. ADR-0123: advertised == enforced, so a format is an
  assertion, not the annotation JSON Schema 2020-12 makes it by default (Validation §7.2).
- JSON Schema 2020-12 names the standards: date-time is RFC 3339 §5.6, email is the RFC 5321 §4.1.2 Mailbox, uri is
  RFC 3986, uuid is RFC 4122 (Validation §7.3.1, §7.3.2, §7.3.5); `pattern` is ECMA-262 (§6.3.3), built with the `u`
  flag and not anchored (Core §6.4).
- Blueprint: library-first, Apache-2.0 or MIT. santhosh-tekuri/jsonschema v6.0.3 (Apache-2.0) is a direct dependency
  (`go.mod:50`); dlclark/regexp2/v2 v2.2.1 (MIT) is already in the module graph through goja (`internal/expr`).
- A pattern runs on caller data, and an ECMA-262 engine backtracks, so each match needs a time bound.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **One Go definition in `internal/contract`; push uses it now, the call-time check later** | One definition under test; Go is funcd's language and the engine is already a dependency | The shims keep disagreeing until the call-time ADR ships | **chosen** (#1339) |
| Register the same rules in each shim now (ajv, fastjsonschema) | Closes the gap at once | Three definitions to keep equal, and shim code plus two releases that the call-time check then retires | rejected |
| The engine's own behavior is the definition | No code | An engine upgrade changes a verdict unnoticed; the built-in `uri` accepts an IRI and a space ([format.go#L515-L548](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/format.go#L515-L548)) | rejected |
| Patterns in Go's RE2, the engine's default ([compiler.go#L330-L331](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L330-L331)) | Linear time, no timeout needed | Not ECMA-262: refuses lookarounds and backreferences that ajv accepts | rejected |
| Patterns through regexp2 v1 (`ECMAScript`), already an indirect dependency | Same engine family | Its option follows C#'s `RegexOptions.ECMAScript`; v2's targets the ECMAScript specification (README) and is the version goja uses for funcd's expressions | rejected |
| **Patterns in ECMA-262 through regexp2/v2 (`ECMAScript\|Unicode`), with a match timeout** | JSON Schema's dialect; the reading goja already gives funcd | Backtracking, so it needs the timeout | **chosen** |
| `details` lists the first error only | fastjsonschema's default | Hides the other errors | rejected |
| A union's `details` lists every variant's errors (the engine's output) | No selection code | A value with a valid tag also reports every other variant's tag and required failures | rejected |

## Decision

1. **A format means its standard**, and every format is asserted. funcd registers its own check for each profile name
   with `Compiler.RegisterFormat`, which replaces the engine's built-in of the same name
   ([compiler.go#L67-L79](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L67-L79)):

   | Format | Meaning | Passes | Fails |
   |---|---|---|---|
   | `date-time` | RFC 3339 §5.6, with §5.7: the day exists in its month, the hour is 00–23, the offset is required | `2026-10-30T10:00:00Z` | `2026-02-30T10:00:00Z`, `2026-10-30T10:00:00` |
   | `uuid` | RFC 4122 §3 string form, hex digits in any case, no `urn:uuid:` prefix | `550E8400-E29B-41D4-A716-446655440000` | `urn:uuid:550e8400-…` |
   | `email` | RFC 5321 §4.1.2 `Mailbox` | `ops@localhost` | `a b@example.com` |
   | `uri` | RFC 3986 §3 URI: a scheme is required, a fragment is allowed, ASCII only, so an IRI (RFC 3987) fails | `https://example.com/a#b` | `http://例え.テスト/`, `http://example.com/a b`, `/relative/path` |
   | `int32` | −2^31 … 2^31 − 1 | 2147483647 | 2147483648 |
   | `int64` | ADR-0150: −(2^53 − 1) … 2^53 − 1 | 9007199254740991 | 9007199254740992 |

   A table pins each meaning: the scenario values plus the JSON-Schema-Test-Suite draft 2020-12 `optional/format`
   cases for `date-time`, `email`, `uri` and `uuid` (MIT, commit `89e83a7`). A check may reuse the engine's built-in
   only where that built-in passes the whole table.
2. **A `pattern` means what ECMA-262 says**, with the `u` flag. regexp2/v2 compiles it with `ECMAScript|Unicode`, the
   options goja uses, so `\d` is `[0-9]`, `\w` is `[A-Za-z0-9_]` and `$` ends the input. A pattern is not anchored.
   Each match runs under a timeout, 100 ms by default, and a match that times out fails the value like any non-match.
   The default matches regexp2's deadline clock, which ticks every 100 ms by default (`DefaultClockPeriod`), so a
   shorter timeout is not tighter; funcd leaves that process-wide period alone. A legitimate match takes microseconds.
3. **`details` lists every error**, in one shape for input (422) and output (500): one `{"path", "message"}` entry per
   leaf of the engine's error tree. `path` is an RFC 6901 JSON Pointer; `""` is the root. A missing required property
   and an extra property of a closed object each get their own entry at their own pointer (`/b`). In a discriminated
   `oneOf`, only the variant whose tag the value names is reported. A missing or unknown tag, a tag that more than
   one variant names, or a failed union whose tagged variant adds no leaf gives one entry at the union's path, so an
   invalid document always yields at least one entry. Entries are sorted by `path` in byte order, then by `message`.
   `message` is for people, and its wording is not part of the contract. ADR-0058's `error` string is unchanged.
4. **`internal/contract` owns these rules**, through `Compile` and `Validator.Validate` (Contracts). `Check` also
   compiles each side with `Compile`, so `funcdctl push` and `funcdctl types` (`cmd/funcdctl/contract.go:47-58`) and
   the bundle gate (`internal/artifact/bundle.go:209-212`) refuse a side that cannot be checked, an invalid pattern
   included.
5. **The shims stay unchanged.** A following ADR moves the call-time check to this package and decides where it runs;
   #1339 closes with that ADR's implementation.

## Design model

None: the decision defines formats, a pattern dialect and an error shape, and picks a library (ADR-0272). The push
gate and the call-time check are two rules that must agree, and they agree by construction: both call `Compile`.

## Temporary workarounds

The Node and Python shims keep ajv and fastjsonschema, which disagree with Decisions 1 to 3 on the cases in Context.
Push now compiles patterns with regexp2 only, so a pattern that Python's `re` cannot compile (`\p{…}`, a
variable-width lookbehind) passes push and then fails the worker's validator compile, as any pattern can today. Exit
criterion: the call-time ADR's implementation checks every call with this package and the shims stop checking; #1339
closes then.

## Contracts

```go
// internal/contract/rules.go (new), beside Check. A 422 or 500 body stays ADR-0058's {error, details};
// details is a []Detail.

// Detail is one entry of a 422 or 500 body's details.
type Detail struct {
	Path    string `json:"path"`    // RFC 6901 JSON Pointer to the failing value; "" is the root
	Message string `json:"message"` // for people; the wording is not part of the contract
}

// Option configures Compile.
type Option func(*options)

// WithMatchTimeout bounds each pattern match (default 100 ms). A match that runs longer fails the value.
func WithMatchTimeout(d time.Duration) Option

// Validator checks JSON documents against one compiled contract side. It is safe for concurrent use.
type Validator struct{ /* unexported */ }

// Compile compiles one contract side as JSON Schema 2020-12 with funcd's rules (Decisions 1 and 2). An empty
// schemaJSON compiles to a Validator that accepts every document (ADR-0058's Json form). A side that does not
// compile returns a fault.Invalid naming its schema location.
func Compile(schemaJSON []byte, opts ...Option) (*Validator, error)

// Validate returns every error of doc (Decision 3), or nil when doc is valid. Numbers are compared exactly, never
// through float64. It returns a fault.Invalid when doc is not JSON. It does no I/O, and the match timeout bounds
// each pattern match (Decision 2), so it takes no context.
func (v *Validator) Validate(doc []byte) ([]Detail, error)
```

A 422 body under this ADR (the messages are illustrative):

```json
{
  "error": "event data does not match the input contract",
  "details": [
    {"path": "/a", "message": "got number, want string"},
    {"path": "/b", "message": "got string, want integer"}
  ]
}
```

| Consumes | Exposes |
|---|---|
| A contract side: JSON Schema 2020-12 inside the ADR-0058 profile | `Compile`, `Validator.Validate`, `Detail` |
| santhosh-tekuri/jsonschema/v6 v6.0.3 (direct); dlclark/regexp2/v2 v2.2.1 (indirect today, direct after this ADR) | `Check` fails on a side that does not compile |

## Implementation plan

1. `internal/contract/rules.go`: the Contracts. Each `Compile` builds a `jsonschema.Compiler` with
   `DefaultDraft(jsonschema.Draft2020)`, `AssertFormat()`, `RegisterFormat` for the six profile names, and
   `UseRegexpEngine` with regexp2/v2 (`ECMAScript|Unicode`, `MatchTimeout` on each compiled pattern; a timeout error
   is a non-match). The leaves come from walking `ValidationError.Causes`, as `leaves` in
   `internal/app/template/values.go` does, with each token escaped per RFC 6901. The engine's compiled schema keeps no
   `discriminator`, so the union rule reads it and each variant's tag `const` or `enum` from the side's JSON, as `walk`
   decodes it (`internal/contract/contract.go:115-127`).
2. `internal/contract/contract.go`: `Check` calls `Compile` after `walk` and wraps its error as Invalid.
3. `go mod tidy`: regexp2/v2 v2.2.1 moves to the direct block; no new module.
4. Tests in `internal/contract`: one test per scenario named `TestScenario` plus the scenario name in CamelCase
   (`TestScenarioDateTimeFollowsRFC3339`, …), `TestScenarioPushGatesPatterns` through `Check`; `TestFormatTable` over
   the test-suite files vendored under `testdata/jsts/` with their license; `TestValidateConcurrent` under `-race`.
5. `just ci` green. Definition of done: every scenario has one passing test, `TestFormatTable` passes, and no shim,
   gateway or `CheckInput` code changes.

## Review checklist

- [ ] funcd registers the six profile formats with the meanings of Decision 1, and every case of the table passes.
- [ ] Patterns compile with regexp2/v2 `ECMAScript|Unicode` and a `MatchTimeout`; a timed-out match fails the value.
- [ ] `Validate` returns the leaves of `Causes` as `{path, message}`: RFC 6901 paths, one entry per missing or extra
      property, only the tagged variant of a union, sorted by path in byte order then message.
- [ ] An invalid document always yields at least one entry, including a union that two variants accept.
- [ ] `Check` compiles each side: `[z-a]` fails it and `^(?!admin$)[a-z]+$` passes it.
- [ ] Each scenario has one named, passing test; `TestValidateConcurrent` passes under `-race`.
- [ ] regexp2/v2 v2.2.1 is direct in `go.mod`; no shim, gateway or `CheckInput` change.

## Consequences

- Positive: each rule has one definition, written by funcd and pinned by a table, so an engine upgrade cannot change a
  pinned verdict unnoticed. Push refuses a pattern that no check could run. The call-time ADR only wires this package.
- Negative: the shims keep disagreeing until the call-time ADR ships (Temporary workarounds). A contract whose pattern
  regexp2 cannot compile now fails push and `funcdctl types`; no example in funcd-functions v0.7.0 has a pattern or a
  format. Detail messages differ from the shims' wording.
- Risks accepted: the timeout bounds one match, not one document. A pathological pattern costs up to the timeout plus
  one clock tick for each string it checks, and a `oneOf` checks a string once per variant: a 1 MiB document of short
  strings can cost on the order of 100 CPU-minutes. The call-time ADR sets the budget of one request. The timeout is wall-clock, so a match near it can pass on an idle host and fail on a loaded
  one; legitimate patterns finish orders of magnitude below it. regexp2/v2's ECMAScript mode aims at the
  specification but is not a JavaScript engine; the tests pin what funcd relies on, and push refuses what regexp2
  cannot compile.

## Open questions

- Where the call-time check runs (a per-worker Go front or funcd's gateway), the funcdconfig key for the match
  timeout, the CPU budget of one request, and whether `details` is capped: the call-time ADR.
- A 422 that funcd's own Go code produces at the edge falls under the blueprint's RFC 9457 rule
  (`blueprint.md:970`), while this body is ADR-0058's `{error, details}`: the call-time ADR reconciles them.
- Whether the profile requires each union variant to declare a distinct, required tag: an issue against ADR-0058's
  profile. Until then, Decision 3's fallback keeps `Validate` failing closed.

## References

- Issue [#1339](https://github.com/pyvvo/funcd/issues/1339) and its decision comment; issue #518 (ADR-0150).
- JSON Schema 2020-12: Core §6.4 (regular expressions: ECMA-262, the `u` flag, not anchored); Validation §6.3.3
  (`pattern`), §7.2 (format as annotation or assertion), §7.3.1, §7.3.2 and §7.3.5 (date-time, email, uri, uuid).
- RFC 3339 §5.6–5.7, RFC 4122 §3 (obsoleted by RFC 9562, same string form), RFC 5321 §4.1.2, RFC 3986 §3, RFC 3987,
  RFC 6901, ECMA-262.
- JSON-Schema-Test-Suite (MIT), `tests/draft2020-12/optional/format/` at
  [`89e83a7`](https://github.com/json-schema-org/JSON-Schema-Test-Suite/tree/89e83a74014617ffbe79a56d83d14d054cd11417/tests/draft2020-12/optional/format).
- santhosh-tekuri/jsonschema v6.0.3:
  [compiler.go#L53](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L53) (`AssertFormat`),
  [#L67-L79](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L67-L79) (`RegisterFormat`),
  [#L146](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L146) (`UseRegexpEngine`),
  [#L306-L314](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L306-L314) (`Regexp` is
  `MatchString(string) bool`), [validator.go#L960-L972](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/validator.go#L960-L972)
  (`ValidationError.Causes`). It does not document `*Schema` as safe for concurrent use, so `TestValidateConcurrent`
  is the guard.
- dlclark/regexp2/v2 v2.2.1 (MIT):
  [options.go#L34-L36](https://github.com/dlclark/regexp2/blob/v2.2.1/options.go#L34-L36) (`ECMAScript`, `Unicode`),
  [regexp.go#L31](https://github.com/dlclark/regexp2/blob/v2.2.1/regexp.go#L31) (safe for concurrent use),
  [regexp.go#L38](https://github.com/dlclark/regexp2/blob/v2.2.1/regexp.go#L38) (`MatchTimeout`),
  [fastclock.go#L117](https://github.com/dlclark/regexp2/blob/v2.2.1/fastclock.go#L117) (`DefaultClockPeriod`),
  [README.md#L272-L275](https://github.com/dlclark/regexp2/blob/v2.2.1/README.md#L272-L275) (the ECMAScript mode).
  goja compiles patterns with the same options (`regexp.go:89-102` at the version in `go.mod`).
- The shims' 422 today: funcd-typescript
  [shim.ts#L129](https://github.com/pyvvo/funcd-typescript/blob/4a1ef558a936fc840c86d65336b304ba7affc1b4/shim/src/shim.ts#L129);
  funcd-python
  [shim.py#L272](https://github.com/pyvvo/funcd-python/blob/b26a0cc9b862e348dae450ad3266f123cccce881/shim/src/funcd_shim/shim.py#L272).
