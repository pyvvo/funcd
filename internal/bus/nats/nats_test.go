package nats_test

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/bus"
	"github.com/pyvvo/funcd/internal/bus/buscontract"
	natsdriver "github.com/pyvvo/funcd/internal/bus/nats"
)

// scenario: subscribe-after-close — Subscribe/Consume on a closed bus fail cleanly with
// fault.Unavailable (no panic, no forwarder leaked into a drained registry).
func TestSubscribeAfterCloseIsUnavailable(t *testing.T) {
	b, err := natsdriver.Open(context.Background(), natsdriver.Options{Storage: natsdriver.MemoryStorage})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := b.Subscribe(context.Background(), "after.close"); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("Subscribe after Close: kind=%v want unavailable", fault.KindOf(err))
	}
}

// scenario: close-leaks-no-goroutine — ADR-0008 DoD. Close() must reap the per-sub /
// per-consumer forwarder goroutines even when the caller did NOT Unsubscribe/Close them.
func TestScenario_NoGoroutineLeakOnClose(t *testing.T) {
	ctx := context.Background()
	b, err := natsdriver.Open(ctx, natsdriver.Options{Storage: natsdriver.MemoryStorage})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := b.Subscribe(ctx, "leak.sub"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := b.EnsureStream(ctx, bus.StreamConfig{Name: "LK", Subjects: []bus.Subject{"leak.>"}}); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	if _, err := b.Consume(ctx, bus.ConsumeConfig{Stream: "LK", Durable: "d", Subject: "leak.>"}); err != nil {
		t.Fatalf("consume: %v", err)
	}
	// Close WITHOUT Unsubscribe/Close on the sub or consumer.
	if err := b.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// The forwarders must exit promptly; poll their stacks until gone (or fail with a dump).
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !forwarderGoroutineRunning() {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("forwarder goroutine leaked after Close:\n%s", buf[:n])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func forwarderGoroutineRunning() bool {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	s := string(buf[:n])
	return strings.Contains(s, "internal/bus/nats.(*embedded).Subscribe.func") ||
		strings.Contains(s, "internal/bus/nats.(*embedded).Consume.func")
}

// scenario: open-respects-context — Open returns an error (not a started server) when the
// context is already cancelled, exercising the ctx-cancellation path.
func TestOpenRespectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, err := natsdriver.Open(ctx, natsdriver.Options{Storage: natsdriver.MemoryStorage})
	if err == nil {
		_ = b.Close()
		t.Fatal("Open with a cancelled context should return an error")
	}
}

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

// scenario: crash-recovery (durability guard) — Publish must return only AFTER persisting, so closing
// with no grace period loses nothing. A batch is the reliable detector: a non-durable publish (core +
// Flush) drops at least one of many in the async capture window, where a single message usually survives.
func TestScenario_PublishIsDurableBeforeReturn(t *testing.T) {
	const count = 25
	ctx := context.Background()
	dir := t.TempDir()

	b1, err := natsdriver.Open(ctx, natsdriver.Options{Storage: natsdriver.FileStorage, StoreDir: dir})
	if err != nil {
		t.Fatalf("open(1): %v", err)
	}
	if err := b1.EnsureStream(ctx, bus.StreamConfig{Name: "DURBATCH", Subjects: []bus.Subject{"durb.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}
	for i := range count {
		if err := b1.Publish(ctx, "durb.1", []byte(strconv.Itoa(i))); err != nil {
			t.Fatalf("Publish(%d): %v", i, err)
		}
	}
	// no drain, no sleep — every message must already be durable
	if err := b1.Close(); err != nil {
		t.Fatalf("close(1): %v", err)
	}

	b2, err := natsdriver.Open(ctx, natsdriver.Options{Storage: natsdriver.FileStorage, StoreDir: dir})
	if err != nil {
		t.Fatalf("open(2): %v", err)
	}
	defer func() { _ = b2.Close() }()
	c, err := b2.Consume(ctx, bus.ConsumeConfig{Stream: "DURBATCH", Durable: "d1", Subject: "durb.>"})
	if err != nil {
		t.Fatalf("Consume after reopen: %v", err)
	}
	defer func() { _ = c.Close() }()

	seen := make(map[string]bool, count)
	deadline := time.After(30 * time.Second)
	for len(seen) < count {
		select {
		case m, ok := <-c.C():
			if !ok {
				t.Fatal("consumer channel closed")
			}
			seen[string(m.Data)] = true
			if err := m.Ack(); err != nil {
				t.Fatalf("Ack: %v", err)
			}
		case <-deadline:
			t.Fatalf("durability: recovered only %d/%d messages — Publish returned before persisting", len(seen), count)
		}
	}
}
