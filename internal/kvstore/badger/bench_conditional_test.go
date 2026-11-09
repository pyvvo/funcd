package badger

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pyvvo/funcd/internal/kvstore"
)

// BenchmarkGatewayWrites is what one write costs the gateway while GOMAXPROCS writers run at once, with fsync on
// commit as funcd runs it: a plain Put, a put-if-absent and a Put with a TTL on distinct keys, then plain Puts and a
// compare-and-set loop on one hot key. A versioned write reads its version back after the commit, and versioned writes
// of one key never share a txn.
func BenchmarkGatewayWrites(b *testing.B) {
	ctx := context.Background()
	val := []byte("holder-0123456789")
	cases := []struct {
		name  string
		write func(d *driver, n int64) error
	}{
		{"put-distinct", func(d *driver, n int64) error { return d.Put(ctx, fmt.Sprintf("ns/s/k%d", n), val) }},
		{"put-if-absent-distinct", func(d *driver, n int64) error {
			_, err := d.PutIf(ctx, fmt.Sprintf("ns/s/k%d", n), val, kvstore.Write{IfAbsent: true})
			return err
		}},
		{"put-ttl-distinct", func(d *driver, n int64) error {
			_, err := d.PutIf(ctx, fmt.Sprintf("ns/s/k%d", n), val, kvstore.Write{TTL: time.Hour})
			return err
		}},
		{"put-hot", func(d *driver, _ int64) error { return d.Put(ctx, "ns/s/hot", val) }},
		{"compare-and-set-hot", func(d *driver, _ int64) error {
			for {
				e, _, err := d.GetEntry(ctx, "ns/s/hot")
				if err != nil {
					return err
				}
				r, err := d.PutIf(ctx, "ns/s/hot", val, kvstore.Write{IfVersion: e.Version})
				if err != nil || r.Done {
					return err
				}
			}
		}},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			kv, err := Open(b.TempDir(), WithValueLogGCInterval(0))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = kv.(*driver).Close() })
			d := kv.(*driver)
			if err := d.Put(ctx, "ns/s/hot", val); err != nil {
				b.Fatal(err)
			}
			var next atomic.Int64
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if err := tc.write(d, next.Add(1)); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
