package blob

import "time"

// NewResolverTTL and CacheLen let the external tests drive the resolver's TTL cache (issue #170).
func NewResolverTTL(r MetaReader, ttl time.Duration) (BindingResolver, error) {
	br, err := NewResolver(r)
	if err != nil {
		return nil, err
	}
	br.(*metaResolver).ttl = ttl
	return br, nil
}

func CacheLen(r BindingResolver) int {
	m := r.(*metaResolver)
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.cache)
}
