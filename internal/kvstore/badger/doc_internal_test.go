package badger

import (
	"reflect"
	"strconv"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	platformconfig "github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// The kvstore keys' descriptions copy the backup's chunk size and prefix and the change feed's retention, so a changed
// one fails here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	cfg := reflect.TypeFor[platformconfig.Config]()
	mib := strconv.FormatFloat(float64(defaultChunkBytes)/(1<<20), 'f', -1, 64) + " MiB"
	doctag.NamesAt(t, cfg, "kvstore.backup.chunkBytes", strconv.Itoa(defaultChunkBytes), "means "+mib)
	doctag.NamesAt(t, cfg, "kvstore.backup.target", KVPrefix)
	doctag.NamesAt(t, cfg, "kvstore.cdc.retention", v1.Duration(defaultCDCRetention).String())
}
