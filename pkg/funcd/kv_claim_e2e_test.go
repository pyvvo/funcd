//go:build e2e

package funcd_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

type claimOut struct {
	Key     string `json:"key"`
	Won     bool   `json:"won"`
	Version string `json:"version"`
	Holder  string `json:"holder"`
}

// scenario (e2e): claim-race-one-winner — the REAL path: the kv-claim function (funcd-typescript examples/kv-claim)
// on four Node workers takes 32 concurrent claims of one key through context.kv.putIfAbsent → worker-node local API →
// Facade → driver. Exactly one call wins and every loser reports the winner as holder, at the winner's version. Then a
// claim with a 1 s ttl expires and is claimed again, its holder renews with the version it holds while a stale version
// loses, and a release with the held version frees the key.
// oracle: docs/design/storage/kv-claims.fizz#OneHolder
func TestScenarioE2EKVClaimRace(t *testing.T) {
	t.Parallel()
	c, dpURL := shimPlatformOCI(t)
	exDir := tsExample(t, "kv-claim")
	ref, digest := pushExampleFn(t, t.TempDir(), exDir, "claim")
	data, err := os.ReadFile(filepath.Join(exDir, "claim.yaml"))
	require.NoError(t, err)
	var fn v1.Function
	require.NoError(t, yaml.Unmarshal(data, &fn))
	fn.Spec.Image, fn.Spec.ImageDigest = ref, digest
	fn.Spec.Replicas, fn.Spec.Scaling.MinReplicas = 4, 4
	applyFnObj(t, c, &fn)
	waitReady(t, c, "claim")

	call := func(t *testing.T, in string) claimOut {
		t.Helper()
		resp, err := http.Post(dpURL+"/function/claim", "application/json", strings.NewReader(`{"data":`+in+`}`))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
		var out claimOut
		require.NoError(t, json.Unmarshal(body, &out))
		return out
	}

	const racers = 32
	outs := make([]claimOut, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range racers {
		wg.Go(func() {
			<-start
			outs[i] = call(t, fmt.Sprintf(`{"key":"run-1","holder":"h%d"}`, i))
		})
	}
	close(start)
	wg.Wait()
	var winner claimOut
	for _, o := range outs {
		if o.Won {
			require.Empty(t, winner.Holder, "two claims won: %s and %s", winner.Holder, o.Holder)
			winner = o
		}
	}
	require.NotEmpty(t, winner.Holder, "no claim won")
	for _, o := range outs {
		if !o.Won {
			require.Equal(t, winner.Holder, o.Holder, "a loser sees the winner as holder")
			require.Equal(t, winner.Version, o.Version)
		}
	}

	first := call(t, `{"key":"lease","holder":"a","ttl":1}`)
	require.True(t, first.Won)
	held := call(t, `{"key":"lease","holder":"b"}`)
	require.Equal(t, claimOut{Key: "lease", Version: first.Version, Holder: "a"}, held)
	var b claimOut
	require.Eventually(t, func() bool {
		b = call(t, `{"key":"lease","holder":"b","ttl":30}`)
		return b.Won
	}, 4*time.Second, 100*time.Millisecond, "the expired claim is claimed again")
	stale := call(t, fmt.Sprintf(`{"key":"lease","holder":"a","op":"renew","version":%q,"ttl":30}`, first.Version))
	require.False(t, stale.Won, "a renewal with a version it no longer holds loses")
	require.Equal(t, "b", stale.Holder)
	renewed := call(t, fmt.Sprintf(`{"key":"lease","holder":"b","op":"renew","version":%q,"ttl":30}`, b.Version))
	require.True(t, renewed.Won)
	require.NotEqual(t, b.Version, renewed.Version)
	require.False(t, call(t, fmt.Sprintf(`{"key":"lease","holder":"b","op":"release","version":%q}`, b.Version)).Won)
	require.True(t, call(t, fmt.Sprintf(`{"key":"lease","holder":"b","op":"release","version":%q}`, renewed.Version)).Won)
	require.True(t, call(t, `{"key":"lease","holder":"c"}`).Won, "a released key is claimed again")
}
