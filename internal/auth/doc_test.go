package auth

import (
	"reflect"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/testkit/doctag"
)

// A Role's and a Policy's descriptions give these actions as examples, and a KV table's names the write action, so a
// renamed one fails here until they follow (ADR-0323 Decision 4).
func TestDocNamesTheConstant(t *testing.T) {
	doctag.Names(t, reflect.TypeFor[v1.KVTable](), "Owner", string(ActionKVWrite))
	doctag.Names(t, reflect.TypeFor[v1.RoleSpec](), "Actions", string(ActionS3Read), string(ActionKVWrite), string(ActionLakeQuery))
	doctag.Names(t, reflect.TypeFor[v1.PolicySpec](), "Cedar", string(ActionKVRead))
}
