//go:build !e2e

package funcd_test

import (
	"net/http"
	"testing"

	"github.com/pyvvo/funcd/internal/workernode/local"
)

// jobEnvs are the platforms that the Job facet's matrix calls: one whose default namespace holds the Function
// reader, whose worker is no Job's.
type jobEnvs struct {
	main *causeEnv
}

// jobCauseRows is the Job facet's matrix over envs (ADR-0348 Decision 6): GET /job/run refuses a caller that is no
// Job's worker.
func jobCauseRows(envs jobEnvs) []causeRow {
	return []causeRow{{endpoint: "GET /job/run", cause: local.ErrNotAJobWorker, env: envs.main, caller: "reader",
		request: causeRequest{method: http.MethodGet, path: "/job/run"}}}
}

// TestJobCauseMatrix provokes each cause that a Job endpoint declares with one request.
func TestJobCauseMatrix(t *testing.T) {
	t.Parallel()
	p := answersPlatform(t)
	create(t, answersClient(), "http://"+p.Addr(), function("reader"))
	runCauseMatrix(t, jobCauseRows(jobEnvs{main: &causeEnv{p: p}}))
}
