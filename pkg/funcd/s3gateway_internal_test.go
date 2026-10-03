package funcd

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
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

// testSources is read through the embed so a `go test -overlay` revert check sees the overlaid source.
//
//go:embed *_test.go
var testSources embed.FS

// TestIssue517_FreeLoopbackAddrDefinedOnce: the internal and the external (e2e) test packages share one
// freeLoopbackAddr; the external one reaches it through export_test.go.
func TestIssue517_FreeLoopbackAddrDefinedOnce(t *testing.T) {
	t.Parallel()
	files, err := fs.Glob(testSources, "*_test.go")
	if err != nil {
		t.Fatalf("glob test sources: %v", err)
	}
	fset := token.NewFileSet()
	var defs []string
	for _, name := range files {
		src, err := testSources.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == "freeLoopbackAddr" {
				defs = append(defs, fset.Position(fn.Pos()).String())
			}
		}
	}
	if len(defs) != 1 {
		t.Fatalf("freeLoopbackAddr is defined %d times, want 1: %v", len(defs), defs)
	}
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
	logger, serve := platformRun()
	_, addr := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), logger, WithS3Gateway(addr, "", 0, "", t.TempDir()))
	}, serve)
	conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial s3 gateway listener: %v", derr)
	}
	_ = conn.Close()
}

// startS3Gateway builds a platform whose S3 gateway listens on an address from reserve, starts it with serve
// (which returns the gateway's Run error once it has stopped) and waits until the gateway is ready. versitygw
// binds the address itself (ADR-0085), so a test can only reserve a port and release it, and a parallel test can
// take it before the gateway binds it (#288): that bind collision rebuilds the platform on a fresh address; any
// other Run error fails the test.
func startS3Gateway(t *testing.T, reserve func(*testing.T) string, build func(addr string) (*Platform, error),
	serve func(context.Context, *Platform) error) (*Platform, string) {
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
		go func() { runErr <- serve(runCtx, p) }()
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

// s3GatewayRun serves only the platform's S3 gateway.
func s3GatewayRun(ctx context.Context, p *Platform) error { return p.s3gw.Run(ctx) }

// platformRun serves the whole platform, which starts the S3 gateway itself. Platform.Run returns a bind error
// (#497) and logs any later gateway stop, so the returned logger option also hands that logged error to serve.
func platformRun() (Option, func(context.Context, *Platform) error) {
	stopped := make(gatewayStopped, 1)
	return WithLogger(slog.New(stopped)), func(ctx context.Context, p *Platform) error {
		ctx, stop := context.WithCancel(ctx)
		defer stop()
		ran := make(chan error, 1)
		go func() { ran <- p.Run(ctx) }()
		select {
		case err := <-stopped:
			stop()
			<-ran
			return err
		case err := <-ran:
			return err
		}
	}
}

// gatewayStopped is a slog handler that passes on the error of the platform's "s3 gateway stopped" record.
type gatewayStopped chan error

func (gatewayStopped) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelError }

func (h gatewayStopped) Handle(_ context.Context, r slog.Record) error {
	if r.Message != "s3 gateway stopped" {
		return nil
	}
	r.Attrs(func(a slog.Attr) bool {
		err, ok := a.Value.Any().(error)
		if ok {
			select {
			case h <- err:
			default:
			}
		}
		return !ok
	})
	return nil
}

func (h gatewayStopped) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h gatewayStopped) WithGroup(string) slog.Handler { return h }

// takenPortReserve returns a reserve whose first address another listener, which taken returns, already holds: the
// bind collision of #288, made deterministic.
func takenPortReserve() (reserve func(*testing.T) string, taken func() net.Listener) {
	var held net.Listener
	reserve = func(t *testing.T) string {
		addr := freeLoopbackAddr(t)
		if held == nil {
			l, err := net.Listen("tcp", addr)
			if err != nil {
				t.Fatalf("take the reserved port: %v", err)
			}
			t.Cleanup(func() { _ = l.Close() })
			held = l
		}
		return addr
	}
	return reserve, func() net.Listener { return held }
}

// TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken: freeLoopbackAddr releases the port it reserves, so a
// parallel test can bind it before the gateway does. The gateway must still come up, on another port, instead of
// the test waiting out the readiness timeout, whether the test serves the gateway alone or the whole platform.
func TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken(t *testing.T) {
	logger, viaPlatform := platformRun()
	for _, tc := range []struct {
		name  string
		opts  []Option
		serve func(context.Context, *Platform) error
	}{
		{name: "gateway Run", serve: s3GatewayRun},
		{name: "platform Run", opts: []Option{logger}, serve: viaPlatform},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir, err := os.MkdirTemp("", "funcd")
			if err != nil {
				t.Fatalf("data dir: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
			reserve, taken := takenPortReserve()
			_, addr := startS3Gateway(t, reserve, func(addr string) (*Platform, error) {
				return New(append([]Option{InMemory(), WithS3Gateway(addr, "", 0, "", dataDir)}, tc.opts...)...)
			}, tc.serve)
			if addr == taken().Addr().String() {
				t.Fatalf("the gateway reports the port another listener holds: %s", addr)
			}
			conn, derr := net.DialTimeout("tcp", addr, 2*time.Second)
			if derr != nil {
				t.Fatalf("dial s3 gateway listener: %v", derr)
			}
			_ = conn.Close()
		})
	}
}

// TestIssue497_RunFailsWhenS3GatewayCannotBind: the gateway binds its address only when Run serves it (ADR-0085).
// When another listener holds that address, Run must fail at startup with the bind error and stop the platform,
// instead of serving the control plane and the data plane without the S3 frontend.
func TestIssue497_RunFailsWhenS3GatewayCannotBind(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold the gateway address: %v", err)
	}
	t.Cleanup(func() { _ = held.Close() })
	dataDir, err := os.MkdirTemp("", "funcd")
	if err != nil {
		t.Fatalf("data dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dataDir) })
	p, err := New(InMemory(), WithS3Gateway(held.Addr().String(), "", 0, "", dataDir))
	if err != nil {
		t.Fatalf("New with the S3 gateway on %s: %v", held.Addr(), err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- p.Run(ctx) }()
	select {
	case err = <-ran:
	case <-time.After(10 * time.Second):
		cancel()
		<-ran
		t.Fatal("Run kept serving the node without its S3 gateway")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("Run must fail with the gateway's bind error, got %v", err)
	}
	if conn, derr := net.DialTimeout("tcp", p.Addr(), time.Second); derr == nil {
		_ = conn.Close()
		t.Fatal("the control plane must not keep listening after the S3 gateway failed to bind")
	}
}

// TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite: a Bucket's spec.maxObjectBytes caps every write into
// it (ADR-0080). Through the real S3 gateway the owner's PutObject past the cap is 403 and stores nothing, and
// one at the cap lands; the Bucket view context.blob and the site reconciler write through refuses it too.
func TestIssue109_BucketMaxObjectBytesForbidsOversizeWrite(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	p, addr := startS3Gateway(t, freeLoopbackAddr, func(addr string) (*Platform, error) {
		return New(InMemory(), WithS3Gateway(addr, "", 0, "", dataDir))
	}, s3GatewayRun)

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
