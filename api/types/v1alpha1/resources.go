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
	CPU string `json:"cpu,omitempty" doc:"The CPU cap of the worker or engine it sizes, as a number of cores of at least 0.01, such as 0.5 or 2. Empty takes the default of the resource that declares it, or no cap when there is none. Only the containerd runtime enforces it."`
	// Memory is a size in ByteSize's grammar (ADR-0266), e.g. "256Mi" or "4GiB".
	Memory string `json:"memory,omitempty" doc:"The memory cap of the worker or engine it sizes, such as 256Mi or 4GiB: 1 to 15 digits and an optional binary unit, Ki, Mi, Gi or Ti, with or without a B, above zero. Empty takes the default of the resource that declares it, or no cap when there is none. Only the containerd runtime enforces it, and there a worker that passes it is killed."`
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
