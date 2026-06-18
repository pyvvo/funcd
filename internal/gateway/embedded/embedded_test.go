package embedded_test

import (
	"testing"

	"github.com/green-0-rabbit/funcd/internal/gateway"
	"github.com/green-0-rabbit/funcd/internal/gateway/embedded"
	"github.com/green-0-rabbit/funcd/internal/gateway/gatewaycontract"
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
