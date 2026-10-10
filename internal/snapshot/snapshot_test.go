package snapshot_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/snapshot"
)

// source is a Source of fixed records that logs its reads and can fail.
type source struct {
	name, version string
	keys          []string
	err           error
	log           *[]string
}

func (s source) Snapshot(_ context.Context, emit func(snapshot.Record) error) (string, error) {
	*s.log = append(*s.log, "read "+s.name)
	if s.err != nil {
		return "", s.err
	}
	for _, k := range s.keys {
		if err := emit(snapshot.Record{Key: []byte(k), Value: []byte(s.name)}); err != nil {
			return "", err
		}
	}
	return s.version, nil
}

// scenario: cut-reads-in-order — Cut reads the event store, the metastore, then the run state, emits each
// record with its source, returns the metastore's version, and reads nothing after a failed read.
func TestScenarioCutReadsInOrder(t *testing.T) {
	var log []string
	events := source{name: "events", keys: []string{"e1"}, log: &log}
	meta := source{name: "meta", version: "0123456789abcdef-7", keys: []string{"m1", "m2"}, log: &log}
	runs := source{name: "runs", keys: []string{"r1"}, log: &log}
	rv, err := snapshot.Cut(context.Background(), events, meta, runs, func(src snapshot.Source, r snapshot.Record) error {
		log = append(log, "emit "+src.(source).name+" "+string(r.Key))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "0123456789abcdef-7", rv)
	require.Equal(t, []string{
		"read events", "emit events e1",
		"read meta", "emit meta m1", "emit meta m2",
		"read runs", "emit runs r1",
	}, log)

	for _, failing := range []string{"events", "meta", "runs"} {
		t.Run("a failed "+failing+" read ends the cut", func(t *testing.T) {
			var log []string
			broken := errors.New("broken " + failing)
			srcs := map[string]source{}
			for _, name := range []string{"events", "meta", "runs"} {
				srcs[name] = source{name: name, keys: []string{name}, log: &log}
			}
			f := srcs[failing]
			f.err = broken
			srcs[failing] = f
			_, err := snapshot.Cut(context.Background(), srcs["events"], srcs["meta"], srcs["runs"],
				func(snapshot.Source, snapshot.Record) error { return nil })
			require.ErrorIs(t, err, broken)
			require.Equal(t, "read "+failing, log[len(log)-1], "nothing is read after the failed read")
		})
	}

	t.Run("an emit error ends the cut", func(t *testing.T) {
		var log []string
		stop := errors.New("target full")
		_, err := snapshot.Cut(context.Background(),
			source{name: "events", keys: []string{"e1"}, log: &log},
			source{name: "meta", keys: []string{"m1"}, log: &log},
			source{name: "runs", keys: []string{"r1"}, log: &log},
			func(src snapshot.Source, _ snapshot.Record) error {
				if src.(source).name == "meta" {
					return stop
				}
				return nil
			})
		require.ErrorIs(t, err, stop)
		require.Equal(t, []string{"read events", "read meta"}, log)
	})
}
