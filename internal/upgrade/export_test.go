package upgrade

// WithVersion sets the installed version Run compares against, for tests that do not stamp the binary.
func WithVersion(o Options, v string) Options {
	o.version = v
	return o
}

// ParseVersion, ExecStartPath and Swap are the step helpers, for their unit tests.
func ParseVersion(bin string, out []byte) (string, error) { return parseVersion(bin, out) }

func ExecStartPath(out []byte) string { return execStartPath(out) }

func Swap(newBin, self string) error { return swap(newBin, self) }
