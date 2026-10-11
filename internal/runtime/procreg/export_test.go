package procreg

import "os"

// LockFile is the registry's lock, for the tests that copy its descriptor.
func LockFile(r *Registry) *os.File { return r.lock }
