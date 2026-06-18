// Package nodejs embeds the funcd Node runtime shim (ADR-0030) into the binary, so the
// single self-contained `funcd` daemon ships it with no sidecar file (ADR-0036). The
// daemon extracts Shim to its data dir on boot and points WithRuntimeShim at it.
package nodejs

import _ "embed"

//go:embed shim.mjs
var Shim []byte
