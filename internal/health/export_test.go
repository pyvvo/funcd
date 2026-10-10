package health

import "context"

// ProbeAll runs one probe of every target, as one tick of Start does.
func ProbeAll(ctx context.Context, p *Prober) { p.probeAll(ctx) }
