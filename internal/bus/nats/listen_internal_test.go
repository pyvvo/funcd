package nats

import (
	"context"
	"testing"
)

// The bus is reached only in process: the embedded server opens no network listener in either storage mode,
// so no client off the daemon can subscribe, publish or call the JetStream API (ADR-0008).
func TestOpenDoesNotListenOnTheNetwork(t *testing.T) {
	for name, opts := range map[string]Options{
		"memory": {Storage: MemoryStorage},
		"file":   {Storage: FileStorage, StoreDir: t.TempDir()},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := Open(context.Background(), opts)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			if addr := b.(*embedded).srv.Addr(); addr != nil {
				t.Fatalf("bus server listens on %s, want no network listener", addr)
			}
		})
	}
}
