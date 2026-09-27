package embedded_test

import (
	"testing"

	"github.com/pyvvo/funcd/internal/gateway"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/gateway/gatewaycontract"
)

// scenario: driver-conformance-parity (and the route/strip/404/reprogram
// scenarios it drives) — the embedded reverse-proxy driver satisfies the shared
// gateway contract.
func TestEmbeddedDriverContract(t *testing.T) {
	t.Parallel()
	gatewaycontract.RunContract(t, func(t *testing.T) gateway.Gateway {
		t.Helper()
		return embedded.New()
	})
}
