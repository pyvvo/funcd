package main

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// quarantineCmd groups the quarantine verbs (ADR-0288 Decision 6): `funcdctl quarantine return` brings an object's
// quarantined data back once the object exists again; `funcdctl hold status` lists the entries. Admin-only.
func (a *cli) quarantineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "The data a start moved aside instead of deleting it, as no object named it (return)",
	}
	cmd.AddCommand(a.quarantineReturnCmd())
	return cmd
}

func (a *cli) quarantineReturnCmd() *cobra.Command {
	var since string
	cmd := &cobra.Command{
		Use:   "return <kvstore|bucket|volume> <namespace>/<name>[/<volume>]",
		Short: "Return an object's quarantined data into it, once the object exists again and holds no data",
		Long: "Return an object's quarantined data into it (ADR-0288). Apply the object again first: the return refuses " +
			"an object that is missing or whose data is not empty, and nothing returns on its own. A volume is " +
			"<namespace>/<function>/<volume>. Without --since, the newest entry returns; a return cut short resumes " +
			"when run again.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := quarantineReturn(args[0], args[1], since)
			if err != nil {
				return err
			}
			c, err := a.sdkClient()
			if err != nil {
				return err
			}
			if err := c.ReturnQuarantined(cmd.Context(), r); err != nil {
				return err
			}
			return a.writef("returned the quarantined data of %s %s\n", args[0], args[1])
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "the entry's since, as hold status lists it (RFC 3339); the newest entry when absent")
	return cmd
}

// quarantineReturn parses return's arguments into the request.
func quarantineReturn(kind, ref, since string) (hold.QuarantineReturn, error) {
	const op = "funcdctl quarantine return"
	parts := strings.Split(ref, "/")
	var r hold.QuarantineReturn
	switch kind {
	case "kvstore", "bucket":
		if len(parts) != 2 {
			return r, fault.Invalidf(op, "%q: want <namespace>/<name>", ref)
		}
		r.Kind = v1.KindKVStore
		if kind == "bucket" {
			r.Kind = v1.KindBucket
		}
	case "volume":
		if len(parts) != 3 {
			return r, fault.Invalidf(op, "%q: want <namespace>/<function>/<volume>", ref)
		}
		r.Kind, r.Volume = v1.KindFunction, v1.ObjectName(parts[2])
	default:
		return r, fault.Invalidf(op, "kind %q: want kvstore, bucket or volume", kind)
	}
	r.Namespace, r.Name = v1.NamespaceName(parts[0]), v1.ObjectName(parts[1])
	if since != "" {
		t, err := time.Parse(time.RFC3339Nano, since)
		if err != nil {
			return r, fault.Invalidf(op, "--since %q: want an RFC 3339 time, as hold status lists it", since)
		}
		ts := v1.NewTimestamp(t)
		r.Since = &ts
	}
	return r, nil
}
