package funcd

import (
	"context"
	"errors"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/funclog"
)

// memberSink stores each raw output line of a pool worker under every member of the pool, as taken when the run
// started (ADR-0168, Open question 1): a raw line names no member.
type memberSink struct {
	inner   funclog.Sink
	members []funclog.Resource
}

func newMemberSink(inner funclog.Sink, worker funclog.Resource, members []v1.ObjectName) *memberSink {
	m := &memberSink{inner: inner}
	for _, name := range members {
		res := worker
		res.Function = string(name)
		m.members = append(m.members, res)
	}
	return m
}

func (m *memberSink) Append(ctx context.Context, _ funclog.Resource, e funclog.Entry) error {
	var errs []error
	for _, res := range m.members {
		errs = append(errs, m.inner.Append(ctx, res, e))
	}
	return errors.Join(errs...)
}

func (m *memberSink) Flush(ctx context.Context, _ funclog.Resource) (string, error) {
	var errs []error
	for _, res := range m.members {
		_, err := m.inner.Flush(ctx, res)
		errs = append(errs, err)
	}
	return "", errors.Join(errs...)
}

// Close is a no-op: the platform closes the inner sink.
func (m *memberSink) Close() error { return nil }
