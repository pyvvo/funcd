//go:build e2e

package funcd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// renewalRig is a shim rig whose S3 key pairs rotate every period, serving Function s, which binds prefix raw of Bucket
// lake, holds raw/x and returns its key pair; its handler takes boot to load.
type renewalRig struct {
	*shimRig
	s3Addr string
}

func newRenewalRig(t *testing.T, period time.Duration, replicas int, boot time.Duration) *renewalRig {
	t.Helper()
	masterFile := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(masterFile, []byte("e2e-renewal-master-0123456789abcdef"), 0o600))
	var h *shimRig
	p, s3Addr := funcd.StartWithS3Gateway(t, funcd.FreeLoopbackAddr, func(addr string, logger funcd.Option) (*funcd.Platform, error) {
		var opts []funcd.Option
		h, opts = shimRigBase(t, "")
		return funcd.New(append(opts, funcd.WithS3Gateway(addr, "", 0, masterFile, shortDataDir(t)),
			funcd.WithS3KeyRotation(period), funcd.WithPacing(funcd.Pacing{
				SupervisionPeriod: 100 * time.Millisecond, DrainGrace: time.Second,
				HandOutSettle: 100 * time.Millisecond, DrainPollInterval: 50 * time.Millisecond,
			}), logger)...)
	})
	h.connect(t, p)
	rig := &renewalRig{shimRig: h, s3Addr: s3Addr}
	obj, _ := v1.NewObject(v1.KindBucket)
	b := obj.(*v1.Bucket)
	b.Name = "lake"
	b.Spec.Prefixes = []v1.BucketPrefix{{Name: "raw"}}
	h.applyObj(t, b)
	// Only once its Bucket exists: the boot reclaim purges an object of a Bucket it cannot find (ADR-0199, #1246).
	require.NoError(t, h.bucket.Put(context.Background(), "s3/default/lake/raw/x", []byte("raw-x"), blob.PutOptions{}))
	src := "await new Promise((r) => setTimeout(r, " + strconv.Itoa(int(boot.Milliseconds())) + "));\n" + nodePoolLang().creds
	h.deploy(t, "s", nodePoolLang().fn(src).with(func(fn *v1.Function) {
		fn.Spec.Bindings.Blob = []v1.FunctionBlob{{Alias: "raw", Bucket: "lake", Prefix: "raw"}}
		fn.Spec.Replicas = replicas
	}))
	waitReady(t, h.c, "s")
	return rig
}

// read reads raw/x with a key pair.
func (r *renewalRig) read(t *testing.T, ak, sk string) error {
	out, err := s3Client(t, "http://"+r.s3Addr, ak, sk).GetObject(context.Background(), &awss3.GetObjectInput{Bucket: aws.String("lake"), Key: aws.String("raw/x")})
	if err == nil {
		_ = out.Body.Close()
	}
	return err
}

// load calls s every 50 ms and samples its phase and the S3 read of the latest pair until stop, recording every
// failed call or read, every phase other than Ready with its reason, and the pairs seen.
type load struct {
	mu               sync.Mutex
	failed, statuses []string
	keys             map[string]bool
	firstAK, firstSK string
	lastAK, lastSK   string
	wg               sync.WaitGroup
	stopped          chan struct{}
}

func (r *renewalRig) startLoad(t *testing.T) *load {
	l := &load{keys: map[string]bool{}, stopped: make(chan struct{})}
	l.wg.Add(2)
	go func() {
		defer l.wg.Done()
		for {
			select {
			case <-l.stopped:
				return
			default:
			}
			rep := r.postWithin("s", `{}`, 20*time.Second)
			var out memberReply
			l.mu.Lock()
			if rep.status != http.StatusOK || json.Unmarshal([]byte(rep.body), &out) != nil {
				l.failed = append(l.failed, rep.body)
			} else {
				l.keys[out.AK] = true
				if l.firstAK == "" {
					l.firstAK, l.firstSK = out.AK, out.SK
				}
				l.lastAK, l.lastSK = out.AK, out.SK
			}
			l.mu.Unlock()
			time.Sleep(50 * time.Millisecond)
		}
	}()
	go func() {
		defer l.wg.Done()
		for {
			select {
			case <-l.stopped:
				return
			default:
			}
			if fn := r.function(t, "s"); fn.Status.Phase != v1.PhaseReady {
				c, _ := fn.Status.Conditions.Get("Ready")
				l.mu.Lock()
				l.statuses = append(l.statuses, string(fn.Status.Phase)+"/"+c.Reason)
				l.mu.Unlock()
			}
			l.mu.Lock()
			ak, sk := l.lastAK, l.lastSK
			l.mu.Unlock()
			if ak != "" {
				if err := r.read(t, ak, sk); err != nil {
					l.mu.Lock()
					l.failed = append(l.failed, "S3 read with the current pair: "+err.Error())
					l.mu.Unlock()
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	return l
}

func (l *load) stop() {
	close(l.stopped)
	l.wg.Wait()
}

// scenario: workers-renewed (ADR-0270) — under calls every 50 ms, Function s's workers renew their key pairs on a real
// platform with no call failing and every current pair reading through the S3 gateway:
//   - rolling: two replicas that take 150 ms to boot renew over two 6 s periods; the standby renews early and the serving
//     replica at its renewal age, waiting for a booting renewal replacement, so s never leaves Ready. The boot stays
//     inside the wait's bound, a twentieth of the period at least, as seconds of boot do in a 24 h period. Two periods
//     take about 14 s. It is a smoke test: at such ratios the overlap the rolling rules prevent is rare, so it does not
//     fail without them; the function package's renewal tests prove the rules with a boot hold across passes;
//   - single: a lone replica renews alone, reading Degraded only with reason Renewing, and its first pair is refused
//     two 3 s windows later.
func TestScenarioKeyRenewalUnderLoad(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		period, boot time.Duration
		replicas     int
		run          time.Duration
	}{
		{"rolling", 6 * time.Second, 150 * time.Millisecond, 2, 13 * time.Second},
		{"single", 3 * time.Second, 0, 1, 7500 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newRenewalRig(t, tc.period, tc.replicas, tc.boot)
			l := rig.startLoad(t)
			time.Sleep(tc.run)
			l.stop()
			require.Empty(t, l.failed, "every call answers and every current pair reads")
			require.GreaterOrEqual(t, len(l.keys), 2, "the workers renewed their pairs")
			for _, s := range l.statuses {
				if tc.replicas > 1 {
					t.Errorf("rolling renewals left s %s", s)
				} else {
					require.Equal(t, string(v1.PhaseDegraded)+"/Renewing", s, "a lone replica's renewal is not an exit")
				}
			}
			require.Error(t, rig.read(t, l.firstAK, l.firstSK), "the first pair is refused two windows later")
		})
	}
}
