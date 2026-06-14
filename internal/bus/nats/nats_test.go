package nats_test

import (
	"context"
	"testing"
	"time"

	"github.com/green-0-rabbit/funcd/internal/bus"
	"github.com/green-0-rabbit/funcd/internal/bus/buscontract"
	natsdriver "github.com/green-0-rabbit/funcd/internal/bus/nats"
)

// scenario: driver-conformance-parity — the embedded-NATS driver passes the
// identical bus contract against both memory and file JetStream storage.
func TestScenario_DriverConformanceParity(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		buscontract.RunContract(t, func(t *testing.T) bus.Bus {
			b, err := natsdriver.Open(context.Background(), natsdriver.Options{Storage: natsdriver.MemoryStorage})
			if err != nil {
				t.Fatalf("open memory bus: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			return b
		})
	})
	t.Run("file", func(t *testing.T) {
		buscontract.RunContract(t, func(t *testing.T) bus.Bus {
			b, err := natsdriver.Open(context.Background(), natsdriver.Options{Storage: natsdriver.FileStorage, StoreDir: t.TempDir()})
			if err != nil {
				t.Fatalf("open file bus: %v", err)
			}
			t.Cleanup(func() { _ = b.Close() })
			return b
		})
	})
}

// scenario: crash-recovery — a file-storage stream + persisted message survive a
// close/reopen on the same StoreDir.
func TestScenario_CrashRecovery(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// session 1: create stream, publish, close.
	b1, err := natsdriver.Open(ctx, natsdriver.Options{Storage: natsdriver.FileStorage, StoreDir: dir})
	if err != nil {
		t.Fatalf("open(1): %v", err)
	}
	if err := b1.EnsureStream(ctx, bus.StreamConfig{Name: "DUR", Subjects: []bus.Subject{"dur.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}
	if err := b1.Publish(ctx, "dur.1", []byte("survives")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := b1.Close(); err != nil {
		t.Fatalf("close(1): %v", err)
	}

	// session 2: reopen the same StoreDir — the stream + message are recovered.
	b2, err := natsdriver.Open(ctx, natsdriver.Options{Storage: natsdriver.FileStorage, StoreDir: dir})
	if err != nil {
		t.Fatalf("open(2): %v", err)
	}
	defer func() { _ = b2.Close() }()

	c, err := b2.Consume(ctx, bus.ConsumeConfig{Stream: "DUR", Durable: "d1", Subject: "dur.>"})
	if err != nil {
		t.Fatalf("Consume after reopen: %v", err)
	}
	defer func() { _ = c.Close() }()

	select {
	case m, ok := <-c.C():
		if !ok {
			t.Fatal("consumer channel closed")
		}
		if string(m.Data) != "survives" {
			t.Fatalf("recovered %q want survives", m.Data)
		}
		if err := m.Ack(); err != nil {
			t.Fatalf("Ack: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crash-recovery: timed out — message not recovered")
	}
}
