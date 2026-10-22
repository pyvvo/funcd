package controlplane_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestScenario_lake_kind_registered covers scenario: lake-kind-registered (ADR-0261) at the API: Lakes list under
// /lakes, the old /catalogservices route is gone, and a Function's old bindings.catalog is refused at apply, naming the
// field.
func TestScenario_lake_kind_registered(t *testing.T) {
	t.Parallel()
	srv := newServer(t)
	base := "/apis/funcd.io/v1alpha1/namespaces/team-a/"
	rec := do(t, srv, http.MethodGet, base+"lakes", devToken, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = do(t, srv, http.MethodGet, base+"catalogservices", devToken, nil)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	fn := `{"apiVersion":"funcd.io/v1alpha1","kind":"Function",` + bindingsMeta +
		`,"spec":{"runtime":"nodejs22","handler":"handle","image":"oci:x","bindings":{"catalog":[{"alias":"a","catalog":"c"}]}}}`
	rec = do(t, srv, http.MethodPost, base+"functions", devToken, []byte(fn))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "catalog")
}
