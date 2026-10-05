package blob

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
)

// Stat returns the attributes of the object at key through Bucket.Attributes, which reads metadata, never the
// object (ADR-0159). found=false ⇒ no object at key, or a key the driver cannot hold as an object.
func Stat(ctx context.Context, b Bucket, key string) (Attributes, bool, error) {
	a, err := b.Attributes(ctx, key)
	if err == nil {
		return a, true, nil
	}
	if k := fault.KindOf(err); k == fault.NotFound || k == fault.Invalid {
		return Attributes{}, false, nil
	}
	return Attributes{}, false, err
}
