package s3gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/versity/versitygw/backend"
	"github.com/versity/versitygw/s3api"
	"github.com/versity/versitygw/s3api/middlewares"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	authz "github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/blob"
)

type denyAll struct{}

func (denyAll) Authorize(context.Context, authz.Request) (authz.Decision, error) {
	return authz.Decision{}, nil
}

// TestIssue583_RunReturnsWhenCloseLandsBeforeServe: versitygw fires its OnListen hook after the bind and before
// fasthttp's Serve registers the listener, so a Close in that window finds no listener to shut down. Run must still
// return after cancel instead of waiting forever on the Serve that starts after the Close.
func TestIssue583_RunReturnsWhenCloseLandsBeforeServe(t *testing.T) {
	master := []byte("master")
	srv, err := New(Deps{
		BucketFor: func(v1.NamespaceName, string) (blob.Bucket, bool) { return nil, false },
		Buckets:   func(context.Context, v1.NamespaceName) ([]v1.Bucket, error) { return nil, nil },
		PDP:       denyAll{},
		Master:    master,
		Listen:    "127.0.0.1:0",
	})
	require.NoError(t, err)
	// The OnListen hook runs on the serving goroutine just before Serve, so a Close inside it lands in the window.
	srv.api, err = s3api.New(backend.BackendUnsupported{}, middlewares.RootUserConfig{Access: "root", Secret: "root"},
		region, &iam{master: master}, nil, nil, nil, nil,
		s3api.WithOnListen(func() {
			_ = srv.Close()
			srv.signalReady()
		}))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- srv.Run(ctx) }()
	require.NoError(t, srv.Wait(context.Background()))
	cancel()
	select {
	case err = <-ran:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		_ = srv.api.ShutDown()
		<-ran
		t.Fatal("Run did not return after cancel: Close shut the gateway down before Serve registered its listener")
	}
}
