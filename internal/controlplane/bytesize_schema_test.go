package controlplane_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: byte-size-grammar-in-schema — a quota's maxSize outside the byte-size grammar is refused by the API's
// schema check with 422, which names the grammar (ADR-0323 Decision 4).
func TestScenarioByteSizeGrammarInSchema(t *testing.T) {
	srv := newServer(t)
	body := []byte(`{"apiVersion":"funcd.io/v1alpha1","kind":"KVStore",` +
		`"metadata":{"name":"sizes","namespace":"team-a","resourceGroup":"rg"},"spec":{"quota":{"maxSize":"2GB"}}}`)
	rec := do(t, srv, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/team-a/kvstores", devToken, body)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), v1.ByteSizeGrammar)
}
