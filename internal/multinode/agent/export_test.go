package agent

// ResyncForTest makes the next poll start from a whole snapshot, as after a revision the change log no longer holds.
func (a *Agent) ResyncForTest() { a.resync.Store(true) }
