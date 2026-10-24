package artifact

import "github.com/pyvvo/funcd/internal/platform/durable"

// TraceDisk reports each step of m's durable cache writes to trace (#1244).
func TraceDisk(m *OrasMaterializer, trace func(durable.Op, string)) { m.disk.Trace = trace }
