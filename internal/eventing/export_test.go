package eventing

import "context"

// PollOnce runs one poll of every registered blob event, for the external test package.
func (w *BlobWatcher) PollOnce(ctx context.Context) { w.poll(ctx) }
