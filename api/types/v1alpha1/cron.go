package v1alpha1

import (
	"github.com/pyvvo/funcd/api/cron"
	"github.com/pyvvo/funcd/api/fault"
)

// CheckCron is the admission check of a cron schedule (ADR-0211): fault.Invalid(op) naming exprField or zoneField,
// the value and, for the expression, cron.Grammar.
func CheckCron(op, exprField, expr, zoneField, zone string) error {
	loc, err := cron.LoadZone(zone)
	if err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "%s %q is not a time zone", zoneField, zone)
	}
	if _, err := cron.Parse(expr, loc); err != nil {
		return fault.Wrapf(err, fault.Invalid, op, "%s %q is not a valid schedule", exprField, expr)
	}
	return nil
}
