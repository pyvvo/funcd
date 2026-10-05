//go:build e2e

package funcd_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/blob/s3gateway"
	"github.com/pyvvo/funcd/internal/bus/nats"
	"github.com/pyvvo/funcd/internal/gateway/embedded"
	"github.com/pyvvo/funcd/internal/runtime/process"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// pushSiteBundle writes files into a dir and pushes it as a site artifact under tag, returning the ref.
func pushSiteBundle(t *testing.T, layout, tag string, files map[string]string) (ref, digest string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dist-"+tag)
	for name, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	ref = "oci-layout://" + layout + ":" + tag
	digest, err := artifact.PushSite(context.Background(), ref, dir)
	require.NoError(t, err)
	return ref, digest
}

// scenarios: site-materializes-and-serves + data-mount-serves-sibling-prefix + data-mount-does-not-spa-
// fallback + redeploy-swaps-atomically + bundle-prefix-has-no-writer (e2e, F103/ADR-0139) — a real
// funcd: a pushed site bundle is applied as a Site, the reconciler materializes it under the
// digest-scoped prefix and owns the Bucket + Route, the live edge serves the index and the assets, a
// sibling `gold` data mount serves its object and 404s a miss (never the SPA shell), a redeploy is
// observed with no request ever failing during the swap, and the site prefix is unwritable through the
// REAL S3 frontend by both a bound Function and an external Identity.
func TestScenarioE2ESite(t *testing.T) {
	ctx := context.Background()
	layout := filepath.Join(t.TempDir(), "layout")
	refA, digestA := pushSiteBundle(t, layout, "v1", map[string]string{
		"index.html":   "<!doctype html><title>bi-A</title>",
		"img/logo.png": "\x89PNG\r\n\x1a\nlogo-bytes",
	})
	refB, digestB := pushSiteBundle(t, layout, "v2", map[string]string{
		"index.html": "<!doctype html><title>bi-B</title>",
		"app.js":     "console.log('B')",
	})
	p, st, s3Addr := startSitePlatform(t, funcd.FreeLoopbackAddr, refA)

	getSite := func() *v1.Site {
		obj, gerr := st.Get(ctx, v1.KindSite.GVK(), siteNS, "bi")
		require.NoError(t, gerr)
		return obj.(*v1.Site)
	}
	waitReady := func(digest string) {
		t.Helper()
		require.Eventually(t, func() bool {
			s := getSite()
			return s.Status.Phase == v1.PhaseReady && s.Status.Digest == digest
		}, 20*time.Second, 100*time.Millisecond, "site Ready at %s; last status %+v", digest, getSite().Status)
	}
	waitReady(digestA)

	// The owned resources: the Bucket (site prefix ownerless + the declared gold prefix, no OwnerReference)
	// and the Route (owner-stamped, data mount + bundle rule).
	bobj, err := st.Get(ctx, v1.KindBucket.GVK(), siteNS, "reports")
	require.NoError(t, err)
	b := bobj.(*v1.Bucket)
	require.Equal(t, []v1.BucketPrefix{{Name: "bi"}, {Name: "gold", Owner: "etl"}}, b.Spec.Prefixes)
	require.Empty(t, b.OwnerReferences)
	robj, err := st.Get(ctx, v1.KindRoute.GVK(), siteNS, "bi")
	require.NoError(t, err)
	rt := robj.(*v1.Route)
	require.Len(t, rt.OwnerReferences, 1)
	require.Equal(t, v1.KindSite, rt.OwnerReferences[0].Kind)
	s := getSite()
	require.Equal(t, "bi/"+strings.ReplaceAll(digestA, ":", "-")+"/", s.Status.ServingPrefix)
	require.Equal(t, 2, s.Status.Objects)

	base := "http://" + p.DataPlaneAddr()
	get := func(path string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.Host = "bi.example.com"
		resp, gerr := http.DefaultClient.Do(req)
		require.NoError(t, gerr)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(body), resp.Header
	}

	// site-materializes-and-serves
	code, body, _ := get("/")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "<title>bi-A</title>")
	code, body, hdr := get("/img/logo.png")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "image/png", hdr.Get("Content-Type"))
	require.Contains(t, hdr.Get("ETag"), "W/")
	require.Equal(t, "\x89PNG\r\n\x1a\nlogo-bytes", body)

	// data-mount-serves-sibling-prefix
	code, body, _ = get("/data/part-0.parquet")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "PAR1-gold-rows", body)

	// data-mount-does-not-spa-fallback: a miss under the data mount is 404, never the app shell — while
	// the SPA bundle rule still serves the index for an unknown app path.
	code, body, _ = get("/data/missing.parquet")
	require.Equal(t, http.StatusNotFound, code)
	require.NotContains(t, body, "<title>bi-A</title>")
	code, body, _ = get("/some/client/route")
	require.Equal(t, http.StatusOK, code, "the SPA fallback applies to the bundle rule")
	require.Contains(t, body, "<title>bi-A</title>")

	// redeploy-swaps-atomically: hammer "/" while the spec moves to B (a status-wiping apply); every
	// response is a 200 carrying either the A or the B index — never a miss, never a 5xx.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var bad []string
	var badMu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			c, bd, _ := get("/")
			if c != http.StatusOK || (!strings.Contains(bd, "bi-A") && !strings.Contains(bd, "bi-B")) {
				badMu.Lock()
				bad = append(bad, bd)
				badMu.Unlock()
			}
		}
	}()
	cur := getSite()
	cur.Spec.Image = refB
	cur.Status = v1.SiteStatus{}
	_, err = st.Update(ctx, cur)
	require.NoError(t, err)
	waitReady(digestB)
	require.Eventually(t, func() bool { _, bd, _ := get("/"); return strings.Contains(bd, "bi-B") }, 10*time.Second, 50*time.Millisecond)
	close(stop)
	wg.Wait()
	require.Empty(t, bad, "no request was served from a partially-written prefix or missed during the swap")
	code, body, _ = get("/app.js")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "console.log('B')", body)
	require.Equal(t, "bi/"+strings.ReplaceAll(digestB, ":", "-")+"/", getSite().Status.ServingPrefix)

	// bundle-prefix-has-no-writer: through the REAL S3 frontend, a PutObject into the serving prefix is
	// 403 for the bound Function (its binding grants read only) and for the external Identity (no writer
	// role); a read of the bound prefix by the Function still works (the binding is a read grant).
	key := "bi/" + strings.ReplaceAll(digestB, ":", "-") + "/index.html"
	kp := s3gateway.DeriveKeypair([]byte(siteMaster), v1.KindFunction, siteNS, "uploader")
	fnClient := s3Client(t, "http://"+s3Addr, kp.AccessKey, kp.SecretKey)
	_, err = fnClient.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("reports"), Key: aws.String(key), Body: bytes.NewReader([]byte("defaced"))})
	require.Equal(t, http.StatusForbidden, s3Status(err), "a bound Function cannot write the site prefix: %v", err)
	out, err := fnClient.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String("reports"), Key: aws.String(key)})
	require.NoError(t, err, "the binding still grants the Function a read")
	_ = out.Body.Close()

	var access, secret string
	require.Eventually(t, func() bool {
		sobj, gerr := st.Get(ctx, v1.KindSecret.GVK(), siteNS, "publisher")
		if gerr != nil {
			return false
		}
		sec := sobj.(*v1.Secret)
		access, secret = string(sec.Spec.Data["accessKeyId"]), string(sec.Spec.Data["secretAccessKey"])
		return access != "" && secret != ""
	}, 10*time.Second, 100*time.Millisecond, "the Identity reconciler issues the external keypair")
	extClient := s3Client(t, "http://"+s3Addr, access, secret)
	_, err = extClient.PutObject(ctx, &awss3.PutObjectInput{Bucket: aws.String("reports"), Key: aws.String(key), Body: bytes.NewReader([]byte("defaced"))})
	require.Equal(t, http.StatusForbidden, s3Status(err), "an external Identity cannot write the site prefix: %v", err)

	// ...and the site still serves exactly what was deployed.
	code, body, _ = get("/")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "<title>bi-B</title>")
}

// siteNS and siteMaster are the namespace and the node S3 master secret of the site e2e platform.
const (
	siteNS     = "openteam"
	siteMaster = "e2e-node-master-secret-0123456789"
)

// startSitePlatform runs a platform whose substrate holds the `bi` Site at image, the sibling `gold` data the app
// fetches, a Function bound to the site prefix (read grant only) and an external Identity with no role assignment.
// Its S3 gateway listens on an address from reserve, through the #288 retry: a start that loses its port shuts the
// platform down with its substrate, so each attempt builds and seeds its own (#464).
func startSitePlatform(t *testing.T, reserve func(*testing.T) string, image string) (*funcd.Platform, store.Store, string) {
	t.Helper()
	master := filepath.Join(t.TempDir(), "master.key")
	require.NoError(t, os.WriteFile(master, []byte(siteMaster), 0o600))
	var st store.Store
	p, s3Addr := funcd.StartWithS3Gateway(t, reserve, func(s3Addr string, logger funcd.Option) (*funcd.Platform, error) {
		ctx := context.Background()
		bucket, err := gocloud.Open(ctx, "mem://")
		require.NoError(t, err)
		messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
		require.NoError(t, err)
		st = store.New(memory.New())
		// Written straight into the substrate: the per-namespace view the S3 frontend, the static handler, and the
		// Site reconciler share.
		require.NoError(t, bucket.Put(ctx, "s3/"+siteNS+"/reports/gold/part-0.parquet", []byte("PAR1-gold-rows"), blob.PutOptions{}))

		siteObj := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
		siteObj.Name, siteObj.Namespace, siteObj.ResourceGroup = "bi", siteNS, "rg1"
		siteObj.Spec = v1.SiteSpec{
			Image:   image,
			Bucket:  v1.SiteBucket{Name: "reports", Prefixes: []v1.BucketPrefix{{Name: "gold", Owner: "etl"}}},
			Prefix:  "bi",
			SPA:     true,
			Ingress: v1.SiteIngress{Host: "bi.example.com", Public: true, Rules: []v1.SiteRule{{Path: "/data", Prefix: "gold"}}},
		}
		seed(t, st, siteObj)
		uploader := &v1.Function{TypeMeta: v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}}
		uploader.Name, uploader.Namespace, uploader.ResourceGroup = "uploader", siteNS, "rg1"
		uploader.Spec = v1.FunctionSpec{Runtime: "nodejs22", Blob: []v1.FunctionBlob{{Alias: "bi", Bucket: "reports", Prefix: "bi"}}}
		seed(t, st, uploader)
		ext := &v1.Identity{TypeMeta: v1.TypeMeta{APIVersion: v1.KindIdentity.GVK().APIVersion(), Kind: v1.KindIdentity}}
		ext.Name, ext.Namespace, ext.ResourceGroup = "publisher", siteNS, "rg1"
		ext.Spec = v1.IdentitySpec{Type: v1.IdentityTypeExternal}
		seed(t, st, ext)

		return funcd.New(
			funcd.WithBlob(bucket), funcd.WithBus(messaging),
			funcd.WithStore(st), funcd.WithRuntime(process.New()),
			funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
			funcd.WithDataPlaneAddr("127.0.0.1:0"),
			funcd.WithS3Gateway(s3Addr, "", 0, master, shortDataDir(t)),
			funcd.WithDevAuth(funcd.DevToken, siteNS),
			funcd.WithArtifactStore(shortDataDir(t)),
			logger,
		)
	})
	return p, st, s3Addr
}

// TestIssue464_SiteS3GatewayStartsWhenItsReservedPortIsTaken: the site scenario reserves its S3 gateway port and
// releases it, so another listener can bind it before the gateway does. The platform must still serve S3 at the
// address the scenario's clients use.
func TestIssue464_SiteS3GatewayStartsWhenItsReservedPortIsTaken(t *testing.T) {
	ref, _ := pushSiteBundle(t, filepath.Join(t.TempDir(), "layout"), "v1", map[string]string{"index.html": "<!doctype html>"})
	reserve, taken := funcd.TakenPortReserve()
	_, _, s3Addr := startSitePlatform(t, reserve, ref)
	require.NotEqual(t, taken().Addr().String(), s3Addr, "the S3 clients target the port another listener holds")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := s3Client(t, "http://"+s3Addr, "unknown", "unknown").ListBuckets(ctx, &awss3.ListBucketsInput{})
	require.Equal(t, http.StatusForbidden, s3Status(err), "the S3 gateway rejects an unknown key: %v", err)
}

// s3Client builds a real aws-sdk-go-v2 client (path-style, static keys) against the in-process gateway.
func s3Client(t *testing.T, endpoint, access, secret string) *awss3.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(awscreds.NewStaticCredentialsProvider(access, secret, "")),
	)
	require.NoError(t, err)
	return awss3.NewFromConfig(cfg, func(o *awss3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
}

// s3Status extracts the HTTP status from an aws-sdk error (0 when none).
func s3Status(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

// scenarios: path-mounted-site-serves-bundle + path-mount-respects-segment-boundary +
// data-mount-nested-under-bundle-path + spa-fallback-scoped-to-the-mount + two-hostless-sites-coexist
// (e2e, F104/ADR-0140) — two Sites on ONE listener with NO Host header and no DNS: `bi` mounted at /bi
// with its gold data nested at /bi/data, and a host-less `docs` at its by-name default /site/docs. Both
// reach Ready (so neither Route conflicted), the SPA fallback stays inside its own mount, and /binary
// proves the mount respects segment boundaries.
func TestScenarioE2EPathMountedSites(t *testing.T) {
	ctx := context.Background()
	bucket, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	messaging, err := nats.Open(ctx, nats.Options{Storage: nats.MemoryStorage})
	require.NoError(t, err)
	layout := filepath.Join(t.TempDir(), "layout")
	refBI, _ := pushSiteBundle(t, layout, "bi", map[string]string{
		"index.html":    "<!doctype html><title>bi-app</title>",
		"assets/app.js": "console.log('bi')",
	})
	refDocs, _ := pushSiteBundle(t, layout, "docs", map[string]string{
		"index.html": "<!doctype html><title>docs-app</title>",
	})

	const ns = "openteam"
	st := store.New(memory.New())
	require.NoError(t, bucket.Put(ctx, "s3/"+ns+"/reports/gold/part-0.parquet", []byte("PAR1-gold-rows"), blob.PutOptions{}))

	mkSite := func(name, ref string, mutate func(*v1.Site)) {
		s := &v1.Site{TypeMeta: v1.TypeMeta{APIVersion: v1.KindSite.GVK().APIVersion(), Kind: v1.KindSite}}
		s.Name, s.Namespace, s.ResourceGroup = v1.ObjectName(name), ns, "rg1"
		s.Spec = v1.SiteSpec{
			Image:  ref,
			Bucket: v1.SiteBucket{Name: "reports"},
			Prefix: name,
		}
		mutate(s)
		seed(t, st, s)
	}
	// `bi`: an explicit /bi mount, SPA on, with the gold layer nested at /bi/data.
	mkSite("bi", refBI, func(s *v1.Site) {
		s.Spec.SPA = true
		s.Spec.Ingress = v1.SiteIngress{
			Path:   "/bi",
			Public: true,
			Rules: []v1.SiteRule{
				{
					Path:   "/bi/data",
					Prefix: "gold",
				},
			},
		}
	})
	// `docs`: no host, no path ⇒ the by-name default /site/docs.
	mkSite("docs", refDocs, func(s *v1.Site) { s.Spec.Ingress = v1.SiteIngress{Public: true} })

	p, err := funcd.New(
		funcd.WithBlob(bucket), funcd.WithBus(messaging),
		funcd.WithStore(st), funcd.WithRuntime(process.New()),
		funcd.WithGateway(embedded.New()), funcd.WithListenAddr("127.0.0.1:0"),
		funcd.WithDataPlaneAddr("127.0.0.1:0"),
		funcd.WithDevAuth(funcd.DevToken, ns),
		funcd.WithArtifactStore(t.TempDir()),
	)
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	doneCh := make(chan error, 1)
	go func() { doneCh <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-doneCh })

	// two-hostless-sites-coexist: BOTH reach Ready — neither owned Route was RouteConflict.
	for _, name := range []string{"bi", "docs"} {
		require.Eventually(t, func() bool {
			obj, gerr := st.Get(ctx, v1.KindSite.GVK(), ns, v1.ObjectName(name))
			return gerr == nil && obj.(*v1.Site).Status.Phase == v1.PhaseReady
		}, 20*time.Second, 100*time.Millisecond, "site %s Ready", name)
	}

	base := "http://" + p.DataPlaneAddr()
	get := func(path string) (int, string) {
		t.Helper()
		// NO Host header: the mounts are reached by path alone.
		resp, gerr := http.Get(base + path) //nolint:noctx // test client
		require.NoError(t, gerr)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(body)
	}

	// path-mounted-site-serves-bundle: the mount serves the index with and without a trailing slash.
	for _, path := range []string{"/bi/", "/bi"} {
		code, body := get(path)
		require.Equal(t, http.StatusOK, code, "GET %s", path)
		require.Contains(t, body, "<title>bi-app</title>", "GET %s", path)
	}
	code, body := get("/bi/assets/app.js")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "console.log('bi')", body)

	// path-mount-respects-segment-boundary: /binary is NOT captured by the /bi mount.
	code, body = get("/binary")
	require.Equal(t, http.StatusNotFound, code)
	require.NotContains(t, body, "bi-app")

	// data-mount-nested-under-bundle-path: the nested rule wins over the bundle rule…
	code, body = get("/bi/data/part-0.parquet")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "PAR1-gold-rows", body)
	// …and a miss there is 404, never the app shell, despite spa: true on the bundle rule.
	code, body = get("/bi/data/missing.parquet")
	require.Equal(t, http.StatusNotFound, code)
	require.NotContains(t, body, "bi-app")

	// spa-fallback-scoped-to-the-mount: an unknown path UNDER /bi serves the shell; outside it does not.
	code, body = get("/bi/client/route")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "<title>bi-app</title>")
	code, body = get("/elsewhere")
	require.Equal(t, http.StatusNotFound, code)
	require.NotContains(t, body, "bi-app")

	// the by-name default mount serves the second site on the same listener.
	code, body = get("/site/docs/")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "<title>docs-app</title>")
}
