package gc

// OnStartSwept sets the function Run calls once its start sweep has returned.
func OnStartSwept(c *Collector, f func()) { c.startSwept = f }
