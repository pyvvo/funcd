package gc

import "time"

// OnStartSwept sets the function Run calls once its start sweep has returned.
func OnStartSwept(c *Collector, f func()) { c.startSwept = f }

// WaitForNode sets how long CollectNamespace waits for the worker node that runs the namespace.
func WaitForNode(c *Collector, d time.Duration) { c.nodeCollects = d }
