package funclog

import (
	"context"
	"errors"
	"io"
	"log/slog"
)

// Pump drains one instance's Reader into the Sink until the channel closes (io.EOF), then seals
// the remaining segment. It is loss-safe: a frozen/suspended instance stops sending, so Read
// blocks and the pump pauses — no already-emitted record is dropped. A single malformed line is
// skipped (logged), never killing the stream; ctx cancellation stops the pump.
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
		switch {
		case errors.Is(err, io.EOF):
			_, ferr := s.Flush(ctx, res) // channel closed (instance gone): seal remaining
			return ferr
		case err != nil:
			log.WarnContext(ctx, "funclog: skipping unreadable record", "function", res.Function, "error", err)
			continue
		}
		if err := s.Append(ctx, res, e); err != nil {
			log.WarnContext(ctx, "funclog: append failed", "function", res.Function, "error", err)
		}
	}
}
