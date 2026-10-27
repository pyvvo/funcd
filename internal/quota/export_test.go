package quota

// Usage is s's counted usage: the last measure plus the settled and the reserved bytes; ok=false when not tracked.
func (b *Book) Usage(s Subject) (int64, bool) {
	b.mu.Lock()
	u, ok := b.subjects[s]
	b.mu.Unlock()
	if !ok {
		return 0, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.measured + u.settled + u.reserved, true
}
