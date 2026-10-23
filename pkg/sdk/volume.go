package sdk

import (
	"context"
	"net/http"
	"net/url"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// DeleteVolume removes a retained Function volume (ADR-0262 Decision 8): NotFound with none, Conflict while a live
// Function holds it.
func (c *Client) DeleteVolume(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error {
	const op = "sdk.DeleteVolume"
	if ns == "" || fn == "" || name == "" {
		return fault.Invalidf(op, "namespace, function and volume are required")
	}
	u, err := c.namespaceURL(ns)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, u+"/volumes/"+url.PathEscape(string(fn))+"/"+url.PathEscape(string(name)), nil)
	return err
}
