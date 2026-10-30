# ADR-0301: funcd defines each contract rule once, in its contract package

- **Status**: Draft
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
`funcdctl push` and the bundle gate) and the call-time check later, which a following ADR places and which closes #1339.

## Scenarios

Each scenario runs on the contract package (`Compile`, then `Validate`) unless it names push.

- `scenario: date-time-follows-rfc3339` — Given a side `{"type": "string", "format": "date-time"}`, When the value is
  `"2026-10-30T10:00:00Z"` or `"2026-10-30T10:00:00.5+02:00"`, Then it passes; When it is `"2026-02-30T10:00:00Z"`,
  `"2026-10-30T25:00:00Z"` or `"2026-10-30T10:00:00"`, Then `Validate` returns one detail with path `""`.
- `scenario: uuid-is-the-string-form` — Given `format: uuid`, Then `"550E8400-E29B-41D4-A716-446655440000"` passes and
  `"urn:uuid:550e8400-e29b-41d4-a716-446655440000"` returns one detail.
- `scenario: email-is-an-rfc5321-mailbox` — Given `format: email`, Then `"ops@localhost"` passes and
  `"a b@example.com"` returns one detail.
- `scenario: uri-is-absolute-and-ascii` — Given `format: uri`, Then `"https://example.com/a?b=c"` passes, and
  `"http://例え.テスト/"` and `"/relative/path"` each return one detail.
- `scenario: integer-formats-keep-their-ranges` — Given `format: int32`, Then 2147483647 passes and 2147483648 fails;
  Given `format: int64`, Then 9007199254740991 passes and 9007199254740992 fails (ADR-0150).
- `scenario: pattern-follows-ecma262` — Given `pattern: ^\d+$`, Then `"123"` passes and `"١٢٣"` fails; Given
  `pattern: ^\w+$`, Then `"café"` fails; Given `pattern: ^(?!admin$)[a-z]+$`, Then `"alice"` passes and `"admin"`
  fails.
- `scenario: a-runaway-pattern-match-fails-the-value` — Given `pattern: ^(a+)+$` and the default match timeout, When
  the value is 40 `a` followed by `!`, Then `Validate` returns within 1 s with one detail whose message names the
  timeout.
- `scenario: every-error-is-listed` — Given a closed object whose required properties are `a` (string) and `b`
  (integer), When the value is `{"a": 1, "b": "x"}`, Then `Validate` returns two details, paths `/a` then `/b`, each
  with a message.
- `scenario: push-gates-patterns` — Given a contract side with `pattern: "[z-a]"`, When `funcdctl push` or the bundle
  gate runs `contract.Check` on it, Then it fails with Invalid naming the pattern's schema location and nothing is
  pushed; Given `pattern: ^(?!admin$)[a-z]+$` instead, Then `contract.Check` passes.

## Scope

In: the meaning of the profile formats (`date-time`, `uuid`, `email`, `uri`, `int32`, `int64`) and of `pattern`; the
shape of `details` in a 422 (input) and a 500 (output); the Go API that compiles a contract side with these rules and
validates a document; `contract.Check` compiling each side with it.

Out:
- where the call-time check runs (a per-worker Go front or funcd's gateway), its wiring and config keys, and whether a
  422 caps `details`: the following ADR, which closes #1339;
- the Node and Python shims, unchanged until that ADR replaces their check;
- funcd's Go `CheckInput` for Workflow inputs, which checks types only (`internal/controlplane/admission/workflowrun.go:89`,
  `internal/workflow/engine.go:304`), as in ADR-0150;
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
| The engine's own behavior is the definition | No code | An engine upgrade changes a verdict unnoticed; the engine accepts an IRI as a `uri` ([format.go#L535-L548](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/format.go#L535-L548)) | rejected |
| Patterns in Go's RE2, the engine's default ([compiler.go#L330-L331](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L330-L331)) | Linear time, no timeout needed | Not ECMA-262: refuses lookarounds and backreferences that ajv accepts | rejected |
| **Patterns in ECMA-262 through regexp2/v2 (`ECMAScript\|Unicode`), with a match timeout** | JSON Schema's dialect; the reading goja already gives funcd's expressions | Backtracking, so it needs the timeout | **chosen** |
| `details` lists the first error only | fastjsonschema's default | Hides the other errors | rejected |

## Decision

1. **A format means its standard**, and every format is asserted. funcd registers its own check for each profile name
   with `Compiler.RegisterFormat`, which replaces the engine's built-in of the same name
   ([compiler.go#L67-L79](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L67-L79)), so no
   engine default decides a verdict:

   | Format | Meaning | Passes | Fails |
   |---|---|---|---|
   | `date-time` | RFC 3339 §5.6 with §5.7: the day exists in its month, the hour is 00–23, the offset is required | `2026-10-30T10:00:00Z` | `2026-02-30T10:00:00Z`, `2026-10-30T10:00:00` |
   | `uuid` | RFC 4122 §3 string form, hex digits in any case, no `urn:uuid:` prefix | `550E8400-E29B-41D4-A716-446655440000` | `urn:uuid:550e8400-…` |
   | `email` | RFC 5321 §4.1.2 `Mailbox` | `ops@localhost` | `a b@example.com` |
   | `uri` | RFC 3986 §4.3 absolute URI, ASCII only, so an IRI (RFC 3987) fails | `https://example.com/a?b=c` | `http://例え.テスト/`, `/relative/path` |
   | `int32` | −2^31 … 2^31 − 1 | 2147483647 | 2147483648 |
   | `int64` | ADR-0150: −(2^53 − 1) … 2^53 − 1 | 9007199254740991 | 9007199254740992 |

2. **A `pattern` means what ECMA-262 says**, with the `u` flag. regexp2/v2 compiles it with `ECMAScript|Unicode`, the
   options goja uses, so `\d` is `[0-9]` and `\w` is `[A-Za-z0-9_]`. A pattern is not anchored. Each match runs under a
   timeout, 100 ms by default; a match that times out fails the value with a detail that says so.
3. **`details` lists every error**, in one shape for input (422) and output (500): one `{"path", "message"}` entry per
   failing keyword at a value, which are the leaves of the engine's error tree. `path` is an RFC 6901 JSON Pointer to
   the value; `""` is the root, and a missing required property is reported at its object's path. Entries are sorted
   by `path`, then `message`. `message` is for people, and its wording is not part of the contract. ADR-0058's `error`
   string is unchanged.
4. **`internal/contract` owns these rules**, through `Compile` and `Validator.Validate` (Contracts). `Check` also
   compiles each side with `Compile`, so `funcdctl push` (`cmd/funcdctl/contract.go:54-57`) and the bundle gate
   (`internal/artifact/bundle.go:209-212`) refuse a side that cannot be checked, an invalid pattern included.
5. **The shims stay unchanged.** A following ADR moves the call-time check to this package and decides where it runs;
   #1339 closes with that ADR's implementation.

## Design model

None: the decision defines formats, a pattern dialect and an error shape, and picks a library; it adds no lifecycle or
concurrency step (ADR-0272).

## Temporary workarounds

The Node and Python shims keep ajv and fastjsonschema, which disagree with Decisions 1 to 3 on the cases in Context.
Exit criterion: the call-time ADR's implementation checks every call with this package and the shims stop checking;
#1339 closes then.

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
// through float64. It returns a fault.Invalid when doc is not JSON.
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
   `UseRegexpEngine` with regexp2/v2 (`ECMAScript|Unicode`, `MatchTimeout` set on each compiled pattern). `date-time`,
   `uuid` and `email` may wrap the engine's built-in checks where the scenario values pass; `uri` adds the ASCII rule;
   `int32` and `int64` are funcd's. The leaves of `ValidationError.BasicOutput()` map to `Detail`.
2. `internal/contract/contract.go`: `Check` calls `Compile` after `walk` and wraps its error as Invalid.
3. `go mod tidy`: regexp2/v2 v2.2.1 moves to the direct block; no new module.
4. Tests: in `internal/contract/rules_test.go`, one test per scenario named `TestScenario` plus the scenario name in
   CamelCase (`TestScenarioDateTimeFollowsRFC3339`, …) with the values above, and `TestValidateConcurrent` under
   `-race`; `TestScenarioPushGatesPatterns` goes through `Check` in `internal/contract/contract_test.go`.
5. `just ci` green. Definition of done: every scenario has one passing test, and no shim, gateway or `CheckInput` code
   changes.

## Review checklist

- [ ] funcd registers the six profile formats with the meanings of Decision 1; `uri` fails any non-ASCII character.
- [ ] Patterns compile with regexp2/v2 `ECMAScript|Unicode` and a `MatchTimeout`; a timed-out match fails the value.
- [ ] `Validate` returns every leaf error as `{path, message}`, `path` an RFC 6901 pointer, sorted by path then message.
- [ ] `Check` compiles each side: `[z-a]` fails it and `^(?!admin$)[a-z]+$` passes it.
- [ ] Each scenario has one named, passing test; `TestValidateConcurrent` passes under `-race`.
- [ ] regexp2/v2 v2.2.1 is direct in `go.mod`; no shim, gateway or `CheckInput` change.

## Consequences

- Positive: each rule has one definition, written by funcd and pinned by tests, so an engine upgrade cannot change a
  verdict unnoticed. Push refuses a pattern that no check could run. The call-time ADR only wires this package.
- Negative: the shims keep disagreeing until the call-time ADR ships (Temporary workarounds). A contract whose pattern
  regexp2 cannot compile now fails push; no example in funcd-functions v0.7.0 has a pattern or a format. Detail
  messages differ from the shims' wording.
- Risks accepted: regexp2's ECMAScript mode follows .NET's reading of ECMA-262 and may differ from a JavaScript engine
  on rare syntax; the scenario tests pin what funcd relies on, and push refuses what regexp2 cannot compile. A
  pathological pattern costs up to the match timeout per match.

## Open questions

- Where the call-time check runs (a per-worker Go front or funcd's gateway), the funcdconfig key for the match
  timeout, and whether a 422 caps `details`: the call-time ADR.
- Whether a failed discriminated `oneOf` lists every variant's errors or only the tagged variant's: the call-time ADR.

## References

- Issue [#1339](https://github.com/pyvvo/funcd/issues/1339) and its decision comment; issue #518 (ADR-0150).
- JSON Schema 2020-12: Core §6.4 (regular expressions: ECMA-262, the `u` flag, not anchored); Validation §6.3.3
  (`pattern`), §7.2 (format as annotation or assertion), §7.3.1, §7.3.2 and §7.3.5 (date-time, email, uri, uuid).
- RFC 3339 §5.6–5.7, RFC 4122 §3, RFC 5321 §4.1.2, RFC 3986 §4.3, RFC 3987, RFC 6901, ECMA-262.
- santhosh-tekuri/jsonschema v6.0.3:
  [compiler.go#L53](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L53) (`AssertFormat`),
  [#L67-L79](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L67-L79) (`RegisterFormat`),
  [#L146](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/compiler.go#L146) (`UseRegexpEngine`),
  [output.go#L149](https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.3/output.go#L149) (`BasicOutput`).
- dlclark/regexp2/v2 v2.2.1 (MIT):
  [options.go#L34-L36](https://github.com/dlclark/regexp2/blob/v2.2.1/options.go#L34-L36) (`ECMAScript`, `Unicode`),
  [regexp.go#L31](https://github.com/dlclark/regexp2/blob/v2.2.1/regexp.go#L31) (safe for concurrent use),
  [regexp.go#L38](https://github.com/dlclark/regexp2/blob/v2.2.1/regexp.go#L38) (`MatchTimeout`). goja compiles
  patterns with the same options (`regexp.go:89-102` at the version in `go.mod`).
- The shims' 422 today: funcd-typescript
  [shim.ts#L129](https://github.com/pyvvo/funcd-typescript/blob/4a1ef558a936fc840c86d65336b304ba7affc1b4/shim/src/shim.ts#L129);
  funcd-python
  [shim.py#L272](https://github.com/pyvvo/funcd-python/blob/b26a0cc9b862e348dae450ad3266f123cccce881/shim/src/funcd_shim/shim.py#L272).
