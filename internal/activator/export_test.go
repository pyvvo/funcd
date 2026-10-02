package activator

// TrackedFunctions reports how many functions the last-activity tracker holds.
func (a *Activator) TrackedFunctions() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.lastActive)
}
