package config

import (
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The default of functions.defaultResources (ADR-0291 Decision 3).
const (
	DefaultFunctionCPU    = "1"
	DefaultFunctionMemory = "512Mi"
)

// FunctionDefaultResources is functions.defaultResources, 1 CPU and 512Mi unless set, an empty field setting no default;
// fault.Invalid naming the key for a value ResourceSpec.Validate refuses.
func (c Config) FunctionDefaultResources() (v1.ResourceSpec, error) {
	r := v1.ResourceSpec{CPU: c.Functions.DefaultResources.CPU, Memory: c.Functions.DefaultResources.Memory}
	if err := r.Validate("config.FunctionDefaultResources", `config key "functions.defaultResources"`); err != nil {
		return v1.ResourceSpec{}, err
	}
	return r, nil
}

// CheckFunctions refuses a functions.defaultResources that FunctionDefaultResources refuses.
func (c Config) CheckFunctions() []Finding {
	var f findings
	if _, err := c.FunctionDefaultResources(); err != nil {
		f.refuse("functions.defaultResources", "%v", err)
	}
	return f.errs
}
