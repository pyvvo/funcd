package blob

import (
	"time"

	"github.com/pyvvo/funcd/internal/services"
)

// NewResolverTTL and CacheLen let the external tests drive the resolver's TTL cache (issue #170).
func NewResolverTTL(r MetaReader, ttl time.Duration) (BindingResolver, error) {
	br, err := NewResolver(r)
	if err != nil {
		return nil, err
	}
	br.(*metaResolver).cache = services.NewBindingCache[Binding](ttl)
	return br, nil
}

func CacheLen(r BindingResolver) int {
	return r.(*metaResolver).cache.Len()
}
