package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// JoinToken is a minted token and its expiry: a worker node's one-time join token (FEAT-0002), which funcd node join
// swaps for the node's certificate, or an API token (F248).
type JoinToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// MintJoinToken mints a one-time join token for the declared WorkerNode node, valid for ttl, a duration such as "30m"
// ("" ⇒ an hour). An earlier unused token of the node stops working.
func (c *Client) MintJoinToken(ctx context.Context, node v1.ObjectName, ttl string) (JoinToken, error) {
	return c.mintToken(ctx, "sdk.MintJoinToken", v1.KindWorkerNode, node, ttl)
}

// MintAPIToken mints the token of the declared APIToken name (FEAT-0002 F248), valid for ttl, a duration such as "720h"
// ("" ⇒ 30 days). The token before it stops working.
func (c *Client) MintAPIToken(ctx context.Context, name v1.ObjectName, ttl string) (JoinToken, error) {
	return c.mintToken(ctx, "sdk.MintAPIToken", v1.KindAPIToken, name, ttl)
}

// mintToken posts to the token route of the cluster-scoped object kind/name.
func (c *Client) mintToken(ctx context.Context, op string, kind v1.Kind, name v1.ObjectName, ttl string) (JoinToken, error) {
	u, err := c.itemURL(kind, "", name)
	if err != nil {
		return JoinToken{}, err
	}
	body, err := json.Marshal(map[string]string{"ttl": ttl})
	if err != nil {
		return JoinToken{}, fault.Internalf(op, "marshal: %v", err)
	}
	data, err := c.do(ctx, http.MethodPost, u+"/token", body)
	if err != nil {
		return JoinToken{}, err
	}
	var out JoinToken
	if err := json.Unmarshal(data, &out); err != nil {
		return JoinToken{}, fault.Internalf(op, "decode the token: %v", err)
	}
	return out, nil
}
