package sdk

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// HoldStatus reads the platform hold's evidence (ADR-0206 Decision 8); admin-only.
func (c *Client) HoldStatus(ctx context.Context) (hold.Evidence, error) {
	body, err := c.do(ctx, http.MethodGet, c.baseURL+apiPrefix+"/hold", nil)
	if err != nil {
		return hold.Evidence{}, err
	}
	var st hold.Evidence
	if err := json.Unmarshal(body, &st); err != nil {
		return hold.Evidence{}, fault.Internalf("sdk.HoldStatus", "decode hold status: %v", err)
	}
	return st, nil
}

// ReleaseHold lifts the platform hold (ADR-0206 Decision 8). advance names blob EventSources, <ns>/<name>, whose
// listed keys are marked seen instead of replayed. Not held ⇒ fault.Conflict; an advance naming no blob event of an
// existing EventSource ⇒ fault.Invalid, both before any change.
func (c *Client) ReleaseHold(ctx context.Context, advance []string) error {
	body, err := json.Marshal(struct {
		Advance []string `json:"advance,omitempty"`
	}{advance})
	if err != nil {
		return fault.Internalf("sdk.ReleaseHold", "encode the release: %v", err)
	}
	_, err = c.do(ctx, http.MethodPost, c.baseURL+apiPrefix+"/hold/release", body)
	return err
}
