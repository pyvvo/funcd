// Package bus is the messaging port (ADR-0008): core pub/sub plus durable
// JetStream streams/consumers, behind the bus.Bus port. It is the internal-plane
// messaging substrate — never handed to functions (the raw JetStream API is not
// exposed beyond this port; blueprint security note).
//
// This package imports no nats library; the embedded-NATS driver (memory / file
// storage, pure-Go) lives in internal/bus/nats.
package bus

import "context"

// Subject is a typed NATS subject (dot-delimited; namespaced as ns.<namespace>.…).
type Subject string

// Bus is the messaging port: core pub/sub plus durable JetStream streams/consumers.
// Errors are api/fault kinds; every method is ctx-first. The port imports no nats library.
type Bus interface {
	Publish(ctx context.Context, subject Subject, data []byte) error
	Subscribe(ctx context.Context, subject Subject) (Subscription, error)
	EnsureStream(ctx context.Context, cfg StreamConfig) error
	Consume(ctx context.Context, cfg ConsumeConfig) (Consumer, error)
	Close() error
}

// StreamConfig declares a durable JetStream stream capturing the given subjects.
type StreamConfig struct {
	Name     string
	Subjects []Subject
}

// ConsumeConfig declares a durable consumer on a stream for a subject filter.
type ConsumeConfig struct {
	Stream  string
	Durable string
	Subject Subject
}

// Subscription is an ephemeral core subscription. Unsubscribe releases it and
// stops the delivery goroutine (no leak); C() is closed on Unsubscribe.
type Subscription interface {
	C() <-chan Message
	Unsubscribe() error
}

// Consumer is a durable JetStream consumer. Close releases it (the stream persists)
// and stops the delivery goroutine (no leak); C() is closed on Close.
type Consumer interface {
	C() <-chan Message
	Close() error
}

// Message is one delivered message. Ack acknowledges a durable (JetStream) message;
// for a core Subscription it is a no-op. Drivers construct it via NewMessage.
type Message struct {
	Subject Subject
	Data    []byte
	ack     func() error
}

// NewMessage builds a Message — the driver-facing constructor (the ack closure is
// unexported, so a driver in another package sets it through here).
func NewMessage(subject Subject, data []byte, ack func() error) Message {
	return Message{Subject: subject, Data: data, ack: ack}
}

// Ack acknowledges a durable message; a nil ack (core pub/sub) is a no-op.
func (m Message) Ack() error {
	if m.ack == nil {
		return nil
	}
	return m.ack()
}
