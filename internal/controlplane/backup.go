package controlplane

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/auth"
	"github.com/pyvvo/funcd/internal/backup/runner"
	"github.com/pyvvo/funcd/internal/controlplane/middleware"
)

// BackupStatuser reads the platform backup's status (ADR-0205 Decision 4); *runner.Runner is one.
type BackupStatuser interface {
	Status(ctx context.Context) (runner.Status, error)
}

// PlatformBackupStatus is runner.Status under a schema name apart from v1alpha1.Status.
type PlatformBackupStatus runner.Status

type platformBackupOutput struct {
	Body PlatformBackupStatus
}

// RegisterPlatformBackup registers GET /apis/funcd.io/v1alpha1/platformbackup. It authorizes get on the
// cluster-scoped WorkerNode, so only an admin reads it; a failed listing of the target is 503. A nil s (no backup
// stream on) reads enabled: false.
func RegisterPlatformBackup(api huma.API, s BackupStatuser, authz auth.Authorizer) {
	huma.Register(api, huma.Operation{
		OperationID: "getPlatformBackup",
		Method:      http.MethodGet,
		Path:        "/apis/funcd.io/v1alpha1/platformbackup",
		Tags:        []string{"PlatformBackup"},
	}, func(ctx context.Context, _ *struct{}) (*platformBackupOutput, error) {
		if err := authorizePlatformBackup(ctx, authz); err != nil {
			return nil, wrapFaultError(err)
		}
		if s == nil {
			return &platformBackupOutput{}, nil
		}
		st, err := s.Status(ctx)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &platformBackupOutput{Body: PlatformBackupStatus(st)}, nil
	})
}

func authorizePlatformBackup(ctx context.Context, authz auth.Authorizer) error {
	const op = "controlplane.platformbackup"
	id, ok := middleware.IdentityFrom(ctx)
	if !ok {
		return fault.Unauthorizedf(op, "no authenticated identity")
	}
	dec, err := authz.Authorize(ctx, auth.Request{Identity: id, Verb: auth.VerbGet, Kind: v1.KindWorkerNode})
	if err != nil {
		return fault.Wrapf(err, fault.Internal, op, "authorize the platform backup status")
	}
	if !dec.Allowed {
		return fault.Forbiddenf(op, "platform backup status denied: %s", dec.Reason)
	}
	return nil
}

// RegisterStubPlatformBackup registers the platform backup route for spec generation.
func RegisterStubPlatformBackup(api huma.API) {
	RegisterPlatformBackup(api, nil, stubLogAuthorizer{})
}
