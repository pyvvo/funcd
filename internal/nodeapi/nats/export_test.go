package nats

import "time"

// WithPing is cfg with the node API pinging every d, so a test sees a silent link end sooner.
func WithPing(cfg Config, d time.Duration) Config {
	cfg.ping = d
	return cfg
}
