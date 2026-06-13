package funcd

import (
	"context"
	"errors"
	"log/slog"
)

// Platform is the assembled funcd runtime — the composition root built by New.
type Platform struct {
	cfg    *config
	logger *slog.Logger
}

// New assembles the platform from the given options. It validates that all
// required dependencies are present and returns a typed fault.Invalid if any
// are missing — never a partial platform, never a panic.
func New(opts ...Option) (*Platform, error) {
	cfg := &config{}
	for _, o := range opts {
		if err := o(cfg); err != nil {
			return nil, err
		}
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Platform{cfg: cfg, logger: logger}, nil
}

// Run starts the platform and blocks until ctx is cancelled or a fatal error
// occurs. It returns nil on graceful shutdown.
func (p *Platform) Run(ctx context.Context) error {
	return errors.New("not implemented")
}

// Shutdown gracefully stops the platform, draining in-flight work.
func (p *Platform) Shutdown(ctx context.Context) error {
	return errors.New("not implemented")
}

// config holds the assembled dependencies — validated by validate() before the
// platform is returned.
type config struct {
	logger *slog.Logger
}

func (c *config) validate() error {
	// No required dependencies yet — they arrive with later ADRs.
	// The pattern is:
	//   if c.store == nil { return fault.Invalidf("funcd.New", "store is required") }
	return nil
}
