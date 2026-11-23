package s3gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/versity/versitygw/backend"
	"github.com/versity/versitygw/s3api"
	"github.com/versity/versitygw/s3api/middlewares"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	authz "github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob"
)

type denyAll struct{}

func (denyAll) Authorize(context.Context, authz.Request) (authz.Decision, error) {
	return authz.Decision{}, nil
}

// TestIssue583_RunReturnsWhenCloseLandsBeforeServe: versitygw fires its OnListen hook after the bind and before
// fasthttp's Serve registers the listener. A Close in that window must still stop Run, as a clean stop: since v1.11,
// versitygw's ShutDown closes that listener itself, so Serve ends with "listener closed", which is no failure.
func TestIssue583_RunReturnsWhenCloseLandsBeforeServe(t *testing.T) {
	master := []byte("master")
	srv, err := New(Deps{
		BucketFor: func(v1.NamespaceName, string) (blob.Bucket, error) { return nil, fault.NotFoundf("test", "no bucket") },
		Buckets:   func(context.Context, v1.NamespaceName) ([]v1.Bucket, error) { return nil, nil },
		PDP:       denyAll{},
		Master:    master,
		Listen:    "127.0.0.1:0",
	})
	require.NoError(t, err)
	// The OnListen hook runs on the serving goroutine just before Serve, so a Close inside it lands in the window.
	srv.api, err = s3api.New(backend.BackendUnsupported{}, middlewares.RootUserConfig{Access: "root", Secret: "root"},
		region, &iam{derive: Deriver{Master: master}}, nil, nil, nil, nil,
		s3api.WithOnListen(func() {
			_ = srv.Close()
			srv.signalReady()
		}))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan error, 1)
	go func() { ran <- srv.Run(ctx) }()
	require.NoError(t, srv.Wait(context.Background()))
	select {
	case err = <-ran:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		cancel()
		<-ran
		t.Fatal("Run did not return after a Close that landed before Serve registered its listener")
	}
}
