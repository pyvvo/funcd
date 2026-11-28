package controlplane

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
)

// VolumeDeleter removes a retained local volume (ADR-0262 Decision 8): NotFound with none, Conflict while a live
// Function holds it.
type VolumeDeleter interface {
	DeleteVolume(ctx context.Context, ns v1.NamespaceName, fn, name v1.ObjectName) error
}

type volumeDeleteInput struct {
	Namespace v1.NamespaceName `path:"namespace" doc:"The Function's namespace."`
	Function  v1.ObjectName    `path:"function" doc:"The name of the Function that declared the volume."`
	Name      v1.ObjectName    `path:"name" doc:"The volume's name."`
}

// RegisterVolumes registers DELETE …/namespaces/{ns}/volumes/{function}/{name}, which `funcdctl volume delete` calls;
// it authorizes delete/Function in the namespace, the right to delete the volume's owner.
func RegisterVolumes(api huma.API, d VolumeDeleter, authz auth.Authorizer) {
	huma.Register(api, Describe(huma.Operation{
		OperationID: "deleteVolume",
		Method:      http.MethodDelete,
		Path:        "/apis/funcd.io/v1alpha1/namespaces/{namespace}/volumes/{function}/{name}",
		Tags:        []string{"Function"},
	}, "Delete a retained volume of a Function", unauthorized(), forbidden("delete Functions in the namespace"),
		problem(fault.NotFound, "No volume of that name exists for the Function in the namespace.",
			"Check the namespace, the Function's name and the volume's name."),
		badPath("namespace, the function or the name"), badQuery(), nodeAway(),
		problem(fault.Conflict, "The Function still exists and declares the volume.",
			"Delete the Function, or remove the volume from its spec, then retry."),
	), func(ctx context.Context, in *volumeDeleteInput) (*struct{}, error) {
		const op = "controlplane.volumes"
		id, ok := middleware.IdentityFrom(ctx)
		if !ok {
			return nil, wrapFaultError(fault.Unauthorizedf(op, "no authenticated identity"))
		}
		dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbDelete, Kind: v1.KindFunction, Namespace: in.Namespace})
		if err != nil {
			return nil, wrapFaultError(fault.Wrapf(err, fault.Internal, op, "authorize volume delete"))
		}
		if !dec.Allowed {
			return nil, wrapFaultError(fault.Forbiddenf(op, "volume delete in %q denied: %s", in.Namespace, dec.Reason))
		}
		return nil, wrapFaultError(d.DeleteVolume(ctx, in.Namespace, in.Function, in.Name))
	})
}

type stubVolumeDeleter struct{}

func (stubVolumeDeleter) DeleteVolume(context.Context, v1.NamespaceName, v1.ObjectName, v1.ObjectName) error {
	return nil
}

// RegisterStubVolumes registers the volume route with inert deps for spec generation.
func RegisterStubVolumes(api huma.API) {
	RegisterVolumes(api, stubVolumeDeleter{}, stubLogAuthorizer{})
}
