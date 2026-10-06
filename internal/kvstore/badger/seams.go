package badger

import (
	badger "github.com/dgraph-io/badger/v4"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/kvstore"
)

// Seams holds the optional DR + CDC the daemon wires when configured (ADR-0067/0068). A nil field means
// that seam is off — the base driver then writes no change-log and runs no DR loop.
type Seams struct {
	Backup Backup // nil unless kvstore.backup.enabled
	CDC    CDC    // nil unless kvstore.cdc.enabled
}

// OpenWithSeams opens the durable KV driver (ADR-0066) and wires the opt-in DR/CDC seams to the SAME
// Badger instance. It resolves the bootstrap order the per-seam constructors imply — NewBackup/NewCDC
// need the *badger.DB that Open creates — by opening the db first, building each seam (when its builder
// is non-nil) over that db, then starting the gateway with the seams already attached (so CDC's OnWrite
// fires on the very first write, with no attach-after-start race). With both builders nil it is exactly
// Open. Without a backup seam it drops the backup cursor, so the next backup re-baselines (#808). The seams'
// background loops (Backup.Ship cadence, CDC.Tail) are started by the caller.
func OpenWithSeams(
	dir string,
	buildBackup func(*badger.DB) (Backup, error),
	buildCDC func(*badger.DB) (CDC, error),
	opts ...Option,
) (kvstore.KV, Seams, error) {
	cfg := newConfig(opts)
	db, err := openDB(dir, cfg.sync)
	if err != nil {
		return nil, Seams{}, err
	}
	var seams Seams
	if buildCDC != nil {
		c, err := buildCDC(db)
		if err != nil {
			_ = db.Close()
			return nil, Seams{}, err
		}
		seams.CDC, cfg.cdc = c, c
	}
	if buildBackup != nil {
		b, err := buildBackup(db)
		if err != nil {
			_ = db.Close()
			return nil, Seams{}, err
		}
		seams.Backup, cfg.backup = b, b
	}
	if cfg.backup == nil {
		if err := dropBackupCursor(db); err != nil {
			_ = db.Close()
			return nil, Seams{}, err
		}
	}
	return startDriver(db, cfg), seams, nil
}

// OpenWithSeamsFor is the daemon-facing opener: it wires the opt-in seams from plain values, hiding the
// *badger.DB. A non-nil bucket enables DR backup (ADR-0067); a non-nil sink enables CDC (ADR-0068). Start
// the seams' loops with RunBackup / RunCDC on the returned Seams.
func OpenWithSeamsFor(
	dir string,
	bucket blob.Bucket, bcfg BackupConfig,
	sink bus.Bus, ccfg CDCConfig,
	opts ...Option,
) (kvstore.KV, Seams, error) {
	var buildBackup func(*badger.DB) (Backup, error)
	if bucket != nil {
		buildBackup = func(db *badger.DB) (Backup, error) { return NewBackup(db, bucket, bcfg) }
	}
	var buildCDC func(*badger.DB) (CDC, error)
	if sink != nil {
		buildCDC = func(db *badger.DB) (CDC, error) { return NewCDC(db, sink, ccfg) }
	}
	return OpenWithSeams(dir, buildBackup, buildCDC, opts...)
}
