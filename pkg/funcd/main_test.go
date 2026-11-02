//go:build !e2e

package funcd_test

import (
	"os"
	"testing"
)

// The e2e build has its own TestMain, in app_revision_e2e_test.go, which ends the same way.
func TestMain(m *testing.M) {
	os.Exit(leftOpen(m.Run()))
}
