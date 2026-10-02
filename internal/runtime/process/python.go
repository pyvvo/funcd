package process

import (
	"context"
	"os/exec"
	"strings"
)

// PythonShimLoadError imports the extracted python shim (shimDir holds funcd_shim) with the interpreter,
// so a caller registers only a python that can run it (ADR-0049, ADR-0123); the probe checks what the shim
// really needs instead of restating it. "" ⇒ it loads; otherwise the interpreter's last output line (e.g.
// the SyntaxError or ModuleNotFoundError).
func PythonShimLoadError(ctx context.Context, python, shimDir string) string {
	out, err := exec.CommandContext(ctx, python, "-c", "import sys; sys.path.insert(0, sys.argv[1]); import funcd_shim.shim", shimDir).CombinedOutput()
	if err == nil {
		return ""
	}
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return msg[strings.LastIndexByte(msg, '\n')+1:]
	}
	return err.Error()
}
