package dataplane_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/dataplane"
	"github.com/pyvvo/funcd/internal/edge/limit"
	"github.com/pyvvo/funcd/internal/edge/router"
)

// perTarget is the key: function rate step at one call per minute, so no token refills during a test.
func perTarget(burst, maxKeys int) *limit.TargetLimiter {
	return limit.NewTargetLimiter(limit.Config{RatePerMin: 1, Burst: burst, Key: limit.KeyFunction, MaxKeys: maxKeys})
}

func requireThrottled(t *testing.T, resp *http.Response, msg string) {
	t.Helper()
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode, msg)
	require.NotEmpty(t, resp.Header.Get("Retry-After"), msg)
}

// scenario: route-paths-share-target-bucket
func TestScenarioRoutePathsShareTargetBucket(t *testing.T) {
	entries := []router.Entry{{Namespace: "team", Rules: []router.CompiledRule{{Path: "/a", Function: "fa"}}}}
	h, st, scaler, _ := limitedFrontDoor(t, nil, entries, perTarget(1, 0))
	seedNS(t, st, "team", v1.ExposureImplicit)
	seedFn(t, st, "team", "fa")

	first := do(t, h, "GET", "edge.test", "/a/1", "")
	require.NotEqual(t, http.StatusTooManyRequests, first.StatusCode)
	require.Equal(t, 1, scaler.count(), "the first call wakes fa")
	for _, path := range []string{"/a/2", "/a", "/a/"} {
		requireThrottled(t, do(t, h, "GET", "edge.test", path, ""), path+" shares fa's bucket")
	}
	requireThrottled(t, do(t, h, "GET", "edge.test", "/function/fa/x", "team"), "the invoke form shares fa's bucket")
	require.Equal(t, 1, scaler.count(), "a 429 never wakes fa")
}

// scenario: tenants-do-not-share-bucket
func TestScenarioTenantsDoNotShareBucket(t *testing.T) {
	warm := map[v1.ObjectName]string{"fa": "u", "fb": "u", "x": "u"}
	entries := []router.Entry{
		{Namespace: "t1", Host: "h1.test", Rules: []router.CompiledRule{{Path: "/h", Function: "fa"}}},
		{Namespace: "t2", Host: "h2.test", Rules: []router.CompiledRule{{Path: "/h", Function: "fb"}}},
	}
	h, st, _, _ := limitedFrontDoor(t, warm, entries, perTarget(1, 0))
	seedFn(t, st, "t1", "fa")
	seedFn(t, st, "t2", "fb")
	seedFn(t, st, "a", "x")
	seedFn(t, st, "b", "x")

	require.Equal(t, http.StatusOK, do(t, h, "GET", "h1.test", "/h/1", "").StatusCode)
	require.Equal(t, http.StatusOK, do(t, h, "GET", "h2.test", "/h/1", "").StatusCode, "the same path on another host is another target")
	require.Equal(t, http.StatusOK, do(t, h, "GET", "edge.test", "/function/x", "a").StatusCode)
	require.Equal(t, http.StatusOK, do(t, h, "GET", "edge.test", "/function/x", "b").StatusCode, "the same name in another namespace is another target")
	requireThrottled(t, do(t, h, "GET", "h1.test", "/h/1", ""), "t1/fa has spent its burst")
}

// scenario: junk-names-create-no-bucket
func TestScenarioJunkNamesCreateNoBucket(t *testing.T) {
	warm := map[v1.ObjectName]string{"f1": "u", "f2": "u"}
	h, st, _, _ := limitedFrontDoor(t, warm, nil, perTarget(2, 2))
	seedFn(t, st, "default", "f1")
	seedFn(t, st, "default", "f2")

	for i := 0; i < 100; i++ {
		require.Equal(t, http.StatusNotFound, do(t, h, "GET", "edge.test", fmt.Sprintf("/function/junk-%d", i), "").StatusCode)
	}
	require.Equal(t, http.StatusOK, do(t, h, "GET", "edge.test", "/function/f1", "").StatusCode)
	require.Equal(t, http.StatusOK, do(t, h, "GET", "edge.test", "/function/f2", "").StatusCode)
}

// scenario: drained-bucket-survives-flood
func TestScenarioDrainedBucketSurvivesFlood(t *testing.T) {
	h, st, _, _ := limitedFrontDoor(t, map[v1.ObjectName]string{"victim": "u"}, nil, perTarget(5, 0))
	seedFn(t, st, "default", "victim")

	served := 0
	for i := 0; i < 10; i++ {
		if do(t, h, "GET", "edge.test", "/function/victim", "").StatusCode == http.StatusOK {
			served++
		}
	}
	for i := 0; i < 4096; i++ {
		require.Equal(t, http.StatusNotFound, do(t, h, "GET", "edge.test", fmt.Sprintf("/function/junk-%d", i), "").StatusCode)
	}
	for i := 0; i < 5; i++ {
		requireThrottled(t, do(t, h, "GET", "edge.test", "/function/victim", ""), "the flood did not reset the victim's bucket")
	}
	require.Equal(t, 5, served)
}

// scenario: fn-to-fn-not-rate-limited
func TestScenarioFnToFnNotRateLimited(t *testing.T) {
	h, st, _, _ := limitedFrontDoor(t, map[v1.ObjectName]string{"fa": "u"}, nil, perTarget(1, 0))
	seedFn(t, st, "default", "fa")

	require.Equal(t, http.StatusOK, do(t, h, "POST", "edge.test", "/function/fa", "").StatusCode, "the external call drains fa")
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "http://edge.test/function/fa", nil)
		req = req.WithContext(dataplane.WithInternal(req.Context()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "internal call %d is not rate-limited", i)
	}
	requireThrottled(t, do(t, h, "POST", "edge.test", "/function/fa", ""), "the next external call")
}
