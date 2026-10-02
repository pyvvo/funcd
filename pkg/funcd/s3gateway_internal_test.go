package funcd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
)

// freeLoopbackAddr returns an unused 127.0.0.1:port for the gateway listener.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// scenario: disabled-by-default (ADR-0080) — New(InMemory()) with no S3 config wires no
// gateway and opens no listener.
func TestScenarioS3GatewayDisabledByDefault(t *testing.T) {
	p, err := New(InMemory())
	if err != nil {
		t.Fatalf("New(InMemory): %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	if p.s3gw != nil {
		t.Fatal("the S3 gateway must be absent unless WithS3Gateway is set")
	}
}

// scenario: enabled wires the gateway + opens the listener (ADR-0080/0085) — WithS3Gateway
// builds the server; once Run binds the node-private addr, a TCP dial succeeds. Disabled by
// default is the contrast above.
func TestScenarioS3GatewayEnabledOpensListener(t *testing.T) {
	_, addr := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), WithS3Gateway(addr, "", 0, "", t.TempDir()))
	})
	conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial s3 gateway listener: %v", derr)
	}
	_ = conn.Close()
}

// startS3Gateway builds a platform whose S3 gateway listens on an address from reserve, and serves the
// gateway until it is ready. versitygw binds the address itself (ADR-0085), so a test can only reserve a
// port and release it, and a parallel test can take it before the gateway binds it (#288): that bind
// collision rebuilds the platform on a fresh address; any other Run error fails the test.
func startS3Gateway(t *testing.T, reserve func(*testing.T) string, build func(addr string) (*Platform, error)) (*Platform, string) {
	t.Helper()
	const attempts = 5
	for i := 1; ; i++ {
		addr := reserve(t)
		p, err := build(addr)
		if err != nil {
			t.Fatalf("New with the S3 gateway on %s: %v", addr, err)
		}
		if p.s3gw == nil {
			t.Fatal("WithS3Gateway must wire the S3 gateway")
		}
		runCtx, cancel := context.WithCancel(context.Background())
		runErr := make(chan error, 1)
		go func() { runErr <- p.s3gw.Run(runCtx) }()
		select {
		case <-p.s3gw.Ready():
			t.Cleanup(func() {
				cancel()
				<-runErr
				_ = p.Shutdown(context.Background())
			})
			return p, addr
		case err = <-runErr:
		case <-time.After(10 * time.Second):
			err = errors.New("s3 gateway did not become ready")
		}
		cancel()
		_ = p.Shutdown(context.Background())
		if !errors.Is(err, syscall.EADDRINUSE) || i == attempts {
			t.Fatalf("s3 gateway on %s: %v", addr, err)
		}
	}
}

// TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken: freeLoopbackAddr releases the port it reserves, so a
// parallel test can bind it before the gateway does. The gateway must still come up, on another port, instead of
// the test waiting out the readiness timeout.
func TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken(t *testing.T) {
	dataDir, err := os.MkdirTemp("", "funcd")
	if err != nil {
		t.Fatalf("data dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	var taken net.Listener
	reserve := func(t *testing.T) string {
		addr := freeLoopbackAddr(t)
		if taken == nil {
			l, lerr := net.Listen("tcp", addr)
			if lerr != nil {
				t.Fatalf("take the reserved port: %v", lerr)
			}
			t.Cleanup(func() { _ = l.Close() })
			taken = l
		}
		return addr
	}
	_, addr := startS3Gateway(t, reserve, func(addr string) (*Platform, error) {
		return New(InMemory(), WithS3Gateway(addr, "", 0, "", dataDir))
	})
	if addr == taken.Addr().String() {
		t.Fatalf("the gateway reports the port another listener holds: %s", addr)
	}
	conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial s3 gateway listener: %v", derr)
	}
	_ = conn.Close()
}

// TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite: a Bucket's spec.maxObjectBytes caps every write into
// it (ADR-0080). Through the real S3 gateway the owner's PutObject past the cap is 403 and stores nothing, and
// one at the cap lands; the Bucket view context.blob and the site reconciler write through refuses it too.
func TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	p, addr := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), WithS3Gateway(addr, "", 0, "", dataDir))
	})

	st := p.cfg.store
	bucket := &v1.Bucket{TypeMeta: v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket}}
	bucket.Name, bucket.Namespace, bucket.ResourceGroup = "lake", "default", "rg1"
	bucket.Spec = v1.BucketSpec{Prefixes: []v1.BucketPrefix{{Name: "raw", Owner: "writer"}}, MaxObjectBytes: 16}
	writer := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
	writer.Name, writer.Namespace, writer.ResourceGroup = "writer", "default", "rg1"
	writer.Spec = v1.FunctionSpec{Runtime: "nodejs22"}
	for _, obj := range []v1.Object{bucket, writer} {
		if _, cerr := st.Create(ctx, obj); cerr != nil {
			t.Fatalf("seed %s: %v", obj.GroupVersionKind().Kind, cerr)
		}
	}

	master, err := s3gateway.LoadOrCreateMaster("", dataDir)
	if err != nil {
		t.Fatalf("load master: %v", err)
	}
	kp := s3gateway.DeriveKeypair(master, "default", "writer")
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(kp.AccessKey, kp.SecretKey, "")),
	)
	if err != nil {
		t.Fatalf("aws config: %v", err)
	}
	endpoint := "http://" + addr
	client := awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	put := func(key string, n int) error {
		_, perr := client.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String("lake"), Key: aws.String(key), Body: bytes.NewReader(make([]byte, n)),
		})
		return perr
	}

	var re interface{ HTTPStatusCode() int }
	if perr := put("raw/big.parquet", 20); !errors.As(perr, &re) || re.HTTPStatusCode() != http.StatusForbidden {
		t.Fatalf("a 20-byte PutObject into a Bucket with maxObjectBytes=16 must be 403, got %v", perr)
	}
	view, ok := s3BucketFor(p.cfg.blob, st)("default", "lake")
	if !ok {
		t.Fatal("the Bucket view must resolve")
	}
	if found, _ := view.Exists(ctx, "raw/big.parquet"); found {
		t.Fatal("the over-cap object must not be stored")
	}
	if perr := put("raw/ok.parquet", 16); perr != nil {
		t.Fatalf("an object at the cap must land: %v", perr)
	}
	if perr := view.Put(ctx, "raw/direct.parquet", make([]byte, 20)); fault.KindOf(perr) != fault.Forbidden {
		t.Fatalf("the Bucket view must refuse an over-cap Put as Forbidden, got %v", perr)
	}
}
