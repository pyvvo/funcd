package controlplane

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ===== Service (namespaced) =====

type createServiceInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Service
}
type serviceOutput struct{ Body v1.Service }
type listServiceOutput struct{ Body []v1.Service }

func registerService(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/services"

	huma.Register(api, huma.Operation{
		OperationID: "listServices", Method: http.MethodGet, Path: base,
		Tags: []string{"Service"},
	}, func(ctx context.Context, in *namespacedList) (*listServiceOutput, error) {
		items, err := h.ListServices(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listServiceOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createService", Method: http.MethodPost, Path: base,
		Tags: []string{"Service"},
	}, func(ctx context.Context, in *createServiceInput) (*serviceOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateService(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &serviceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getService", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Service"},
	}, func(ctx context.Context, in *namespacedGet) (*serviceOutput, error) {
		item, err := h.GetService(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &serviceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceService", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Service"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Service
	}) (*serviceOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceService(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &serviceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteService", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Service"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteService(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== EventSource (namespaced) =====

type createEventSourceInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.EventSource
}
type eventSourceOutput struct{ Body v1.EventSource }
type listEventSourceOutput struct{ Body []v1.EventSource }

func registerEventSource(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/eventsources"

	huma.Register(api, huma.Operation{
		OperationID: "listEventSources", Method: http.MethodGet, Path: base,
		Tags: []string{"EventSource"},
	}, func(ctx context.Context, in *namespacedList) (*listEventSourceOutput, error) {
		items, err := h.ListEventSources(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listEventSourceOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createEventSource", Method: http.MethodPost, Path: base,
		Tags: []string{"EventSource"},
	}, func(ctx context.Context, in *createEventSourceInput) (*eventSourceOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateEventSource(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &eventSourceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getEventSource", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"EventSource"},
	}, func(ctx context.Context, in *namespacedGet) (*eventSourceOutput, error) {
		item, err := h.GetEventSource(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &eventSourceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceEventSource", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"EventSource"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.EventSource
	}) (*eventSourceOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceEventSource(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &eventSourceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteEventSource", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"EventSource"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteEventSource(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== ConfigMap (namespaced) =====

type createConfigMapInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.ConfigMap
}
type configMapOutput struct{ Body v1.ConfigMap }
type listConfigMapOutput struct{ Body []v1.ConfigMap }

func registerConfigMap(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/configmaps"

	huma.Register(api, huma.Operation{
		OperationID: "listConfigMaps", Method: http.MethodGet, Path: base,
		Tags: []string{"ConfigMap"},
	}, func(ctx context.Context, in *namespacedList) (*listConfigMapOutput, error) {
		items, err := h.ListConfigMaps(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listConfigMapOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createConfigMap", Method: http.MethodPost, Path: base,
		Tags: []string{"ConfigMap"},
	}, func(ctx context.Context, in *createConfigMapInput) (*configMapOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateConfigMap(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &configMapOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getConfigMap", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"ConfigMap"},
	}, func(ctx context.Context, in *namespacedGet) (*configMapOutput, error) {
		item, err := h.GetConfigMap(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &configMapOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceConfigMap", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"ConfigMap"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.ConfigMap
	}) (*configMapOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceConfigMap(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &configMapOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteConfigMap", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"ConfigMap"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteConfigMap(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== Secret (namespaced) =====

type createSecretInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Secret
}
type secretOutput struct{ Body v1.Secret }
type listSecretOutput struct{ Body []v1.Secret }

func registerSecret(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/secrets"

	huma.Register(api, huma.Operation{
		OperationID: "listSecrets", Method: http.MethodGet, Path: base,
		Tags: []string{"Secret"},
	}, func(ctx context.Context, in *namespacedList) (*listSecretOutput, error) {
		items, err := h.ListSecrets(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listSecretOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createSecret", Method: http.MethodPost, Path: base,
		Tags: []string{"Secret"},
	}, func(ctx context.Context, in *createSecretInput) (*secretOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateSecret(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &secretOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getSecret", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Secret"},
	}, func(ctx context.Context, in *namespacedGet) (*secretOutput, error) {
		item, err := h.GetSecret(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &secretOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceSecret", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Secret"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Secret
	}) (*secretOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceSecret(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &secretOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteSecret", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Secret"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteSecret(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== Grant (namespaced) =====

type createGrantInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Grant
}
type grantOutput struct{ Body v1.Grant }
type listGrantOutput struct{ Body []v1.Grant }

func registerGrant(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/grants"

	huma.Register(api, huma.Operation{
		OperationID: "listGrants", Method: http.MethodGet, Path: base,
		Tags: []string{"Grant"},
	}, func(ctx context.Context, in *namespacedList) (*listGrantOutput, error) {
		items, err := h.ListGrants(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listGrantOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createGrant", Method: http.MethodPost, Path: base,
		Tags: []string{"Grant"},
	}, func(ctx context.Context, in *createGrantInput) (*grantOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateGrant(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &grantOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getGrant", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Grant"},
	}, func(ctx context.Context, in *namespacedGet) (*grantOutput, error) {
		item, err := h.GetGrant(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &grantOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceGrant", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Grant"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Grant
	}) (*grantOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceGrant(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &grantOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteGrant", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Grant"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteGrant(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== Identity (namespaced) — ADR-0135, FEAT-0008/F100 =====

type createIdentityInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Identity
}
type identityOutput struct{ Body v1.Identity }
type listIdentityOutput struct{ Body []v1.Identity }

func registerIdentity(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/identities"

	huma.Register(api, huma.Operation{
		OperationID: "listIdentities", Method: http.MethodGet, Path: base,
		Tags: []string{"Identity"},
	}, func(ctx context.Context, in *namespacedList) (*listIdentityOutput, error) {
		items, err := h.ListIdentities(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listIdentityOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createIdentity", Method: http.MethodPost, Path: base,
		Tags: []string{"Identity"},
	}, func(ctx context.Context, in *createIdentityInput) (*identityOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateIdentity(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &identityOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getIdentity", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Identity"},
	}, func(ctx context.Context, in *namespacedGet) (*identityOutput, error) {
		item, err := h.GetIdentity(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &identityOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceIdentity", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Identity"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Identity
	}) (*identityOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceIdentity(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &identityOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteIdentity", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Identity"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteIdentity(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== Role + RolesAssignment (namespaced) — ADR-0136, FEAT-0008/F101 =====

type createRoleInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Role
}
type roleOutput struct{ Body v1.Role }
type listRoleOutput struct{ Body []v1.Role }
type createRolesAssignmentInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.RolesAssignment
}
type rolesAssignmentOutput struct{ Body v1.RolesAssignment }
type listRolesAssignmentOutput struct{ Body []v1.RolesAssignment }

func registerRole(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/roles"
	huma.Register(api, huma.Operation{OperationID: "listRoles", Method: http.MethodGet, Path: base, Tags: []string{"Role"}},
		func(ctx context.Context, in *namespacedList) (*listRoleOutput, error) {
			items, err := h.ListRoles(ctx, in.Namespace)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &listRoleOutput{Body: items}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "createRole", Method: http.MethodPost, Path: base, Tags: []string{"Role"}},
		func(ctx context.Context, in *createRoleInput) (*roleOutput, error) {
			ctx = withDryRun(ctx, in.DryRun)
			if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
				return nil, wrapFaultError(err)
			}
			item, err := h.CreateRole(ctx, in.Body)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &roleOutput{Body: item}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "getRole", Method: http.MethodGet, Path: base + "/{name}", Tags: []string{"Role"}},
		func(ctx context.Context, in *namespacedGet) (*roleOutput, error) {
			item, err := h.GetRole(ctx, in.Namespace, in.Name)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &roleOutput{Body: item}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "replaceRole", Method: http.MethodPut, Path: base + "/{name}", Tags: []string{"Role"}},
		func(ctx context.Context, in *struct {
			IfMatchParams
			DryRunParams
			Namespace v1.NamespaceName `path:"namespace"`
			Name      v1.ObjectName    `path:"name"`
			Body      v1.Role
		}) (*roleOutput, error) {
			ctx = withDryRun(ctx, in.DryRun)
			if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
				return nil, wrapFaultError(err)
			}
			item, err := h.ReplaceRole(ctx, in.Namespace, in.Name, in.Body)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &roleOutput{Body: item}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "deleteRole", Method: http.MethodDelete, Path: base + "/{name}", Tags: []string{"Role"}},
		func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
			return nil, wrapFaultError(h.DeleteRole(ctx, in.Namespace, in.Name, in.rv))
		})
}

func registerRolesAssignment(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/rolesassignments"
	huma.Register(api, huma.Operation{OperationID: "listRolesAssignments", Method: http.MethodGet, Path: base, Tags: []string{"RolesAssignment"}},
		func(ctx context.Context, in *namespacedList) (*listRolesAssignmentOutput, error) {
			items, err := h.ListRolesAssignments(ctx, in.Namespace)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &listRolesAssignmentOutput{Body: items}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "createRolesAssignment", Method: http.MethodPost, Path: base, Tags: []string{"RolesAssignment"}},
		func(ctx context.Context, in *createRolesAssignmentInput) (*rolesAssignmentOutput, error) {
			ctx = withDryRun(ctx, in.DryRun)
			if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
				return nil, wrapFaultError(err)
			}
			item, err := h.CreateRolesAssignment(ctx, in.Body)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &rolesAssignmentOutput{Body: item}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "getRolesAssignment", Method: http.MethodGet, Path: base + "/{name}", Tags: []string{"RolesAssignment"}},
		func(ctx context.Context, in *namespacedGet) (*rolesAssignmentOutput, error) {
			item, err := h.GetRolesAssignment(ctx, in.Namespace, in.Name)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &rolesAssignmentOutput{Body: item}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "replaceRolesAssignment", Method: http.MethodPut, Path: base + "/{name}", Tags: []string{"RolesAssignment"}},
		func(ctx context.Context, in *struct {
			IfMatchParams
			DryRunParams
			Namespace v1.NamespaceName `path:"namespace"`
			Name      v1.ObjectName    `path:"name"`
			Body      v1.RolesAssignment
		}) (*rolesAssignmentOutput, error) {
			ctx = withDryRun(ctx, in.DryRun)
			if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
				return nil, wrapFaultError(err)
			}
			item, err := h.ReplaceRolesAssignment(ctx, in.Namespace, in.Name, in.Body)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &rolesAssignmentOutput{Body: item}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "deleteRolesAssignment", Method: http.MethodDelete, Path: base + "/{name}", Tags: []string{"RolesAssignment"}},
		func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
			return nil, wrapFaultError(h.DeleteRolesAssignment(ctx, in.Namespace, in.Name, in.rv))
		})
}

// ===== KVStore (namespaced) — ADR-0072 =====

type createKVStoreInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.KVStore
}
type kvStoreOutput struct{ Body v1.KVStore }
type listKVStoreOutput struct{ Body []v1.KVStore }

func registerKVStore(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/kvstores"

	huma.Register(api, huma.Operation{
		OperationID: "listKVStores", Method: http.MethodGet, Path: base,
		Tags: []string{"KVStore"},
	}, func(ctx context.Context, in *namespacedList) (*listKVStoreOutput, error) {
		items, err := h.ListKVStores(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listKVStoreOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createKVStore", Method: http.MethodPost, Path: base,
		Tags: []string{"KVStore"},
	}, func(ctx context.Context, in *createKVStoreInput) (*kvStoreOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateKVStore(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &kvStoreOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getKVStore", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"KVStore"},
	}, func(ctx context.Context, in *namespacedGet) (*kvStoreOutput, error) {
		item, err := h.GetKVStore(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &kvStoreOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceKVStore", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"KVStore"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.KVStore
	}) (*kvStoreOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceKVStore(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &kvStoreOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteKVStore", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"KVStore"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteKVStore(ctx, in.Namespace, in.Name, in.rv))
	})

	huma.Register(api, huma.Operation{
		OperationID: "handoverKVStore", Method: http.MethodPost, Path: base + "/{name}/handover",
		Tags: []string{"KVStore"},
	}, func(ctx context.Context, in *struct {
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      KVStoreHandover
	}) (*kvStoreOutput, error) {
		item, err := h.HandoverKVStore(ctx, in.Namespace, in.Name, in.Body.Workflow)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &kvStoreOutput{Body: item}, nil
	})
}

// KVStoreHandover is the handover request body: the Workflow that becomes the store's owner.
type KVStoreHandover struct {
	Workflow v1.ObjectName `json:"workflow"`
}

// ===== Bucket (namespaced) — ADR-0080 =====

type createBucketInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Bucket
}
type bucketOutput struct{ Body v1.Bucket }
type listBucketOutput struct{ Body []v1.Bucket }

func registerBucket(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/buckets"

	huma.Register(api, huma.Operation{
		OperationID: "listBuckets", Method: http.MethodGet, Path: base,
		Tags: []string{"Bucket"},
	}, func(ctx context.Context, in *namespacedList) (*listBucketOutput, error) {
		items, err := h.ListBuckets(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listBucketOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createBucket", Method: http.MethodPost, Path: base,
		Tags: []string{"Bucket"},
	}, func(ctx context.Context, in *createBucketInput) (*bucketOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateBucket(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &bucketOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getBucket", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Bucket"},
	}, func(ctx context.Context, in *namespacedGet) (*bucketOutput, error) {
		item, err := h.GetBucket(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &bucketOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceBucket", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Bucket"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Bucket
	}) (*bucketOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceBucket(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &bucketOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteBucket", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Bucket"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteBucket(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== CatalogService (namespaced) — ADR-0086 =====

type createCatalogServiceInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.CatalogService
}
type catalogServiceOutput struct{ Body v1.CatalogService }
type listCatalogServiceOutput struct{ Body []v1.CatalogService }

func registerCatalogService(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/catalogservices"

	huma.Register(api, huma.Operation{
		OperationID: "listCatalogServices", Method: http.MethodGet, Path: base,
		Tags: []string{"CatalogService"},
	}, func(ctx context.Context, in *namespacedList) (*listCatalogServiceOutput, error) {
		items, err := h.ListCatalogServices(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listCatalogServiceOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createCatalogService", Method: http.MethodPost, Path: base,
		Tags: []string{"CatalogService"},
	}, func(ctx context.Context, in *createCatalogServiceInput) (*catalogServiceOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateCatalogService(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &catalogServiceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getCatalogService", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"CatalogService"},
	}, func(ctx context.Context, in *namespacedGet) (*catalogServiceOutput, error) {
		item, err := h.GetCatalogService(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &catalogServiceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceCatalogService", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"CatalogService"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.CatalogService
	}) (*catalogServiceOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceCatalogService(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &catalogServiceOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteCatalogService", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"CatalogService"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteCatalogService(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== Policy (namespaced) — ADR-0074 =====

type createPolicyInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Policy
}
type policyOutput struct{ Body v1.Policy }
type listPolicyOutput struct{ Body []v1.Policy }

func registerPolicy(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/policies"

	huma.Register(api, huma.Operation{
		OperationID: "listPolicies", Method: http.MethodGet, Path: base,
		Tags: []string{"Policy"},
	}, func(ctx context.Context, in *namespacedList) (*listPolicyOutput, error) {
		items, err := h.ListPolicies(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listPolicyOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createPolicy", Method: http.MethodPost, Path: base,
		Tags: []string{"Policy"},
	}, func(ctx context.Context, in *createPolicyInput) (*policyOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreatePolicy(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &policyOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getPolicy", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Policy"},
	}, func(ctx context.Context, in *namespacedGet) (*policyOutput, error) {
		item, err := h.GetPolicy(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &policyOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replacePolicy", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Policy"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Policy
	}) (*policyOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplacePolicy(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &policyOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deletePolicy", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Policy"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeletePolicy(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== EgressPolicy (namespaced) =====

type createEgressPolicyInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.EgressPolicy
}
type egressPolicyOutput struct{ Body v1.EgressPolicy }
type listEgressPolicyOutput struct{ Body []v1.EgressPolicy }

func registerEgressPolicy(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/egresspolicies"

	huma.Register(api, huma.Operation{
		OperationID: "listEgressPolicies", Method: http.MethodGet, Path: base,
		Tags: []string{"EgressPolicy"},
	}, func(ctx context.Context, in *namespacedList) (*listEgressPolicyOutput, error) {
		items, err := h.ListEgressPolicies(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listEgressPolicyOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createEgressPolicy", Method: http.MethodPost, Path: base,
		Tags: []string{"EgressPolicy"},
	}, func(ctx context.Context, in *createEgressPolicyInput) (*egressPolicyOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateEgressPolicy(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &egressPolicyOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getEgressPolicy", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"EgressPolicy"},
	}, func(ctx context.Context, in *namespacedGet) (*egressPolicyOutput, error) {
		item, err := h.GetEgressPolicy(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &egressPolicyOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceEgressPolicy", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"EgressPolicy"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.EgressPolicy
	}) (*egressPolicyOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceEgressPolicy(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &egressPolicyOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteEgressPolicy", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"EgressPolicy"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteEgressPolicy(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== Invocation (namespaced) =====

type createInvocationInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Invocation
}
type invocationOutput struct{ Body v1.Invocation }
type listInvocationOutput struct{ Body []v1.Invocation }

func registerInvocation(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/invocations"

	huma.Register(api, huma.Operation{
		OperationID: "listInvocations", Method: http.MethodGet, Path: base,
		Tags: []string{"Invocation"},
	}, func(ctx context.Context, in *namespacedList) (*listInvocationOutput, error) {
		items, err := h.ListInvocations(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listInvocationOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createInvocation", Method: http.MethodPost, Path: base,
		Tags: []string{"Invocation"},
	}, func(ctx context.Context, in *createInvocationInput) (*invocationOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateInvocation(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &invocationOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getInvocation", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Invocation"},
	}, func(ctx context.Context, in *namespacedGet) (*invocationOutput, error) {
		item, err := h.GetInvocation(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &invocationOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceInvocation", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Invocation"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Invocation
	}) (*invocationOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceInvocation(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &invocationOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteInvocation", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Invocation"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteInvocation(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== RuntimeClass (cluster-scoped) =====

type createRuntimeClassInput struct {
	DryRunParams
	Body v1.RuntimeClass
}
type runtimeClassOutput struct{ Body v1.RuntimeClass }
type listRuntimeClassOutput struct{ Body []v1.RuntimeClass }

func registerRuntimeClass(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/runtimeclasses"

	huma.Register(api, huma.Operation{
		OperationID: "listRuntimeClasses", Method: http.MethodGet, Path: base,
		Tags: []string{"RuntimeClass"},
	}, func(ctx context.Context, _ *struct{}) (*listRuntimeClassOutput, error) {
		items, err := h.ListRuntimeClasses(ctx)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listRuntimeClassOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createRuntimeClass", Method: http.MethodPost, Path: base,
		Tags: []string{"RuntimeClass"},
	}, func(ctx context.Context, in *createRuntimeClassInput) (*runtimeClassOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		item, err := h.CreateRuntimeClass(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &runtimeClassOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getRuntimeClass", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"RuntimeClass"},
	}, func(ctx context.Context, in *clusterScopedGet) (*runtimeClassOutput, error) {
		item, err := h.GetRuntimeClass(ctx, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &runtimeClassOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceRuntimeClass", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"RuntimeClass"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Name v1.ObjectName `path:"name"`
		Body v1.RuntimeClass
	}) (*runtimeClassOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceRuntimeClass(ctx, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &runtimeClassOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteRuntimeClass", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"RuntimeClass"},
	}, func(ctx context.Context, in *clusterScopedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteRuntimeClass(ctx, in.Name, in.rv))
	})
}

// ===== WorkerNode (cluster-scoped) =====

type createWorkerNodeInput struct {
	DryRunParams
	Body v1.WorkerNode
}
type workerNodeOutput struct{ Body v1.WorkerNode }
type listWorkerOutput struct{ Body []v1.WorkerNode }

func registerWorker(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/workernodes"

	huma.Register(api, huma.Operation{
		OperationID: "listWorkerNodes", Method: http.MethodGet, Path: base,
		Tags: []string{"WorkerNode"},
	}, func(ctx context.Context, _ *struct{}) (*listWorkerOutput, error) {
		items, err := h.ListWorkerNodes(ctx)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listWorkerOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createWorkerNode", Method: http.MethodPost, Path: base,
		Tags: []string{"WorkerNode"},
	}, func(ctx context.Context, in *createWorkerNodeInput) (*workerNodeOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		item, err := h.CreateWorkerNode(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workerNodeOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getWorkerNode", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"WorkerNode"},
	}, func(ctx context.Context, in *clusterScopedGet) (*workerNodeOutput, error) {
		item, err := h.GetWorkerNode(ctx, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workerNodeOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceWorkerNode", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"WorkerNode"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Name v1.ObjectName `path:"name"`
		Body v1.WorkerNode
	}) (*workerNodeOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceWorkerNode(ctx, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workerNodeOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteWorkerNode", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"WorkerNode"},
	}, func(ctx context.Context, in *clusterScopedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteWorkerNode(ctx, in.Name, in.rv))
	})
}

// ===== Gateway (cluster-scoped) =====

type createGatewayInput struct {
	DryRunParams
	Body v1.Gateway
}
type gatewayOutput struct{ Body v1.Gateway }
type listGatewayOutput struct{ Body []v1.Gateway }

func registerGateway(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/gateways"

	huma.Register(api, huma.Operation{
		OperationID: "listGateways", Method: http.MethodGet, Path: base,
		Tags: []string{"Gateway"},
	}, func(ctx context.Context, _ *struct{}) (*listGatewayOutput, error) {
		items, err := h.ListGateways(ctx)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listGatewayOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createGateway", Method: http.MethodPost, Path: base,
		Tags: []string{"Gateway"},
	}, func(ctx context.Context, in *createGatewayInput) (*gatewayOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		item, err := h.CreateGateway(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &gatewayOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getGateway", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Gateway"},
	}, func(ctx context.Context, in *clusterScopedGet) (*gatewayOutput, error) {
		item, err := h.GetGateway(ctx, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &gatewayOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceGateway", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Gateway"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Name v1.ObjectName `path:"name"`
		Body v1.Gateway
	}) (*gatewayOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceGateway(ctx, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &gatewayOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteGateway", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Gateway"},
	}, func(ctx context.Context, in *clusterScopedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteGateway(ctx, in.Name, in.rv))
	})
}

// ===== Workflow (namespaced) — ADR-0094 =====

type createWorkflowInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Workflow
}
type workflowOutput struct{ Body v1.Workflow }
type listWorkflowOutput struct{ Body []v1.Workflow }

func registerWorkflow(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/workflows"

	huma.Register(api, huma.Operation{
		OperationID: "listWorkflows", Method: http.MethodGet, Path: base,
		Tags: []string{"Workflow"},
	}, func(ctx context.Context, in *namespacedList) (*listWorkflowOutput, error) {
		items, err := h.ListWorkflows(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listWorkflowOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createWorkflow", Method: http.MethodPost, Path: base,
		Tags: []string{"Workflow"},
	}, func(ctx context.Context, in *createWorkflowInput) (*workflowOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateWorkflow(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workflowOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getWorkflow", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Workflow"},
	}, func(ctx context.Context, in *namespacedGet) (*workflowOutput, error) {
		item, err := h.GetWorkflow(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workflowOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceWorkflow", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Workflow"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Workflow
	}) (*workflowOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceWorkflow(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workflowOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteWorkflow", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Workflow"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteWorkflow(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== WorkflowRun (namespaced) — ADR-0094 =====

type createWorkflowRunInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.WorkflowRun
}
type workflowRunOutput struct{ Body v1.WorkflowRun }
type listWorkflowRunOutput struct{ Body []v1.WorkflowRun }

func registerWorkflowRun(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/workflowruns"

	huma.Register(api, huma.Operation{
		OperationID: "listWorkflowRuns", Method: http.MethodGet, Path: base,
		Tags: []string{"WorkflowRun"},
	}, func(ctx context.Context, in *namespacedList) (*listWorkflowRunOutput, error) {
		items, err := h.ListWorkflowRuns(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listWorkflowRunOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createWorkflowRun", Method: http.MethodPost, Path: base,
		Tags: []string{"WorkflowRun"},
	}, func(ctx context.Context, in *createWorkflowRunInput) (*workflowRunOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateWorkflowRun(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workflowRunOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getWorkflowRun", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"WorkflowRun"},
	}, func(ctx context.Context, in *namespacedGet) (*workflowRunOutput, error) {
		item, err := h.GetWorkflowRun(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workflowRunOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceWorkflowRun", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"WorkflowRun"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.WorkflowRun
	}) (*workflowRunOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceWorkflowRun(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &workflowRunOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteWorkflowRun", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"WorkflowRun"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteWorkflowRun(ctx, in.Namespace, in.Name, in.rv))
	})
}

type createSensorInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Sensor
}
type sensorOutput struct{ Body v1.Sensor }
type listSensorOutput struct{ Body []v1.Sensor }

func registerSensor(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/sensors"

	huma.Register(api, huma.Operation{
		OperationID: "listSensors", Method: http.MethodGet, Path: base, Tags: []string{"Sensor"},
	}, func(ctx context.Context, in *namespacedList) (*listSensorOutput, error) {
		items, err := h.ListSensors(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listSensorOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createSensor", Method: http.MethodPost, Path: base, Tags: []string{"Sensor"},
	}, func(ctx context.Context, in *createSensorInput) (*sensorOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateSensor(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &sensorOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getSensor", Method: http.MethodGet, Path: base + "/{name}", Tags: []string{"Sensor"},
	}, func(ctx context.Context, in *namespacedGet) (*sensorOutput, error) {
		item, err := h.GetSensor(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &sensorOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceSensor", Method: http.MethodPut, Path: base + "/{name}", Tags: []string{"Sensor"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Sensor
	}) (*sensorOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceSensor(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &sensorOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteSensor", Method: http.MethodDelete, Path: base + "/{name}", Tags: []string{"Sensor"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteSensor(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== Site (namespaced) — ADR-0139, FEAT-0003/F103 =====

type createSiteInput struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      v1.Site
}
type siteOutput struct{ Body v1.Site }
type listSiteOutput struct{ Body []v1.Site }

func registerSite(api huma.API, h Handlers) {
	base := "/apis/funcd.io/v1alpha1/namespaces/{namespace}/sites"

	huma.Register(api, huma.Operation{
		OperationID: "listSites", Method: http.MethodGet, Path: base,
		Tags: []string{"Site"},
	}, func(ctx context.Context, in *namespacedList) (*listSiteOutput, error) {
		items, err := h.ListSites(ctx, in.Namespace)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &listSiteOutput{Body: items}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "createSite", Method: http.MethodPost, Path: base,
		Tags: []string{"Site"},
	}, func(ctx context.Context, in *createSiteInput) (*siteOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := matchPathNamespace(in.Namespace, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.CreateSite(ctx, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &siteOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "getSite", Method: http.MethodGet, Path: base + "/{name}",
		Tags: []string{"Site"},
	}, func(ctx context.Context, in *namespacedGet) (*siteOutput, error) {
		item, err := h.GetSite(ctx, in.Namespace, in.Name)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &siteOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "replaceSite", Method: http.MethodPut, Path: base + "/{name}",
		Tags: []string{"Site"},
	}, func(ctx context.Context, in *struct {
		IfMatchParams
		DryRunParams
		Namespace v1.NamespaceName `path:"namespace"`
		Name      v1.ObjectName    `path:"name"`
		Body      v1.Site
	}) (*siteOutput, error) {
		ctx = withDryRun(ctx, in.DryRun)
		if err := withReplaceVersion(in.IfMatchParams, &in.Body.ObjectMeta); err != nil {
			return nil, wrapFaultError(err)
		}
		item, err := h.ReplaceSite(ctx, in.Namespace, in.Name, in.Body)
		if err != nil {
			return nil, wrapFaultError(err)
		}
		return &siteOutput{Body: item}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "deleteSite", Method: http.MethodDelete, Path: base + "/{name}",
		Tags: []string{"Site"},
	}, func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
		return nil, wrapFaultError(h.DeleteSite(ctx, in.Namespace, in.Name, in.rv))
	})
}

// ===== App (namespaced) — ADR-0199, FEAT-0010/F113 =====

func registerApp(api huma.API, h Handlers) {
	registerNamespacedCRUD(api, crudRoutes[v1.App]{
		kind: "App", kinds: "Apps", path: "apps",
		list: h.ListApps, create: h.CreateApp, get: h.GetApp, replace: h.ReplaceApp, remove: h.DeleteApp,
	})
}

// ===== AppRevision (namespaced, read-only) — ADR-0200, FEAT-0010/F114 =====

func registerAppRevision(api huma.API, h Handlers) {
	registerNamespacedRead(api, crudRoutes[v1.AppRevision]{
		kind: "AppRevision", kinds: "AppRevisions", path: "apprevisions",
		list: h.ListAppRevisions, get: h.GetAppRevision,
	})
}

// crudRoutes is the REST surface of one namespaced kind under
// /apis/funcd.io/v1alpha1/namespaces/{namespace}/<path>, registered by registerNamespacedCRUD, or by
// registerNamespacedRead for a read-only kind, which leaves create, replace and remove nil.
type crudRoutes[T crudKind] struct {
	kind, kinds, path string
	list              func(context.Context, v1.NamespaceName) ([]T, error)
	create            func(context.Context, T) (T, error)
	get               func(context.Context, v1.NamespaceName, v1.ObjectName) (T, error)
	replace           func(context.Context, v1.NamespaceName, v1.ObjectName, T) (T, error)
	remove            func(context.Context, v1.NamespaceName, v1.ObjectName, string) error
}

type namespacedBodyInput[T crudKind] struct {
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Body      T
}

type namespacedNameBodyInput[T crudKind] struct {
	IfMatchParams
	DryRunParams
	Namespace v1.NamespaceName `path:"namespace"`
	Name      v1.ObjectName    `path:"name"`
	Body      T
}

type bodyOutput[T crudKind] struct{ Body T }

type listOutput[T crudKind] struct{ Body []T }

func respond[T crudKind](v T, err error) (*bodyOutput[T], error) {
	if err != nil {
		return nil, wrapFaultError(err)
	}
	return &bodyOutput[T]{Body: v}, nil
}

func registerNamespacedCRUD[T crudKind, PT objectPtr[T]](api huma.API, r crudRoutes[T]) {
	base, item, tags := registerNamespacedRead(api, r)
	huma.Register(api, huma.Operation{OperationID: "create" + r.kind, Method: http.MethodPost, Path: base, Tags: tags},
		func(ctx context.Context, in *namespacedBodyInput[T]) (*bodyOutput[T], error) {
			if err := matchPathNamespace(in.Namespace, PT(&in.Body).GetObjectMeta()); err != nil {
				return nil, wrapFaultError(err)
			}
			return respond(r.create(withDryRun(ctx, in.DryRun), in.Body))
		})
	huma.Register(api, huma.Operation{OperationID: "replace" + r.kind, Method: http.MethodPut, Path: item, Tags: tags},
		func(ctx context.Context, in *namespacedNameBodyInput[T]) (*bodyOutput[T], error) {
			if err := withReplaceVersion(in.IfMatchParams, PT(&in.Body).GetObjectMeta()); err != nil {
				return nil, wrapFaultError(err)
			}
			return respond(r.replace(withDryRun(ctx, in.DryRun), in.Namespace, in.Name, in.Body))
		})
	huma.Register(api, huma.Operation{OperationID: "delete" + r.kind, Method: http.MethodDelete, Path: item, Tags: tags},
		func(ctx context.Context, in *namespacedDelete) (*struct{}, error) {
			return nil, wrapFaultError(r.remove(ctx, in.Namespace, in.Name, in.rv))
		})
}

// registerNamespacedRead registers r's list and get only, and returns the collection path, the item path and the
// tags. A read-only kind (ADR-0172 Decision 2, ADR-0200 Decision 2) stops here: its POST, PUT and DELETE answer 405
// with Allow: GET.
func registerNamespacedRead[T crudKind](api huma.API, r crudRoutes[T]) (base, item string, tags []string) {
	base = "/apis/funcd.io/v1alpha1/namespaces/{namespace}/" + r.path
	item = base + "/{name}"
	tags = []string{r.kind}
	huma.Register(api, huma.Operation{OperationID: "list" + r.kinds, Method: http.MethodGet, Path: base, Tags: tags},
		func(ctx context.Context, in *namespacedList) (*listOutput[T], error) {
			items, err := r.list(ctx, in.Namespace)
			if err != nil {
				return nil, wrapFaultError(err)
			}
			return &listOutput[T]{Body: items}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get" + r.kind, Method: http.MethodGet, Path: item, Tags: tags},
		func(ctx context.Context, in *namespacedGet) (*bodyOutput[T], error) {
			return respond(r.get(ctx, in.Namespace, in.Name))
		})
	return base, item, tags
}
