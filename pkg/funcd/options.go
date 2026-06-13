package funcd

import (
	"log/slog"
)

// Option configures the platform. Each With* function returns an Option that
// injects one dependency. Options are validated by New before the platform is
// returned.
type Option func(*config) error

// WithLogger injects the root logger. If not set, slog.Default() is used.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) error {
		c.logger = l
		return nil
	}
}
