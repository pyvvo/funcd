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
	require.Equal(t, int64(256<<20), n, "256MiB by default (ADR-0294)")
	c.S3Gateway.MaxUploadBytes = 128 << 20
	n, err = c.LakeCatalogMaxBytes()
	require.NoError(t, err)
	require.Equal(t, int64(128<<20), n, "unset, it is the gateway's upload cap when smaller")
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

func TestLakeScratchBytes(t *testing.T) {
	t.Parallel()
	var c Config
	n, err := c.LakeScratchBytes()
	require.NoError(t, err)
	require.Equal(t, int64(1<<30), n, "1GiB by default: twice the 256MiB cap plus 512MiB")
	c.Lake.CatalogMaxSize = "1GiB"
	n, err = c.LakeScratchBytes()
	require.NoError(t, err)
	require.Equal(t, int64(2<<30+512<<20), n, "empty, it follows a larger cap")
	c.Lake.ScratchSize = "3GiB"
	n, err = c.LakeScratchBytes()
	require.NoError(t, err)
	require.Equal(t, int64(3<<30), n)
	for v, msg := range map[string]string{"2GiB": "below twice lake.catalogMaxSize plus 512MiB", "3GB": "lake.scratchSize"} {
		c.Lake.ScratchSize = v
		_, err := c.LakeScratchBytes()
		require.Equal(t, fault.Invalid, fault.KindOf(err), v)
		require.ErrorContains(t, err, msg)
	}
	c.Lake.ScratchSize = "2GiB"
	findings := c.CheckLake()
	require.Len(t, findings, 1)
	require.Contains(t, findings[0].Key, "lake.scratchSize")
}
