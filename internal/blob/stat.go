package blob

import "context"

// Stat returns the attributes of the object at key via a List keyed by that exact key: the port has no
// per-key Stat (ADR-0007), and a listing reads metadata, never the object. found=false ⇒ no object at key.
func Stat(ctx context.Context, b Bucket, key string) (Attributes, bool, error) {
	items, err := b.List(ctx, key)
	if err != nil {
		return Attributes{}, false, err
	}
	for i := range items {
		if items[i].Key == key {
			return items[i], true, nil
		}
	}
	return Attributes{}, false, nil
}
