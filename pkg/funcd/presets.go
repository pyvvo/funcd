package funcd

// InMemory returns an Option bundle that selects in-memory drivers for every
// port. It is the preset used for e2e testing and development. Each driver
// is wired as its port ADR lands — initially this preset is empty.
func InMemory() Option {
	return func(c *config) error {
		// Drivers arrive with their port ADRs:
		//   c.store = memory.New()
		//   c.blob  = gocloud.New("mem://")
		//   c.bus   = memorybus.New()
		return nil
	}
}

// Production returns an Option bundle that selects production drivers for
// every port. Each driver is wired as its port ADR lands.
func Production() Option {
	return func(c *config) error {
		// Production drivers arrive with their port ADRs.
		return nil
	}
}
