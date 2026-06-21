package main

import (
	badger "github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
)

// openOptions builds Badger options for a named profile. The point of the bench is to compare the
// stock defaults against a deliberately RAM-frugal profile (the funcd lens — RAM is the binding
// constraint) and a pure-in-memory mode, on the SAME workload.
//
//   - "default": badger.DefaultOptions — the out-of-the-box footprint.
//   - "lowmem":  tuned per the Badger memory-usage guide — fewer/smaller memtables, small block/index
//     caches, a modest value-log, compression off (so the block cache is not load-bearing), and small
//     values kept INLINE in the LSM (no value log) since funcd's resources are small blobs.
//   - "inmem":   WithInMemory — everything in RAM, nothing on disk (the upper bound on memory, and the
//     analogue of funcd's existing pure-Go memory engine).
func openOptions(profile, dir string, valThreshold int, sync bool) badger.Options {
	switch profile {
	case "default":
		return badger.DefaultOptions(dir).
			WithLoggingLevel(badger.ERROR).
			WithSyncWrites(sync)
	case "inmem":
		return badger.DefaultOptions("").
			WithInMemory(true).
			WithLoggingLevel(badger.ERROR)
	default: // "lowmem"
		return badger.DefaultOptions(dir).
			WithLoggingLevel(badger.ERROR).
			WithSyncWrites(sync).
			WithNumMemtables(2).
			WithMemTableSize(16 << 20).
			WithNumLevelZeroTables(1).
			WithNumLevelZeroTablesStall(3).
			WithBaseTableSize(8 << 20).
			WithValueLogFileSize(64 << 20).
			WithBlockCacheSize(32 << 20).
			WithIndexCacheSize(32 << 20).
			WithNumCompactors(2).
			WithCompression(options.None).
			WithValueThreshold(int64(valThreshold))
	}
}
