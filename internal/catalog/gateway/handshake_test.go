package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// goldenToken is the token embedded in testdata/handshake_golden.bin (a captured Quack handshake frame).
const goldenToken = "GOLDENTOKEN013"

// TestSwapHandshakeToken round-trips the captured Quack handshake frame (ADR-0137 spike fixture): it
// extracts the caller token, swaps it for a longer AND a shorter token (the length-prefix fixup), and
// confirms a non-handshake body yields ok=false (fail-closed, forwarded unchanged).
func TestSwapHandshakeToken(t *testing.T) {
	t.Parallel()
	golden, err := os.ReadFile(filepath.Join("testdata", "handshake_golden.bin"))
	require.NoError(t, err)

	// extract: the golden frame's token field is decoded to the captured token.
	_, caller, ok := swapHandshakeToken(golden, "ignored")
	require.True(t, ok, "the golden frame is a token-bearing handshake")
	require.Equal(t, goldenToken, caller, "the token field decodes to the captured token")

	// swap to a LONGER token and back — a different-length token round-trips via the length-prefix fixup.
	longer := "A-MUCH-LONGER-REPLACEMENT-ENGINE-TOKEN"
	rewritten, caller2, ok := swapHandshakeToken(golden, longer)
	require.True(t, ok)
	require.Equal(t, goldenToken, caller2, "extraction is unaffected by the replacement length")
	_, back, ok := swapHandshakeToken(rewritten, "x")
	require.True(t, ok, "the rewritten frame is still a valid handshake")
	require.Equal(t, longer, back, "the swapped-in longer token is what the engine now sees")

	// swap to a SHORTER token — the trailing fields must be preserved after the shorter value.
	shorter := "s"
	rewritten2, _, ok := swapHandshakeToken(golden, shorter)
	require.True(t, ok)
	_, back2, ok := swapHandshakeToken(rewritten2, "x")
	require.True(t, ok)
	require.Equal(t, shorter, back2)
	// the bytes AFTER the token field (the opaque trailing fields) survive the shorter swap verbatim.
	origTail := golden[preambleLen+3+len(goldenToken):]
	newTail := rewritten2[preambleLen+3+len(shorter):]
	require.Equal(t, origTail, newTail, "opaque trailing fields are preserved across a length-changing swap")

	// a >=128-byte token (every JWT is) uses a 2-BYTE LEB128 varint length — the case the 14-byte golden
	// fixture cannot cover and the live dev demo caught (a fixed-width parse mis-reads the varint's 2nd byte
	// as token data). Build it with makeHandshake (the real varint framing) and round-trip it.
	longTok := strings.Repeat("j", 200) // > 127 ⇒ a 2-byte varint length
	longFrame := makeHandshake(longTok)
	_, gotLong, ok := swapHandshakeToken(longFrame, "x")
	require.True(t, ok, "a handshake with a >=128-byte (2-byte-varint) token parses")
	require.Equal(t, longTok, gotLong, "the >=128-byte token decodes exactly via the varint length, not a fixed width")

	// the longest Identity catalog token (a 63/63 owner) round-trips both ways.
	maxTok := IdentityCatalogToken(v1.NamespaceName(strings.Repeat("n", 63)), v1.ObjectName(strings.Repeat("o", 63)), strings.Repeat("r", randomPartLen))
	require.Len(t, maxTok, 255)
	_, gotMax, ok := swapHandshakeToken(makeHandshake(maxTok), "x")
	require.True(t, ok, "a handshake with a 255-byte token parses")
	require.Equal(t, maxTok, gotMax)
	swappedMax, _, ok := swapHandshakeToken(golden, maxTok)
	require.True(t, ok)
	_, backMax, ok := swapHandshakeToken(swappedMax, "x")
	require.True(t, ok)
	require.Equal(t, maxTok, backMax, "a 255-byte token swapped in is what the engine sees")

	// a non-handshake body ⇒ ok=false, body returned unchanged (fail-closed).
	notHandshake := []byte("not-a-handshake")
	out, caller3, ok := swapHandshakeToken(notHandshake, "engine")
	require.False(t, ok, "a body that is not the token-bearing handshake is not swapped")
	require.Empty(t, caller3)
	require.Equal(t, notHandshake, out, "a non-handshake body is returned unchanged for opaque forwarding")
}
