# ADR-0266: Volume sizes with binary units — `size: 2GiB` beside `sizeBytes`

- **Status**: Implemented (2026-10-23), review passed; Reviewing 2026-10-23, the volumes lane green; Accepted 2026-10-23, under the decider's "don't wait for me … go ahead until completion" of 2026-10-23;
  Proposed 2026-10-23 from the decider's card 7, judged twice: round 1 two Majors (float exactness, the partial
  supersession of a Reviewing ADR), round 2 accept
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: function, volume, api, config
- **Realizes**: [FEAT-0014/F181](../feat/0014-feat-volumes.md)
- **Supersedes in part**: [ADR-0262](0262-function-owned-volumes.md) — Decision 1 (`sizeBytes` required), Decision 5's
  `spec.volumeBudgetBytes` and Decision 3's `volumes.capacityBytes`: each now has a twin with units; the rest of
  ADR-0262 stands. ADR-0262 keeps its own lifecycle (`Reviewing → Implemented`, judged against its own text); its one
  touch is a `Superseded in part by` header line naming these fields

## Context & Need

ADR-0262 writes every volume size as an integer of bytes: `sizeBytes: 1073741824`. A person writing a manifest thinks
in `1GiB`. The decider wants sizes with units on day one, and the byte form kept.

## Scenarios

- `scenario: size-with-units` — Given a volume with `size: 2GiB`, When it is applied, Then it reserves 2147483648 bytes
  and its quota is 2147483648 bytes, as with `sizeBytes: 2147483648`.
- `scenario: one-size-form` — When a volume sets both `size` and `sizeBytes`, or neither, Then apply refuses it, naming
  the volume.
- `scenario: bad-size` — When a volume's size is `2G`, `1.5Gi`, `0`, sixteen digits or past 2⁶³−1 bytes, Then apply
  refuses it, naming the field.
- `scenario: budget-and-capacity-with-units` — Given `volumeBudget: 4Gi` on a namespace and `volumes.capacity: 64GiB`,
  Then they limit volumes exactly as `volumeBudgetBytes: 4294967296` and `volumes.capacityBytes: 68719476736` do.

## Scope

**In**: the size grammar; `size` on a volume, `volumeBudget` on a Namespace, `volumes.capacity` in the daemon config;
the one-form rule for each pair.

**Out**: the other size fields of funcd (`maxObjectBytes`, `maxValueBytes`, `s3gateway.maxUploadBytes`): a later ADR,
if wanted.

## Constraints & Decision drivers

- A manifest stays exact: a size names one byte count, never a rounding.
- The byte form stays valid, so ADR-0262's examples and the integer convention of funcd's other size fields keep working.

## Alternatives considered

1. **Decimal units too (`K`, `M`, `G` = 10³, 10⁶, 10⁹)**: Kubernetes accepts both, but `1G` reads as either 10⁹ or 2³⁰
   to most people. Rejected: binary units only.
2. **Replace `sizeBytes` by `size`**: one field, but it breaks ADR-0262's form and the repo's integer convention.
   Rejected for both forms, exactly one set.
3. **A unit parser of funcd's own**: exact, but a parser to write and test where a library exists. Rejected for
   go-humanize (`github.com/dustin/go-humanize` v1.0.1, MIT, already in the module graph through Badger).
4. **go-humanize's whole grammar**: no pattern, but `2G` would mean 2·10⁹ bytes, and `1.5Gi` and `1 Gi` would pass.
   Rejected: the pattern narrows it to the binary form above.
5. **A number for `size`**: `size: 2147483648` would duplicate `sizeBytes`. Rejected: `size` is a string.

## Decision

1. **The grammar.** A size is a string: a decimal integer of at most 15 digits, optionally followed by a binary unit
   written either way: `Ki`/`KiB` (2¹⁰), `Mi`/`MiB` (2²⁰), `Gi`/`GiB` (2³⁰), `Ti`/`TiB` (2⁴⁰). No unit means bytes. No
   sign, no fraction, no space, no decimal unit; a value past 2⁶³−1 bytes is refused. Fifteen digits stay below 2⁵³, so
   go-humanize's float maths is exact for every count the pattern lets through. The field's pattern holds this form (the
   API schema and `Validate` both check it), and go-humanize's `ParseBytes` computes the byte count: funcd writes no unit
   parser.
2. **The volume.** `spec.volumes[]` sets exactly one of `size` and `sizeBytes`; apply refuses both or neither. Both name
   a size above 0. Everything ADR-0262 does with `sizeBytes` (reservation, quota, the no-resize rule, the backup) uses the
   entry's byte count, whichever form set it: a volume changed from `sizeBytes: 1048576` to `size: 1Mi` is not resized.
3. **The budget and the capacity.** A Namespace sets at most one of `spec.volumeBudget` and `spec.volumeBudgetBytes`, and
   Validate refuses both; the daemon refuses at start a config that sets both `volumes.capacity` and
   `volumes.capacityBytes`, and parses `volumes.capacity` with `ByteSize.Bytes()`. `0` is refused for `size`; for the
   budget and the capacity it means what `0` means for their byte twins (no budget; volumes off).

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/bytesize.go (NEW)

// ByteSize is a size written with an optional binary unit (ADR-0266): "1048576", "512Ki", "2GiB".
type ByteSize string

// Bytes is the size's byte count: Invalid for a string the pattern refuses, else humanize.ParseBytes's count, Invalid
// past math.MaxInt64.
func (s ByteSize) Bytes() (int64, error)

// VolumeEntry gains:
//	Size ByteSize `json:"size,omitempty" pattern:"^[0-9]{1,15}((Ki|Mi|Gi|Ti)B?)?$"`
// and SizeBytes becomes optional: `json:"sizeBytes,omitempty" minimum:"1"`.

// Bytes is the entry's size in bytes, from whichever form is set (0 when neither is valid).
func (e VolumeEntry) Bytes() int64

// NamespaceSpec gains:
//	VolumeBudget ByteSize `json:"volumeBudget,omitempty" pattern:"^[0-9]{1,15}((Ki|Mi|Gi|Ti)B?)?$"`

// internal/platform/config: Volumes gains
//	Capacity string `json:"capacity,omitempty" env:"FUNCD_VOLUMES_CAPACITY"` // cmd/funcd: v1.ByteSize(Capacity).Bytes()
```

| Consumes | Exposes |
|---|---|
| ADR-0262's `spec.volumes`, Namespace budget and `volumes.capacityBytes` | `size`, `volumeBudget`, `volumes.capacity`, `v1.ByteSize` |
| `github.com/dustin/go-humanize` v1.0.1 (MIT), now a direct dependency | — |

## Implementation plan

1. `api/types/v1alpha1/bytesize.go`: `ByteSize`, `Bytes()` (the pattern, then `humanize.ParseBytes`); go-humanize
   becomes a direct requirement in `go.mod` at the version the graph holds; `VolumeEntry.Size` and `Bytes()`; `validateVolumes` with
   the one-form rule; `NamespaceSpec.VolumeBudget` and its one-form rule in `Namespace.Validate`; OpenAPI regenerated.
2. Every reader of `SizeBytes` reads `VolumeEntry.Bytes()`, and the budget reader `NamespaceSpec`'s byte count.
3. `internal/platform/config` and `cmd/funcd`: `volumes.capacity`, refused beside `volumes.capacityBytes`.
4. Tests: one `TestScenario<Name>` per scenario with `// scenario: <name>`; a table of grammar cases.

**Definition of done**: the tests pass; the gate passes; no username or absolute path in a changed file.

## Review checklist

- [ ] `1Ki` = `1KiB` = 1024; `2G`, `1.5Gi`, `-1`, `1 Gi`, `0` for `size` and a value past 2⁶³−1 are refused.
- [ ] A volume, a Namespace and the daemon config each refuse both forms; a volume refuses neither.
- [ ] No code reads `VolumeEntry.SizeBytes` directly outside `VolumeEntry.Bytes()`.
- [ ] A 16-digit count is refused; `999999999999999` with every unit is exact or refused past 2⁶³−1, never rounded.
- [ ] One test per scenario, named for it.

## Consequences

- A manifest writes `size: 2GiB`; the byte form keeps working.
- Volumes are funcd's first size fields with units; the others stay integers of bytes until a later ADR.

## Open questions

- Units for the other size fields: a later ADR, if the decider wants them.

## References

- [ADR-0262](0262-function-owned-volumes.md); FEAT-0014 F181; feature issue
  [#1187](https://github.com/pyvvo/funcd/issues/1187).
