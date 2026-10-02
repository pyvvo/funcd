package funcd

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
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
	addr := freeLoopbackAddr(t)
	p, err := New(InMemory(), WithS3Gateway(addr, "", 0, "", t.TempDir()))
	if err != nil {
		t.Fatalf("New(InMemory, WithS3Gateway): %v", err)
	}
	if p.s3gw == nil {
		t.Fatal("WithS3Gateway must wire the S3 gateway")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	t.Cleanup(func() { cancel(); _ = p.Shutdown(context.Background()) })

	// Wait until the gateway listener is ready (bound + accepting).
	select {
	case <-p.s3gw.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("s3 gateway did not become ready")
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
	addr := freeLoopbackAddr(t)
	dataDir := t.TempDir()
	p, err := New(InMemory(), WithS3Gateway(addr, "", 0, "", dataDir))
	if err != nil {
		t.Fatalf("New(InMemory, WithS3Gateway): %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

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

	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = p.s3gw.Run(runCtx) }()
	t.Cleanup(cancel)
	select {
	case <-p.s3gw.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("s3 gateway did not become ready")
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
