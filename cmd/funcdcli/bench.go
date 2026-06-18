package main

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/green-0-rabbit/funcd/api/fault"
	"github.com/green-0-rabbit/funcd/internal/loadgen"
)

// benchCmd is the `funcdcli bench` verb (ADR-0053): a client-side load/latency probe against a
// RUNNING funcd's data plane — the funcd analogue of `nats bench`. It does NOT embed the platform or
// measure memory (that is funcd-bench, ADR-0040/0052); the load engine (internal/loadgen) is
// stdlib-only, so funcdcli ships no bench dependency (ADR-0051's confinement).
func (a *cli) benchCmd() *cobra.Command {
	var (
		url, function, dataPlane  string
		method, body, contentType string
		concurrency, requests     int
		duration                  time.Duration
		asJSON                    bool
	)
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Load-test a deployed function's data-plane endpoint (throughput + tail latency)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target, err := benchTarget(url, function, dataPlane)
			if err != nil {
				return err
			}
			res, err := loadgen.Run(cmd.Context(), loadgen.Options{
				URL: target, Method: method, Body: body, ContentType: contentType,
				Concurrency: concurrency, Duration: duration, Requests: requests,
			})
			if err != nil {
				return err
			}
			if res.OK == 0 {
				return fault.Unavailablef("funcdcli bench", "no request succeeded against %s (%d errors)", target, res.Errors)
			}
			if asJSON {
				return a.writeBenchJSON(res)
			}
			return a.writeBenchTable(target, res)
		},
	}
	f := cmd.Flags()
	f.StringVar(&url, "url", "", "data-plane endpoint to hit (e.g. http://host:8081/function/hello)")
	f.StringVarP(&function, "function", "f", "", "function name — with --data-plane builds <data-plane>/function/<name>")
	f.StringVar(&dataPlane, "data-plane", envOr("FUNCD_DATA_PLANE", ""), "data-plane base URL ($FUNCD_DATA_PLANE), used with --function")
	f.IntVarP(&concurrency, "concurrency", "c", 8, "concurrent workers")
	f.DurationVarP(&duration, "duration", "d", 5*time.Second, "run length (ignored when --requests is set)")
	f.IntVarP(&requests, "requests", "n", 0, "total requests across workers (overrides --duration)")
	f.StringVar(&method, "method", "POST", "HTTP method")
	f.StringVar(&body, "body", "{}", "request body")
	f.StringVar(&contentType, "content-type", "application/json", "request content-type")
	f.BoolVar(&asJSON, "json", false, "emit the result as JSON")
	return cmd
}

// benchTarget resolves the data-plane endpoint from --url, or from --function + --data-plane.
func benchTarget(url, function, dataPlane string) (string, error) {
	const op = "funcdcli bench"
	if url != "" {
		return url, nil
	}
	if function != "" && dataPlane != "" {
		return strings.TrimRight(dataPlane, "/") + "/function/" + function, nil
	}
	return "", fault.Invalidf(op, "specify --url, or --function together with --data-plane")
}

func (a *cli) writeBenchJSON(res loadgen.Result) error {
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fault.Wrapf(err, fault.Internal, "funcdcli bench", "marshal json")
	}
	return a.writef("%s\n", data)
}

func (a *cli) writeBenchTable(target string, res loadgen.Result) error {
	return a.writef(
		"bench %s\n"+
			"  requests   %d (ok %d, errors %d) in %s\n"+
			"  throughput %.0f req/s\n"+
			"  latency    p50 %s · p90 %s · p99 %s · max %s\n",
		target, res.Total, res.OK, res.Errors, res.Duration.Round(time.Millisecond),
		res.RPS,
		res.P50.Round(time.Microsecond), res.P90.Round(time.Microsecond),
		res.P99.Round(time.Microsecond), res.Max.Round(time.Microsecond),
	)
}
