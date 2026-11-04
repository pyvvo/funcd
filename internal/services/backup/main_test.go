package backup

import (
	"testing"

	"github.com/pyvvo/funcd/internal/testkit/s3stub"
)

func TestMain(m *testing.M) { s3stub.Main(m) }
