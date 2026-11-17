package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// JoinToken is a worker node's one-time join token (FEAT-0002), which funcd node join swaps for the node's certificate.
type JoinToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// MintJoinToken mints a one-time join token for the declared WorkerNode node, valid for ttl, a duration such as "30m"
// ("" ⇒ an hour). An earlier unused token of the node stops working.
func (c *Client) MintJoinToken(ctx context.Context, node v1.ObjectName, ttl string) (JoinToken, error) {
	const op = "sdk.MintJoinToken"
	u, err := c.itemURL(v1.KindWorkerNode, "", node)
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
		return JoinToken{}, fault.Internalf(op, "decode the join token: %v", err)
	}
	return out, nil
}
