// Package nats is the embedded-NATS driver for the bus.Bus port (ADR-0008): an
// in-process nats-server with JetStream (memory or file storage), all pure-Go
// (no cgo). It lives in its own package so nats stays out of the driver-dep-free
// port (internal/bus). The raw JetStream API is never exposed beyond this driver.
package nats

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/bus"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Storage selects the JetStream backend for streams created on this bus.
type Storage int

const (
	MemoryStorage Storage = iota
	FileStorage
)

const readyTimeout = 10 * time.Second

// Options configures the embedded server.
type Options struct {
	Storage  Storage
	StoreDir string // required for FileStorage
}

// Open starts an in-process nats-server (JetStream enabled) and returns the bus.
//
//	Open(ctx, Options{Storage: MemoryStorage})              → in-memory (tests / InMemory())
//	Open(ctx, Options{Storage: FileStorage, StoreDir: dir}) → durable (production)
func Open(ctx context.Context, opts Options) (bus.Bus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	storeDir := opts.StoreDir
	cleanup := func() {}
	switch opts.Storage {
	case FileStorage:
		if storeDir == "" {
			return nil, fault.Invalidf("nats.Open", "StoreDir is required for file storage")
		}
	default: // MemoryStorage
		d, err := os.MkdirTemp("", "funcd-bus-mem-")
		if err != nil {
			return nil, fault.Internalf("nats.Open", "temp store dir: %v", err)
		}
		storeDir = d
		cleanup = func() { _ = os.RemoveAll(d) }
	}

	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "funcd-bus",
		Port:       -1, // random free port
		JetStream:  true,
		StoreDir:   storeDir,
		NoLog:      true,
		NoSigs:     true,
	})
	if err != nil {
		cleanup()
		return nil, fault.Internalf("nats.Open", "new server: %v", err)
	}
	go srv.Start()
	ready := make(chan bool, 1)
	go func() { ready <- srv.ReadyForConnections(readyTimeout) }()
	select {
	case ok := <-ready:
		if !ok {
			srv.Shutdown()
			cleanup()
			return nil, fault.Unavailablef("nats.Open", "server not ready within %s", readyTimeout)
		}
	case <-ctx.Done():
		srv.Shutdown()
		cleanup()
		return nil, ctx.Err()
	}
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		cleanup()
		return nil, fault.Unavailablef("nats.Open", "connect: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		srv.Shutdown()
		cleanup()
		return nil, fault.Internalf("nats.Open", "jetstream: %v", err)
	}
	return &embedded{srv: srv, nc: nc, js: js, storage: opts.Storage, cleanup: cleanup, subs: map[int]func(){}}, nil
}

type embedded struct {
	srv     *natsserver.Server
	nc      *nats.Conn
	js      jetstream.JetStream
	storage Storage
	cleanup func()

	mu     sync.Mutex     // guards subs/nextID/closed
	subs   map[int]func() // live subscriptions/consumers -> their stop func
	nextID int
	closed bool
}

// register records a live subscription/consumer's stop func so Close() can stop it
// (preventing a forwarder-goroutine leak); it returns the deregistration id.
func (e *embedded) register(stop func()) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	id := e.nextID
	e.nextID++
	e.subs[id] = stop
	return id
}

func (e *embedded) deregister(id int) {
	e.mu.Lock()
	delete(e.subs, id)
	e.mu.Unlock()
}

func (e *embedded) streamStorage() jetstream.StorageType {
	if e.storage == FileStorage {
		return jetstream.FileStorage
	}
	return jetstream.MemoryStorage
}

func (e *embedded) Publish(ctx context.Context, subject bus.Subject, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Core publish; a JetStream stream covering the subject captures it durably.
	if err := e.nc.Publish(string(subject), data); err != nil {
		return mapErr("bus.Publish", err)
	}
	if err := e.nc.Flush(); err != nil {
		return mapErr("bus.Publish", err)
	}
	return nil
}

func (e *embedded) Subscribe(ctx context.Context, subject bus.Subject) (bus.Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	natsCh := make(chan *nats.Msg, 64)
	sub, err := e.nc.ChanSubscribe(string(subject), natsCh)
	if err != nil {
		return nil, mapErr("bus.Subscribe", err)
	}
	// Ensure the subscription is registered server-side before any publish races it.
	if err := e.nc.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, mapErr("bus.Subscribe", err)
	}
	out := make(chan bus.Message)
	done := make(chan struct{})
	cs := &coreSub{e: e, sub: sub, out: out, done: done}
	cs.id = e.register(cs.stop) // register before the goroutine so Close can never miss it
	go func() {
		defer close(out)
		for {
			select {
			case m := <-natsCh:
				select {
				case out <- bus.NewMessage(bus.Subject(m.Subject), m.Data, nil):
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()
	return cs, nil
}

func (e *embedded) EnsureStream(ctx context.Context, cfg bus.StreamConfig) error {
	subjects := make([]string, len(cfg.Subjects))
	for i, s := range cfg.Subjects {
		subjects[i] = string(s)
	}
	if _, err := e.js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     cfg.Name,
		Subjects: subjects,
		Storage:  e.streamStorage(),
	}); err != nil {
		return mapErr("bus.EnsureStream", err)
	}
	return nil
}

func (e *embedded) Consume(ctx context.Context, cfg bus.ConsumeConfig) (bus.Consumer, error) {
	stream, err := e.js.Stream(ctx, cfg.Stream)
	if err != nil {
		return nil, mapErr("bus.Consume", err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       cfg.Durable,
		FilterSubject: string(cfg.Subject),
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, mapErr("bus.Consume", err)
	}
	iter, err := cons.Messages()
	if err != nil {
		return nil, mapErr("bus.Consume", err)
	}
	out := make(chan bus.Message)
	done := make(chan struct{})
	jc := &jsConsumer{e: e, iter: iter, out: out, done: done}
	jc.id = e.register(jc.stop)
	go func() {
		defer close(out)
		for {
			msg, err := iter.Next()
			if err != nil {
				return // iterator stopped
			}
			select {
			case out <- bus.NewMessage(bus.Subject(msg.Subject()), msg.Data(), msg.Ack):
			case <-done:
				_ = msg.Nak()
				return
			}
		}
	}()
	return jc, nil
}

func (e *embedded) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil // idempotent
	}
	e.closed = true
	stops := make([]func(), 0, len(e.subs))
	for _, st := range e.subs {
		stops = append(stops, st)
	}
	e.subs = map[int]func(){}
	e.mu.Unlock()
	// Stop every live subscription/consumer first so their forwarder goroutines exit
	// (no leak): nats does not close the user channels on nc.Close, so a parked
	// forwarder would otherwise block forever.
	for _, st := range stops {
		st()
	}
	e.nc.Close()
	e.srv.Shutdown()
	e.srv.WaitForShutdown()
	e.cleanup()
	return nil
}

type coreSub struct {
	e    *embedded
	id   int
	sub  *nats.Subscription
	out  chan bus.Message
	done chan struct{}
	once sync.Once
	err  error
}

func (s *coreSub) C() <-chan bus.Message { return s.out }

// stop releases the NATS subscription and signals the forwarder to exit (idempotent).
func (s *coreSub) stop() {
	s.once.Do(func() {
		s.err = s.sub.Unsubscribe()
		close(s.done)
	})
}

func (s *coreSub) Unsubscribe() error {
	s.stop()
	s.e.deregister(s.id)
	if s.err != nil {
		return mapErr("bus.Unsubscribe", s.err)
	}
	return nil
}

type jsConsumer struct {
	e    *embedded
	id   int
	iter jetstream.MessagesContext
	out  chan bus.Message
	done chan struct{}
	once sync.Once
}

func (c *jsConsumer) C() <-chan bus.Message { return c.out }

// stop releases the JetStream iterator and signals the forwarder to exit (idempotent).
func (c *jsConsumer) stop() {
	c.once.Do(func() {
		close(c.done)
		c.iter.Stop()
	})
}

func (c *jsConsumer) Close() error {
	c.stop()
	c.e.deregister(c.id)
	return nil
}

// mapErr translates nats / JetStream errors to api/fault kinds (ADR-0008 §4).
func mapErr(op string, err error) error {
	switch {
	case errors.Is(err, jetstream.ErrStreamNotFound), errors.Is(err, jetstream.ErrConsumerNotFound):
		return fault.Wrapf(err, fault.NotFound, op, "not found")
	case errors.Is(err, nats.ErrTimeout), errors.Is(err, nats.ErrNoServers), errors.Is(err, nats.ErrConnectionClosed):
		return fault.Wrapf(err, fault.Unavailable, op, "messaging unavailable")
	default:
		return fault.Wrapf(err, fault.Internal, op, "operation failed")
	}
}
