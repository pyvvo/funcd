package sdk

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/backup/runner"
)

// PlatformBackup reads the platform backup's status (ADR-0205); only an admin may.
func (c *Client) PlatformBackup(ctx context.Context) (runner.Status, error) {
	body, err := c.do(ctx, http.MethodGet, c.baseURL+apiPrefix+"/platformbackup", nil)
	if err != nil {
		return runner.Status{}, err
	}
	var st runner.Status
	if err := json.Unmarshal(body, &st); err != nil {
		return runner.Status{}, fault.Internalf("sdk.PlatformBackup", "decode the platform backup status: %v", err)
	}
	return st, nil
}
