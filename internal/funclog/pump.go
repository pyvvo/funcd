package funclog

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/pyvvo/funcd/api/fault"
)

// Pump drains one instance's Reader into the Sink until the channel closes (io.EOF) or fails, then
// seals the remaining segment. It is loss-safe: a frozen/suspended instance stops sending, so Read
// blocks and the pump pauses — no already-emitted record is dropped. A single bad record
// (fault.Invalid) is skipped (logged), never killing the stream; ctx cancellation stops the pump.
func Pump(ctx context.Context, r Reader, s Sink, res Resource, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	for {
		if err := ctx.Err(); err != nil {
			_, ferr := s.Flush(ctx, res)
			return ferr
		}
		e, err := r.Read(ctx)
		if err != nil {
			if fault.KindOf(err) == fault.Invalid {
				log.WarnContext(ctx, "funclog: skipping unreadable record", "function", res.Function, "error", err)
				continue
			}
			// A failed channel returns the same error on every read: stop, as on EOF (issue #364).
			if !errors.Is(err, io.EOF) {
				log.WarnContext(ctx, "funclog: channel read error", "function", res.Function, "error", err)
			}
			_, ferr := s.Flush(ctx, res) // channel closed or failed (instance gone): seal remaining
			return ferr
		}
		if err := s.Append(ctx, res, e); err != nil {
			log.WarnContext(ctx, "funclog: append failed", "function", res.Function, "error", err)
		}
	}
}
