package badger

import (
	badger "github.com/dgraph-io/badger/v4"

	"github.com/green-0-rabbit/funcd/internal/kvstore"
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
// Open. The seams' background loops (Backup.Ship cadence, CDC.Tail) are started by the caller.
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
	return startDriver(db, cfg), seams, nil
}
