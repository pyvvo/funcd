# ADR-0300: The event store's directory and config key are named `eventstore`

- **Status**: Accepted (2026-10-29; adr-judge round 1 folded in and a last look passed; accepted under the decider's
  delegation of 2026-10-29, "batch them", after they chose the names and confirmed that no workload runs anywhere)
- **Date**: 2026-10-29
- **Deciders**: green-0-rabbit
- **Tags**: eventing, config, rename, disaster-recovery, breaking-change
- **Realizes**: [FEAT-0009/F217](../feat/0009-feat-disaster-recovery.md)
- **Supersedes in part**: [ADR-0201](0201-event-store.md) Decision 1's last sentence, which kept the event store's
  directory at `<dataDir>/deadletter`, and Decision 7, which kept its key at `eventing.deadletter.dataDir`; the
  dead-letter keys Decision 7 kept stand
- **Relates to**: [ADR-0118](0118-eventing-dead-letter-queue.md) (the dead-letter keys) ·
  [ADR-0206](0206-restore-and-held-boot.md) and [ADR-0207](0207-pre-upgrade-snapshot-and-safe-mode.md) (the restore
  run, the pre-upgrade snapshot and safe mode name the store directories; `funcd upgrade`) ·
  [ADR-0235](0235-sql-change-feed.md) (the change feed's watermarks, a tenant of the store) ·
  [ADR-0261](0261-rename-catalogservice-to-lake.md) (a rename without compatibility)

## Context & Need

The event store (ADR-0201) is one Badger instance with three tenants: the dead letters (`dl/`, ADR-0118), the blob
seen lists (`seen/`) and the change feed's watermarks (`cdc/`, ADR-0235). ADR-0201 kept its directory at
`<storage.dataDir>/deadletter` (Decision 1) and its key at `eventing.deadletter.dataDir` (Decision 7), defaults the
decider did not confirm. That name covers one tenant of three. An operator who moves the store directories aside, as
the restore runbook and the safe-mode message tell them to, reads "the dead-letter directory" and cannot tell that the
seen lists and the watermarks live there too.

The decider chose on 2026-10-28 the directory `eventstore` and the key `eventing.eventstore.dataDir`, in a PR of its
own after the workload-limits batch. No funcd node is deployed (the decider, 2026-10-29), so the rename carries no
migration and no alias, as ADR-0261 did. Purpose: the directory and its key name what they hold, in the config, the
runbooks and every message that names the store directories.

## Scenarios

- **scenario: eventstore-default-dir**: *Given* a daemon in `storage.mode: file` whose config sets no event store key,
  *When* it starts, *Then* the event store opens `<storage.dataDir>/eventstore`, and no `deadletter` directory exists.
- **scenario: eventstore-key**: *Given* `eventing.eventstore.dataDir: <dir>` in the config file, or
  `FUNCD_EVENTING_EVENTSTORE_DATA_DIR=<dir>`, *When* funcd starts, *Then* the event store opens `<dir>`.
- **scenario: old-key-refused**: *Given* a config file that sets `eventing.deadletter.dataDir`, *When* funcd starts,
  *Then* it fails on the unknown key.
- **scenario: deadletter-keys-kept**: *Given* `eventing.deadletter.retention` and `eventing.deadletter.maxEntries` in
  the config file, *When* funcd loads it, *Then* both still apply to the dead letters.
- **scenario: messages-name-eventstore**: *Given* a restore into a non-empty event store directory, a pre-upgrade
  snapshot while the event store is open, or a safe-mode stop, *When* funcd reports it, *Then* the message names
  `eventing.eventstore.dataDir`.

## Scope

**In**: the default directory name, the key and its environment variable, the config's doc comment, the messages that
name the key (the restore run, the pre-upgrade snapshot's lock probe, safe mode), `examples/funcdconfig.yaml`,
`examples/restore-runbook.md`, the upgrade note in `docs/install.md`, and the Badger bench's directory name.

**Out**: Go packages and identifiers (`internal/eventing/deadletter` is the dead-letter tenant and keeps its name;
`funcd.WithDeadLetterQueue` keeps its signature); `eventing.deadletter.retention` and `maxEntries`; the store's
content and format; moving an existing `deadletter` directory by code.

## Constraints & Decision drivers

- No node is deployed (the decider, 2026-10-29): no migration and no alias.
- `config.Load` refuses an unknown key (`yaml.UnmarshalStrict`), so the old key fails loudly.
- `funcd upgrade` stops the unit, writes the pre-upgrade generation, swaps the binary and starts the unit in one
  command (ADR-0207); with `--unit ""` it stops and starts nothing.
- One name for the store in the config, the messages and the docs.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. `eventstore` and `eventing.eventstore.dataDir`, no alias** | Names the store; the old key fails loudly | An upgrade must move the directory by hand; a first start on an empty event store fires every blob EventSource's objects again, and the change feed publishes again any group the engine resends | **Chosen** (the decider) |
| B. Keep `deadletter` (ADR-0201) | Nothing changes | Names one tenant of three | Rejected |
| C. `eventing.store.dataDir`, with `eventing.deadletter.dataDir` kept as an alias (ADR-0201's alternative) | Old config files keep working | Two keys for one directory, for no deployed node | Rejected |
| D. Move `deadletter` to `eventstore` at start | An upgrade needs no step by hand | Migration code for no deployed node | Rejected |

## Decision

Decision 1 replaces the last sentence of ADR-0201 Decision 1; Decisions 2 and 3 replace ADR-0201 Decision 7;
Decisions 4 and 5 are new.

1. The event store's directory defaults to `<storage.dataDir>/eventstore`.
2. Its key is `eventing.eventstore.dataDir`, with the environment variable `FUNCD_EVENTING_EVENTSTORE_DATA_DIR`. A set
   value overrides the default, and `storage.mode: memory` keeps the store in memory, as before.
   `eventing.deadletter.dataDir` and `FUNCD_EVENTING_DEADLETTER_DATA_DIR` are removed, with no alias: a config file
   that sets the key fails to load, and the environment variable is no longer read.
3. `eventing.deadletter.retention` and `eventing.deadletter.maxEntries` keep their names: they govern dead letters only.
4. The restore run's directory checks, the pre-upgrade snapshot's lock probe and the safe-mode message name
   `eventing.eventstore.dataDir`; the runbook, the example config and the install guide name `eventstore`.
5. Nothing moves an existing `deadletter` directory. `docs/install.md` gives the upgrade order: stop the unit; run
   `funcd upgrade --unit ""` with the installed binary, which writes the pre-upgrade generation from `deadletter` and
   swaps the binary; move `<dataDir>/deadletter` to `<dataDir>/eventstore`, and rename the key in the config file if
   it is set; start the unit. A rollback to an earlier binary needs the old key back, since that binary refuses
   `eventing.eventstore.dataDir`.

## Design model

None: a rename of a directory and a config key, with no state, transition or protocol change (ADR-0272 Decision 1).

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/config/config.go, in Config.Eventing: Deadletter loses DataDir; EventStore is new.
Deadletter struct {
	Retention  string `json:"retention,omitempty" env:"FUNCD_EVENTING_DEADLETTER_RETENTION"`
	MaxEntries int    `json:"maxEntries,omitempty" env:"FUNCD_EVENTING_DEADLETTER_MAX_ENTRIES" validate:"min=0"`
} `json:"deadletter,omitempty"`
EventStore struct {
	// DataDir is the event store's Badger directory (ADR-0201, ADR-0300). Empty ⇒ <Storage.DataDir>/eventstore in
	// Load(); ignored (in memory) when Storage.Mode is memory.
	DataDir string `json:"dataDir,omitempty" env:"FUNCD_EVENTING_EVENTSTORE_DATA_DIR"`
} `json:"eventstore,omitempty"`
```

```yaml
eventing:
  eventstore:
    dataDir: /var/lib/funcd/data/eventstore
  deadletter:
    retention: 720h
    maxEntries: 1000
```

| Consumes | Exposes |
|---|---|
| `eventing.eventstore.dataDir`, `FUNCD_EVENTING_EVENTSTORE_DATA_DIR`, `storage.dataDir`, `storage.mode` | The event store at that directory; messages that name the key |

## Implementation plan

- `internal/platform/config/config.go`: the struct above, the default `<Storage.DataDir>/eventstore`, the doc comment.
- `cmd/funcd/main.go`, `internal/restore/run.go`, `internal/upgrade/upgrade.go`, `internal/safemode/safemode.go`:
  read `Eventing.EventStore.DataDir` and name `eventing.eventstore.dataDir`.
- `bench/badger/topology.go`: `open("eventstore")`.
- `examples/funcdconfig.yaml`, `examples/restore-runbook.md` (both places that name the directories), and
  `docs/install.md` (the upgrade order of Decision 5).
- Tests that build store directories by name: `cmd/funcd/restore_test.go`, `cmd/funcd/upgrade_test.go`,
  `pkg/funcd/backup_internal_test.go` and `pkg/funcd/eventstore_internal_test.go` use `eventstore`.
- No `go.mod` change.

Test plan, one named test per scenario: `TestScenarioEventStoreDefaultDir`, `TestScenarioEventStoreKey` (file and
environment), `TestScenarioOldEventStoreKeyRefused`, `TestScenarioDeadLetterKeysKept` (config package);
`messages-name-eventstore` by `TestScenarioRestoreNamesEventStoreKey` (cmd/funcd),
`TestScenarioUpgradeProbeNamesEventStoreKey` (internal/upgrade) and `TestScenarioSafeModeNamesEventStoreKey`
(internal/safemode).

Done when build, vet, lint and the touched packages' tests pass, and a search of the code, `examples/` and
`docs/install.md` for `eventing.deadletter.dataDir`, `DEADLETTER_DATA_DIR` and a `deadletter` directory finds only
the upgrade note's move.

## Review checklist

- [ ] The default directory is `<storage.dataDir>/eventstore`; the key and its environment variable override it.
- [ ] A config file that sets `eventing.deadletter.dataDir` fails to load.
- [ ] `eventing.deadletter.retention` and `maxEntries` still load and apply.
- [ ] The restore run, the pre-upgrade snapshot's lock probe and safe mode name `eventing.eventstore.dataDir`.
- [ ] The runbook and the example config name `eventstore`; `docs/install.md` gives Decision 5's upgrade order.
- [ ] Package names and `WithDeadLetterQueue` are unchanged.
- [ ] Each scenario has a named, passing test.

## Consequences

- One name for the store in the config, the runbooks and the messages.
- Breaking: a config file that sets `eventing.deadletter.dataDir` fails to load, and
  `FUNCD_EVENTING_DEADLETTER_DATA_DIR` is ignored. An upgrade that skips the move starts on an empty event store: its
  dead letters are gone, every blob EventSource fires again for each object under its prefixes, and the change feed,
  without its watermarks, publishes again any group the engine resends: the effect ADR-0201 rejected for its own
  migration. No node is deployed today.

## Open questions

None.

## References

- ADR-0201 Decisions 1 and 7, and its alternatives, which listed `eventing.store.dataDir` with an alias.
- The decider's choices: 2026-10-28 (the names, a PR of its own) and 2026-10-29 (no node is deployed).
- `config.Load` reads the file with `yaml.UnmarshalStrict` (`internal/platform/config/config.go`); `funcd upgrade`
  with `--unit ""` runs no systemctl (`internal/upgrade/upgrade.go`, `Options.Unit`).
- adr-judge round 1 (coworker-2, 2026-10-29): the upgrade order, what an empty store costs, the Design model section,
  the two superseded parts, the probe's test, the test files.
