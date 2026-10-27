package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
)

func TestLakeCatalogMaxBytes(t *testing.T) {
	t.Parallel()
	var c Config
	n, err := c.LakeCatalogMaxBytes()
	require.NoError(t, err)
	require.Equal(t, int64(1<<30), n, "1GiB by default")
	c.S3Gateway.MaxUploadBytes = 256 << 20
	n, err = c.LakeCatalogMaxBytes()
	require.NoError(t, err)
	require.Equal(t, int64(256<<20), n, "unset, it is the gateway's upload cap when smaller")
	c.Lake.CatalogMaxSize = "64MiB"
	n, err = c.LakeCatalogMaxBytes()
	require.NoError(t, err)
	require.Equal(t, int64(64<<20), n)
	for v, msg := range map[string]string{"1GiB": "is over s3gateway.maxUploadBytes", "1GB": "lake.catalogMaxSize", "0": "must be above zero"} {
		c.Lake.CatalogMaxSize = v
		_, err := c.LakeCatalogMaxBytes()
		require.Equal(t, fault.Invalid, fault.KindOf(err), v)
		require.ErrorContains(t, err, msg)
	}
}
