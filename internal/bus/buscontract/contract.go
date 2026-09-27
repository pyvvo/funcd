// Package buscontract is the shared conformance suite for the bus.Bus port
// (ADR-0008). RunContract asserts the messaging semantics that must hold for
// every backend — memory and file storage run it, proving driver-conformance-parity.
package buscontract

import (
	"context"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/bus"
)

const (
	recvTimeout = 3 * time.Second
	quietWindow = 250 * time.Millisecond
)

// RunContract runs the backend-agnostic bus scenarios against a Bus built by
// newBus. newBus must return a fresh bus on each call.
func RunContract(t *testing.T, newBus func(t *testing.T) bus.Bus) {
	t.Helper()
	t.Run("pubsub-fanout", func(t *testing.T) { testPubsubFanout(t, newBus(t)) })
	t.Run("pubsub-ephemeral-no-replay", func(t *testing.T) { testEphemeralNoReplay(t, newBus(t)) })
	t.Run("jetstream-durable-delivery", func(t *testing.T) { testDurableDelivery(t, newBus(t)) })
	t.Run("jetstream-durable-replay", func(t *testing.T) { testDurableReplay(t, newBus(t)) })
	t.Run("namespace-isolation", func(t *testing.T) { testNamespaceIsolation(t, newBus(t)) })
}

func testPubsubFanout(t *testing.T, b bus.Bus) {
	ctx := context.Background()
	s1 := subscribe(t, b, "fan")
	defer func() { _ = s1.Unsubscribe() }()
	s2 := subscribe(t, b, "fan")
	defer func() { _ = s2.Unsubscribe() }()

	if err := b.Publish(ctx, "fan", []byte("hi")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if m := recv(t, s1.C()); string(m.Data) != "hi" {
		t.Fatalf("sub1: got %q want hi", m.Data)
	}
	if m := recv(t, s2.C()); string(m.Data) != "hi" {
		t.Fatalf("sub2: got %q want hi", m.Data)
	}
}

func testEphemeralNoReplay(t *testing.T, b bus.Bus) {
	ctx := context.Background()
	// publish with no subscriber — core NATS does not persist it.
	if err := b.Publish(ctx, "eph", []byte("past")); err != nil {
		t.Fatalf("Publish(past): %v", err)
	}
	s := subscribe(t, b, "eph")
	defer func() { _ = s.Unsubscribe() }()
	// the late subscriber must not receive the past message...
	recvNone(t, s.C())
	// ...but does receive a new one.
	if err := b.Publish(ctx, "eph", []byte("now")); err != nil {
		t.Fatalf("Publish(now): %v", err)
	}
	if m := recv(t, s.C()); string(m.Data) != "now" {
		t.Fatalf("got %q want now", m.Data)
	}
}

func testDurableDelivery(t *testing.T, b bus.Bus) {
	ctx := context.Background()
	if err := b.EnsureStream(ctx, bus.StreamConfig{Name: "WORK", Subjects: []bus.Subject{"work.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}
	c, err := b.Consume(ctx, bus.ConsumeConfig{Stream: "WORK", Durable: "w1", Subject: "work.>"})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer func() { _ = c.Close() }()

	if err := b.Publish(ctx, "work.1", []byte("job")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	m := recv(t, c.C())
	if string(m.Data) != "job" {
		t.Fatalf("got %q want job", m.Data)
	}
	if err := m.Ack(); err != nil {
		t.Fatalf("Ack: %v", err)
	}
}

func testDurableReplay(t *testing.T, b bus.Bus) {
	ctx := context.Background()
	if err := b.EnsureStream(ctx, bus.StreamConfig{Name: "EV", Subjects: []bus.Subject{"ev.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}
	// publish BEFORE any consumer exists — the stream persists it.
	if err := b.Publish(ctx, "ev.1", []byte("persisted")); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	c, err := b.Consume(ctx, bus.ConsumeConfig{Stream: "EV", Durable: "e1", Subject: "ev.>"})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer func() { _ = c.Close() }()

	m := recv(t, c.C()) // durable (DeliverAll) replays the persisted message
	if string(m.Data) != "persisted" {
		t.Fatalf("got %q want persisted", m.Data)
	}
	if err := m.Ack(); err != nil {
		t.Fatalf("Ack: %v", err)
	}
}

func testNamespaceIsolation(t *testing.T, b bus.Bus) {
	ctx := context.Background()
	a := subscribe(t, b, "ns.a.>")
	defer func() { _ = a.Unsubscribe() }()

	if err := b.Publish(ctx, "ns.b.x", []byte("b")); err != nil {
		t.Fatalf("Publish(ns.b): %v", err)
	}
	recvNone(t, a.C()) // namespace a must not see namespace b
	if err := b.Publish(ctx, "ns.a.x", []byte("a")); err != nil {
		t.Fatalf("Publish(ns.a): %v", err)
	}
	if m := recv(t, a.C()); string(m.Data) != "a" {
		t.Fatalf("got %q want a", m.Data)
	}
}

// --- helpers ---

func subscribe(t *testing.T, b bus.Bus, subject bus.Subject) bus.Subscription {
	t.Helper()
	s, err := b.Subscribe(context.Background(), subject)
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", subject, err)
	}
	return s
}

func recv(t *testing.T, ch <-chan bus.Message) bus.Message {
	t.Helper()
	select {
	case m, ok := <-ch:
		if !ok {
			t.Fatal("channel closed unexpectedly")
		}
		return m
	case <-time.After(recvTimeout):
		t.Fatal("timed out waiting for message")
		return bus.Message{}
	}
}

func recvNone(t *testing.T, ch <-chan bus.Message) {
	t.Helper()
	select {
	case m := <-ch:
		t.Fatalf("unexpected message: %q", m.Data)
	case <-time.After(quietWindow):
	}
}
