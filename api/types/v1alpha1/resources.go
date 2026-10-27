package v1alpha1

import (
	"math"
	"strconv"
	"strings"

	"github.com/pyvvo/funcd/api/fault"
)

// ResourceSpec sizes one worker (ADR-0291): a solo Function's, or the engine of a Lake or a SQLDatabase. Validate is
// its one check and runtime.LimitsFrom its one mapping onto the worker's limits, which the runtime driver enforces.
type ResourceSpec struct {
	// CPU is a number of cores, at least MinCPU (e.g. "0.5" or "2").
	CPU string `json:"cpu,omitempty"`
	// Memory is a size in ByteSize's grammar (ADR-0266), e.g. "256Mi" or "4GiB".
	Memory string `json:"memory,omitempty"`
}

// MinCPU is the smallest cpu a ResourceSpec takes: a hundredth of a core keeps the CFS quota above the kernel's floor.
const MinCPU = 0.01

// Validate refuses a cpu that is not a finite number of cores of at least MinCPU, or a memory that is not a positive
// ByteSize (ADR-0291 Decision 2); path names the field in the error.
func (r ResourceSpec) Validate(op, path string) error {
	if c := strings.TrimSpace(r.CPU); c != "" {
		if n, err := strconv.ParseFloat(c, 64); err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < MinCPU {
			return fault.Invalidf(op, "%s.cpu %q is not a number of cores of at least %g", path, r.CPU, MinCPU)
		}
	}
	if m := strings.TrimSpace(r.Memory); m != "" {
		if n, err := ByteSize(m).Bytes(); err != nil || n == 0 {
			return fault.Invalidf(op, "%s.memory %q is not a size such as 64Mi or 1GiB", path, r.Memory)
		}
	}
	return nil
}
