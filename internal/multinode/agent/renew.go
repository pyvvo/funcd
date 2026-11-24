package agent

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/pyvvo/funcd/internal/multinode"
	"github.com/pyvvo/funcd/internal/platform/durable"
)

// renewEvery is how often the node looks whether its certificate is to be renewed.
const renewEvery = time.Hour

// renew replaces the node certificate in the last third of its life, so a node that reaches its control plane never has to
// join again.
func (a *Agent) renew(ctx context.Context) {
	for ctx.Err() == nil {
		if multinode.RenewalDue(a.id.Load().notAfter, a.cfg.Now()) {
			if err := a.renewCertificate(ctx); err != nil && ctx.Err() == nil {
				a.log.WarnContext(ctx, "node agent: renew the node certificate", "notAfter", a.id.Load().notAfter, "error", err)
			}
		}
		sleep(ctx, renewEvery)
	}
}

// renewCertificate takes a new certificate, on disk first, then uses it, which ends the old one. The polls under way end on
// the old one's connections.
func (a *Agent) renewCertificate(ctx context.Context) error {
	id := a.id.Load()
	key, csr, err := newKey(id.name)
	if err != nil {
		return err
	}
	var out multinode.JoinResponse
	if _, err := a.do(ctx, http.MethodPost, multinode.PathRenew, multinode.RenewRequest{CSR: csr}, &out); err != nil {
		return err
	}
	pair, err := identityPEM(out.Certificate, key)
	if err != nil {
		return err
	}
	next, err := id.with(pair)
	if err != nil {
		return err
	}
	if err := (durable.Disk{}).WriteFile(filepath.Clean(a.cfg.Dir), identityFile, pair, 0o600); err != nil {
		return err
	}
	a.id.Store(&next)
	id.client.CloseIdleConnections()
	a.log.InfoContext(ctx, "node agent: node certificate renewed", "notAfter", next.notAfter)
	_, err = a.do(ctx, http.MethodPut, multinode.PathRenew, nil, nil)
	return err
}
