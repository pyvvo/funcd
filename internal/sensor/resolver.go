package sensor

import (
	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/expr"
)

// eventResolver is the F73 Resolver exposing the firing CloudEvent as the `event` root (ADR-0109): a Sensor
// action's `${{ }}` input references `event.data.<path>` / `event.time` / `event.id` / `event.source` /
// `event.type`. `event.data` is an OPAQUE object — any deep path is admitted (the event payload's shape is
// dynamic). Every field is reported Required:true (an event field is always present at fire time), so the
// F73 defaults rule (an unguarded optional field is rejected) never trips a projection.
type eventResolver struct{}

func (eventResolver) Roots() []string { return []string{"event"} }

func (eventResolver) Resolve(root string, path []string) (expr.Field, error) {
	if root != "event" {
		return expr.Field{}, fault.NotFoundf("sensor.resolve", "unknown reference root %q (only `event` is available)", root)
	}
	if len(path) == 0 {
		return expr.Field{Type: "object", Required: true}, nil
	}
	switch path[0] {
	case "data":
		// opaque payload: any path under data resolves as an object (no deep type-check).
		return expr.Field{Type: "object", Required: true}, nil
	case "time", "id", "source", "type":
		return expr.Field{Type: "string", Required: true}, nil
	default:
		return expr.Field{}, fault.NotFoundf("sensor.resolve", "event has no field %q", path[0])
	}
}
