package aesgcm

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/pyvvo/funcd/internal/platform/config"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// secrets.encryptionKeyFile names the key's length, in bytes and in the cipher's name, so a changed one fails here
// until it follows (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.NamesAt(t, reflect.TypeFor[config.Config](), "secrets.encryptionKeyFile", strconv.Itoa(keyLen), "AES-"+strconv.Itoa(8*keyLen))
}
