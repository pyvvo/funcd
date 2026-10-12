package controlplane

import (
	"context"
	"sync"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// StubHandlers is an in-memory Handlers implementation for spec-gen and tests.
// P-L/F07 replaces it with real store/controller-backed handlers.
type StubHandlers struct {
	mu sync.RWMutex

	namespaces       map[string]v1.Namespace
	resourceGroups   map[string]v1.ResourceGroup
	functions        map[string]v1.Function
	revisions        map[string]v1.Revision
	routes           map[string]v1.Route
	services         map[string]v1.Service
	eventSources     map[string]v1.EventSource
	configMaps       map[string]v1.ConfigMap
	secrets          map[string]v1.Secret
	grants           map[string]v1.Grant
	egressPolicies   map[string]v1.EgressPolicy
	invocations      map[string]v1.Invocation
	runtimeClasses   map[string]v1.RuntimeClass
	workerNodes      map[string]v1.WorkerNode
	gateways         map[string]v1.Gateway
	kvstores         map[string]v1.KVStore
	buckets          map[string]v1.Bucket
	catalogServices  map[string]v1.CatalogService
	sqlDatabases     map[string]v1.SQLDatabase
	identities       map[string]v1.Identity
	roles            map[string]v1.Role
	rolesAssignments map[string]v1.RolesAssignment
	sites            map[string]v1.Site
	apps             map[string]v1.App
	appRevisions     map[string]v1.AppRevision
	backupSchedules  map[string]v1.BackupSchedule
	backups          map[string]v1.Backup
	restores         map[string]v1.Restore
	policies         map[string]v1.Policy
	workflows        map[string]v1.Workflow
	workflowRuns     map[string]v1.WorkflowRun
	sensors          map[string]v1.Sensor
}

// NewStubHandlers returns an initialized StubHandlers.
func NewStubHandlers() *StubHandlers {
	return &StubHandlers{
		namespaces:       make(map[string]v1.Namespace),
		resourceGroups:   make(map[string]v1.ResourceGroup),
		functions:        make(map[string]v1.Function),
		revisions:        make(map[string]v1.Revision),
		routes:           make(map[string]v1.Route),
		services:         make(map[string]v1.Service),
		eventSources:     make(map[string]v1.EventSource),
		configMaps:       make(map[string]v1.ConfigMap),
		secrets:          make(map[string]v1.Secret),
		grants:           make(map[string]v1.Grant),
		egressPolicies:   make(map[string]v1.EgressPolicy),
		invocations:      make(map[string]v1.Invocation),
		runtimeClasses:   make(map[string]v1.RuntimeClass),
		workerNodes:      make(map[string]v1.WorkerNode),
		gateways:         make(map[string]v1.Gateway),
		kvstores:         make(map[string]v1.KVStore),
		buckets:          make(map[string]v1.Bucket),
		catalogServices:  make(map[string]v1.CatalogService),
		sqlDatabases:     make(map[string]v1.SQLDatabase),
		identities:       make(map[string]v1.Identity),
		roles:            make(map[string]v1.Role),
		rolesAssignments: make(map[string]v1.RolesAssignment),
		sites:            make(map[string]v1.Site),
		apps:             make(map[string]v1.App),
		appRevisions:     make(map[string]v1.AppRevision),
		backupSchedules:  make(map[string]v1.BackupSchedule),
		backups:          make(map[string]v1.Backup),
		restores:         make(map[string]v1.Restore),
		policies:         make(map[string]v1.Policy),
		workflows:        make(map[string]v1.Workflow),
		workflowRuns:     make(map[string]v1.WorkflowRun),
		sensors:          make(map[string]v1.Sensor),
	}
}

func nsKey(ns v1.NamespaceName, name v1.ObjectName) string {
	return string(ns) + "/" + string(name)
}

// ---- Namespace (cluster-scoped) ----

func (s *StubHandlers) GetNamespace(_ context.Context, name v1.ObjectName) (v1.Namespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ns, ok := s.namespaces[string(name)]
	if !ok {
		return v1.Namespace{}, fault.NotFoundf("StubHandlers.GetNamespace", "Namespace %q not found", name)
	}
	return ns, nil
}

func (s *StubHandlers) CreateNamespace(_ context.Context, ns v1.Namespace) (v1.Namespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(ns.Name)
	if _, exists := s.namespaces[key]; exists {
		return v1.Namespace{}, fault.Conflictf("StubHandlers.CreateNamespace", "Namespace %q already exists", key)
	}
	s.namespaces[key] = ns
	return ns, nil
}

func (s *StubHandlers) ListNamespaces(_ context.Context) ([]v1.Namespace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Namespace, 0, len(s.namespaces))
	for _, ns := range s.namespaces {
		out = append(out, ns)
	}
	return out, nil
}

func (s *StubHandlers) ReplaceNamespace(_ context.Context, name v1.ObjectName, ns v1.Namespace) (v1.Namespace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.namespaces[key]; !exists {
		return v1.Namespace{}, fault.NotFoundf("StubHandlers.ReplaceNamespace", "Namespace %q not found", name)
	}
	s.namespaces[key] = ns
	return ns, nil
}

func (s *StubHandlers) DeleteNamespace(_ context.Context, name v1.ObjectName, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.namespaces[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteNamespace", "Namespace %q not found", name)
	}
	delete(s.namespaces, key)
	return nil
}

// ---- ResourceGroup (namespaced) ----

func (s *StubHandlers) GetResourceGroup(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.ResourceGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rg, ok := s.resourceGroups[nsKey(ns, name)]; ok {
		return rg, nil
	}
	return v1.ResourceGroup{}, fault.NotFoundf("StubHandlers.GetResourceGroup", "ResourceGroup %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateResourceGroup(_ context.Context, rg v1.ResourceGroup) (v1.ResourceGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(rg.Namespace, rg.Name)
	if _, exists := s.resourceGroups[key]; exists {
		return v1.ResourceGroup{}, fault.Conflictf("StubHandlers.CreateResourceGroup", "ResourceGroup %s already exists", key)
	}
	s.resourceGroups[key] = rg
	return rg, nil
}

func (s *StubHandlers) ListResourceGroups(_ context.Context, ns v1.NamespaceName) ([]v1.ResourceGroup, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.ResourceGroup, 0)
	for _, rg := range s.resourceGroups {
		if rg.Namespace == ns {
			out = append(out, rg)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceResourceGroup(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, rg v1.ResourceGroup) (v1.ResourceGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.resourceGroups[key]; !exists {
		return v1.ResourceGroup{}, fault.NotFoundf("StubHandlers.ReplaceResourceGroup", "ResourceGroup %s not found", key)
	}
	s.resourceGroups[key] = rg
	return rg, nil
}

func (s *StubHandlers) DeleteResourceGroup(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ bool, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.resourceGroups[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteResourceGroup", "ResourceGroup %s not found", key)
	}
	delete(s.resourceGroups, key)
	return nil
}

// ---- Function (namespaced) ----

func (s *StubHandlers) GetFunction(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Function, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if fn, ok := s.functions[nsKey(ns, name)]; ok {
		return fn, nil
	}
	return v1.Function{}, fault.NotFoundf("StubHandlers.GetFunction", "Function %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateFunction(_ context.Context, fn v1.Function) (v1.Function, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(fn.Namespace, fn.Name)
	if _, exists := s.functions[key]; exists {
		return v1.Function{}, fault.Conflictf("StubHandlers.CreateFunction", "Function %s already exists", key)
	}
	s.functions[key] = fn
	return fn, nil
}

func (s *StubHandlers) ListFunctions(_ context.Context, ns v1.NamespaceName) ([]v1.Function, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Function, 0)
	for _, fn := range s.functions {
		if fn.Namespace == ns {
			out = append(out, fn)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceFunction(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, fn v1.Function) (v1.Function, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.functions[key]; !exists {
		return v1.Function{}, fault.NotFoundf("StubHandlers.ReplaceFunction", "Function %s not found", key)
	}
	s.functions[key] = fn
	return fn, nil
}

func (s *StubHandlers) DeleteFunction(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.functions[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteFunction", "Function %s not found", key)
	}
	delete(s.functions, key)
	return nil
}

// ---- Revision, Route, Service, EventSource, ConfigMap, Secret, Grant, EgressPolicy, Invocation (namespaced) ----
// All namespaced kinds follow the same pattern: map key = "namespace/name".

func (s *StubHandlers) GetRevision(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.revisions[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.Revision{}, fault.NotFoundf("StubHandlers.GetRevision", "Revision %s/%s not found", ns, name)
}

func (s *StubHandlers) ListRevisions(_ context.Context, ns v1.NamespaceName) ([]v1.Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Revision, 0)
	for _, v := range s.revisions {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

// ---- Route ----

func (s *StubHandlers) GetRoute(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.routes[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.Route{}, fault.NotFoundf("StubHandlers.GetRoute", "Route %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateRoute(_ context.Context, rt v1.Route) (v1.Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(rt.Namespace, rt.Name)
	if _, exists := s.routes[key]; exists {
		return v1.Route{}, fault.Conflictf("StubHandlers.CreateRoute", "Route %s already exists", key)
	}
	s.routes[key] = rt
	return rt, nil
}

func (s *StubHandlers) ListRoutes(_ context.Context, ns v1.NamespaceName) ([]v1.Route, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Route, 0)
	for _, v := range s.routes {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceRoute(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, rt v1.Route) (v1.Route, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.routes[key]; !exists {
		return v1.Route{}, fault.NotFoundf("StubHandlers.ReplaceRoute", "Route %s not found", key)
	}
	s.routes[key] = rt
	return rt, nil
}

func (s *StubHandlers) DeleteRoute(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.routes[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteRoute", "Route %s not found", key)
	}
	delete(s.routes, key)
	return nil
}

// ---- Service ----

func (s *StubHandlers) GetService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Service, error) {
	return stubGet(s, s.services, "Service", nsKey(ns, name))
}

func (s *StubHandlers) CreateService(_ context.Context, svc v1.Service) (v1.Service, error) {
	return stubPut(s, s.services, "Service", nsKey(svc.Namespace, svc.Name), svc, false)
}

func (s *StubHandlers) ListServices(_ context.Context, ns v1.NamespaceName) ([]v1.Service, error) {
	return stubList(s, s.services, ns)
}

func (s *StubHandlers) ReplaceService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, svc v1.Service) (v1.Service, error) {
	return stubPut(s, s.services, "Service", nsKey(ns, name), svc, true)
}

func (s *StubHandlers) DeleteService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.services, "Service", nsKey(ns, name))
}

// ---- EventSource ----

func (s *StubHandlers) GetEventSource(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EventSource, error) {
	return stubGet(s, s.eventSources, "EventSource", nsKey(ns, name))
}

func (s *StubHandlers) CreateEventSource(_ context.Context, es v1.EventSource) (v1.EventSource, error) {
	return stubPut(s, s.eventSources, "EventSource", nsKey(es.Namespace, es.Name), es, false)
}

func (s *StubHandlers) ListEventSources(_ context.Context, ns v1.NamespaceName) ([]v1.EventSource, error) {
	return stubList(s, s.eventSources, ns)
}

func (s *StubHandlers) ReplaceEventSource(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, es v1.EventSource) (v1.EventSource, error) {
	return stubPut(s, s.eventSources, "EventSource", nsKey(ns, name), es, true)
}

func (s *StubHandlers) DeleteEventSource(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.eventSources, "EventSource", nsKey(ns, name))
}

// ---- ConfigMap ----

func (s *StubHandlers) GetConfigMap(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.ConfigMap, error) {
	return stubGet(s, s.configMaps, "ConfigMap", nsKey(ns, name))
}

func (s *StubHandlers) CreateConfigMap(_ context.Context, cfg v1.ConfigMap) (v1.ConfigMap, error) {
	return stubPut(s, s.configMaps, "ConfigMap", nsKey(cfg.Namespace, cfg.Name), cfg, false)
}

func (s *StubHandlers) ListConfigMaps(_ context.Context, ns v1.NamespaceName) ([]v1.ConfigMap, error) {
	return stubList(s, s.configMaps, ns)
}

func (s *StubHandlers) ReplaceConfigMap(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, cfg v1.ConfigMap) (v1.ConfigMap, error) {
	return stubPut(s, s.configMaps, "ConfigMap", nsKey(ns, name), cfg, true)
}

func (s *StubHandlers) DeleteConfigMap(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.configMaps, "ConfigMap", nsKey(ns, name))
}

// ---- Secret ----

func (s *StubHandlers) GetSecret(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Secret, error) {
	return stubGet(s, s.secrets, "Secret", nsKey(ns, name))
}

func (s *StubHandlers) CreateSecret(_ context.Context, sec v1.Secret) (v1.Secret, error) {
	return stubPut(s, s.secrets, "Secret", nsKey(sec.Namespace, sec.Name), sec, false)
}

func (s *StubHandlers) ListSecrets(_ context.Context, ns v1.NamespaceName) ([]v1.Secret, error) {
	return stubList(s, s.secrets, ns)
}

func (s *StubHandlers) ReplaceSecret(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, sec v1.Secret) (v1.Secret, error) {
	return stubPut(s, s.secrets, "Secret", nsKey(ns, name), sec, true)
}

func (s *StubHandlers) DeleteSecret(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.secrets, "Secret", nsKey(ns, name))
}

// ---- KVStore (ADR-0072) ----

func (s *StubHandlers) GetKVStore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.KVStore, error) {
	return stubGet(s, s.kvstores, "KVStore", nsKey(ns, name))
}

func (s *StubHandlers) CreateKVStore(_ context.Context, ks v1.KVStore) (v1.KVStore, error) {
	return stubPut(s, s.kvstores, "KVStore", nsKey(ks.Namespace, ks.Name), ks, false)
}

func (s *StubHandlers) ListKVStores(_ context.Context, ns v1.NamespaceName) ([]v1.KVStore, error) {
	return stubList(s, s.kvstores, ns)
}

func (s *StubHandlers) ReplaceKVStore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, ks v1.KVStore) (v1.KVStore, error) {
	return stubPut(s, s.kvstores, "KVStore", nsKey(ns, name), ks, true)
}

func (s *StubHandlers) DeleteKVStore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.kvstores, "KVStore", nsKey(ns, name))
}

func (s *StubHandlers) HandoverKVStore(_ context.Context, ns v1.NamespaceName, name, _ v1.ObjectName) (v1.KVStore, error) {
	return v1.KVStore{}, fault.Unavailablef("StubHandlers.HandoverKVStore", "KVStore %s: no workflow store behind the stub", nsKey(ns, name))
}

// ---- Bucket (ADR-0080) ----

func (s *StubHandlers) GetBucket(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Bucket, error) {
	return stubGet(s, s.buckets, "Bucket", nsKey(ns, name))
}

func (s *StubHandlers) CreateBucket(_ context.Context, b v1.Bucket) (v1.Bucket, error) {
	return stubPut(s, s.buckets, "Bucket", nsKey(b.Namespace, b.Name), b, false)
}

func (s *StubHandlers) ListBuckets(_ context.Context, ns v1.NamespaceName) ([]v1.Bucket, error) {
	return stubList(s, s.buckets, ns)
}

func (s *StubHandlers) ReplaceBucket(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, b v1.Bucket) (v1.Bucket, error) {
	return stubPut(s, s.buckets, "Bucket", nsKey(ns, name), b, true)
}

func (s *StubHandlers) DeleteBucket(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.buckets, "Bucket", nsKey(ns, name))
}

// ---- Site (ADR-0139) ----

func (s *StubHandlers) GetSite(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Site, error) {
	return stubGet(s, s.sites, "Site", nsKey(ns, name))
}

func (s *StubHandlers) CreateSite(_ context.Context, si v1.Site) (v1.Site, error) {
	return stubPut(s, s.sites, "Site", nsKey(si.Namespace, si.Name), si, false)
}

func (s *StubHandlers) ListSites(_ context.Context, ns v1.NamespaceName) ([]v1.Site, error) {
	return stubList(s, s.sites, ns)
}

func (s *StubHandlers) ReplaceSite(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, si v1.Site) (v1.Site, error) {
	return stubPut(s, s.sites, "Site", nsKey(ns, name), si, true)
}

func (s *StubHandlers) DeleteSite(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.sites, "Site", nsKey(ns, name))
}

// ---- App (ADR-0199) ----

func (s *StubHandlers) GetApp(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.App, error) {
	return stubGet(s, s.apps, "App", nsKey(ns, name))
}

func (s *StubHandlers) CreateApp(_ context.Context, app v1.App) (v1.App, error) {
	return stubPut(s, s.apps, "App", nsKey(app.Namespace, app.Name), app, false)
}

func (s *StubHandlers) ListApps(_ context.Context, ns v1.NamespaceName) ([]v1.App, error) {
	return stubList(s, s.apps, ns)
}

func (s *StubHandlers) ReplaceApp(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, app v1.App) (v1.App, error) {
	return stubPut(s, s.apps, "App", nsKey(ns, name), app, true)
}

func (s *StubHandlers) DeleteApp(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.apps, "App", nsKey(ns, name))
}

// ---- AppRevision (ADR-0200, read-only) ----

func (s *StubHandlers) GetAppRevision(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.AppRevision, error) {
	return stubGet(s, s.appRevisions, "AppRevision", nsKey(ns, name))
}

func (s *StubHandlers) ListAppRevisions(_ context.Context, ns v1.NamespaceName) ([]v1.AppRevision, error) {
	return stubList(s, s.appRevisions, ns)
}

// ---- BackupSchedule, Backup, Restore (ADR-0222) ----

func (s *StubHandlers) GetBackupSchedule(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.BackupSchedule, error) {
	return stubGet(s, s.backupSchedules, "BackupSchedule", nsKey(ns, name))
}

func (s *StubHandlers) CreateBackupSchedule(_ context.Context, b v1.BackupSchedule) (v1.BackupSchedule, error) {
	return stubPut(s, s.backupSchedules, "BackupSchedule", nsKey(b.Namespace, b.Name), b, false)
}

func (s *StubHandlers) ListBackupSchedules(_ context.Context, ns v1.NamespaceName) ([]v1.BackupSchedule, error) {
	return stubList(s, s.backupSchedules, ns)
}

func (s *StubHandlers) ReplaceBackupSchedule(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, b v1.BackupSchedule) (v1.BackupSchedule, error) {
	return stubPut(s, s.backupSchedules, "BackupSchedule", nsKey(ns, name), b, true)
}

func (s *StubHandlers) DeleteBackupSchedule(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.backupSchedules, "BackupSchedule", nsKey(ns, name))
}

func (s *StubHandlers) GetBackup(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Backup, error) {
	return stubGet(s, s.backups, "Backup", nsKey(ns, name))
}

func (s *StubHandlers) CreateBackup(_ context.Context, b v1.Backup) (v1.Backup, error) {
	return stubPut(s, s.backups, "Backup", nsKey(b.Namespace, b.Name), b, false)
}

func (s *StubHandlers) ListBackups(_ context.Context, ns v1.NamespaceName) ([]v1.Backup, error) {
	return stubList(s, s.backups, ns)
}

func (s *StubHandlers) ReplaceBackup(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, b v1.Backup) (v1.Backup, error) {
	return stubPut(s, s.backups, "Backup", nsKey(ns, name), b, true)
}

func (s *StubHandlers) DeleteBackup(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.backups, "Backup", nsKey(ns, name))
}

func (s *StubHandlers) GetRestore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Restore, error) {
	return stubGet(s, s.restores, "Restore", nsKey(ns, name))
}

func (s *StubHandlers) CreateRestore(_ context.Context, r v1.Restore) (v1.Restore, error) {
	return stubPut(s, s.restores, "Restore", nsKey(r.Namespace, r.Name), r, false)
}

func (s *StubHandlers) ListRestores(_ context.Context, ns v1.NamespaceName) ([]v1.Restore, error) {
	return stubList(s, s.restores, ns)
}

func (s *StubHandlers) ReplaceRestore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, r v1.Restore) (v1.Restore, error) {
	return stubPut(s, s.restores, "Restore", nsKey(ns, name), r, true)
}

func (s *StubHandlers) DeleteRestore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.restores, "Restore", nsKey(ns, name))
}

// stubGet, stubPut, stubList and stubDelete keep one crudKind in a stub map, keyed by nsKey for a namespaced kind and
// by name for a cluster-scoped one.
func stubGet[T crudKind](s *StubHandlers, m map[string]T, kind, key string) (T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := m[key]
	if !ok {
		return v, fault.NotFoundf("StubHandlers.Get"+kind, "%s %s not found", kind, key)
	}
	return v, nil
}

// stubPut creates (replace false: the key must be new) or replaces (replace true: the key must exist).
func stubPut[T crudKind](s *StubHandlers, m map[string]T, kind, key string, v T, replace bool) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero T
	switch _, exists := m[key]; {
	case exists && !replace:
		return zero, fault.Conflictf("StubHandlers.Create"+kind, "%s %s already exists", kind, key)
	case !exists && replace:
		return zero, fault.NotFoundf("StubHandlers.Replace"+kind, "%s %s not found", kind, key)
	}
	m[key] = v
	return v, nil
}

// stubList lists m's objects in ns, or all of them for a cluster-scoped kind (ns empty).
func stubList[T crudKind, PT objectPtr[T]](s *StubHandlers, m map[string]T, ns v1.NamespaceName) ([]T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]T, 0)
	for _, v := range m {
		if ns == "" || PT(&v).GetNamespace() == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func stubDelete[T crudKind](s *StubHandlers, m map[string]T, kind, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := m[key]; !exists {
		return fault.NotFoundf("StubHandlers.Delete"+kind, "%s %s not found", kind, key)
	}
	delete(m, key)
	return nil
}

// ---- CatalogService (ADR-0086) ----

func (s *StubHandlers) GetCatalogService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.CatalogService, error) {
	return stubGet(s, s.catalogServices, "CatalogService", nsKey(ns, name))
}

func (s *StubHandlers) CreateCatalogService(_ context.Context, cs v1.CatalogService) (v1.CatalogService, error) {
	return stubPut(s, s.catalogServices, "CatalogService", nsKey(cs.Namespace, cs.Name), cs, false)
}

func (s *StubHandlers) ListCatalogServices(_ context.Context, ns v1.NamespaceName) ([]v1.CatalogService, error) {
	return stubList(s, s.catalogServices, ns)
}

func (s *StubHandlers) ReplaceCatalogService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, cs v1.CatalogService) (v1.CatalogService, error) {
	return stubPut(s, s.catalogServices, "CatalogService", nsKey(ns, name), cs, true)
}

func (s *StubHandlers) GetIdentity(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Identity, error) {
	return stubGet(s, s.identities, "Identity", nsKey(ns, name))
}

func (s *StubHandlers) CreateIdentity(_ context.Context, id v1.Identity) (v1.Identity, error) {
	return stubPut(s, s.identities, "Identity", nsKey(id.Namespace, id.Name), id, false)
}

func (s *StubHandlers) ListIdentities(_ context.Context, ns v1.NamespaceName) ([]v1.Identity, error) {
	return stubList(s, s.identities, ns)
}

func (s *StubHandlers) ReplaceIdentity(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, id v1.Identity) (v1.Identity, error) {
	return stubPut(s, s.identities, "Identity", nsKey(ns, name), id, true)
}

func (s *StubHandlers) DeleteIdentity(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.identities, "Identity", nsKey(ns, name))
}

func (s *StubHandlers) GetRole(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Role, error) {
	return stubGet(s, s.roles, "Role", nsKey(ns, name))
}

func (s *StubHandlers) CreateRole(_ context.Context, ro v1.Role) (v1.Role, error) {
	return stubPut(s, s.roles, "Role", nsKey(ro.Namespace, ro.Name), ro, false)
}

func (s *StubHandlers) ListRoles(_ context.Context, ns v1.NamespaceName) ([]v1.Role, error) {
	return stubList(s, s.roles, ns)
}

func (s *StubHandlers) ReplaceRole(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, ro v1.Role) (v1.Role, error) {
	return stubPut(s, s.roles, "Role", nsKey(ns, name), ro, true)
}

func (s *StubHandlers) DeleteRole(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.roles, "Role", nsKey(ns, name))
}

func (s *StubHandlers) GetRolesAssignment(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.RolesAssignment, error) {
	return stubGet(s, s.rolesAssignments, "RolesAssignment", nsKey(ns, name))
}

func (s *StubHandlers) CreateRolesAssignment(_ context.Context, ra v1.RolesAssignment) (v1.RolesAssignment, error) {
	return stubPut(s, s.rolesAssignments, "RolesAssignment", nsKey(ra.Namespace, ra.Name), ra, false)
}

func (s *StubHandlers) ListRolesAssignments(_ context.Context, ns v1.NamespaceName) ([]v1.RolesAssignment, error) {
	return stubList(s, s.rolesAssignments, ns)
}

func (s *StubHandlers) ReplaceRolesAssignment(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, ra v1.RolesAssignment) (v1.RolesAssignment, error) {
	return stubPut(s, s.rolesAssignments, "RolesAssignment", nsKey(ns, name), ra, true)
}

func (s *StubHandlers) DeleteRolesAssignment(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.rolesAssignments, "RolesAssignment", nsKey(ns, name))
}

func (s *StubHandlers) DeleteCatalogService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.catalogServices, "CatalogService", nsKey(ns, name))
}

// ---- SQLDatabase (ADR-0232) ----

func (s *StubHandlers) GetSQLDatabase(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.SQLDatabase, error) {
	return stubGet(s, s.sqlDatabases, "SQLDatabase", nsKey(ns, name))
}

func (s *StubHandlers) CreateSQLDatabase(_ context.Context, db v1.SQLDatabase) (v1.SQLDatabase, error) {
	return stubPut(s, s.sqlDatabases, "SQLDatabase", nsKey(db.Namespace, db.Name), db, false)
}

func (s *StubHandlers) ListSQLDatabases(_ context.Context, ns v1.NamespaceName) ([]v1.SQLDatabase, error) {
	return stubList(s, s.sqlDatabases, ns)
}

func (s *StubHandlers) ReplaceSQLDatabase(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, db v1.SQLDatabase) (v1.SQLDatabase, error) {
	return stubPut(s, s.sqlDatabases, "SQLDatabase", nsKey(ns, name), db, true)
}

func (s *StubHandlers) DeleteSQLDatabase(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.sqlDatabases, "SQLDatabase", nsKey(ns, name))
}

// ---- Policy (ADR-0074) ----

func (s *StubHandlers) GetPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Policy, error) {
	return stubGet(s, s.policies, "Policy", nsKey(ns, name))
}

func (s *StubHandlers) CreatePolicy(_ context.Context, pol v1.Policy) (v1.Policy, error) {
	return stubPut(s, s.policies, "Policy", nsKey(pol.Namespace, pol.Name), pol, false)
}

func (s *StubHandlers) ListPolicies(_ context.Context, ns v1.NamespaceName) ([]v1.Policy, error) {
	return stubList(s, s.policies, ns)
}

func (s *StubHandlers) ReplacePolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, pol v1.Policy) (v1.Policy, error) {
	return stubPut(s, s.policies, "Policy", nsKey(ns, name), pol, true)
}

func (s *StubHandlers) DeletePolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.policies, "Policy", nsKey(ns, name))
}

// ---- Grant ----

func (s *StubHandlers) GetGrant(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Grant, error) {
	return stubGet(s, s.grants, "Grant", nsKey(ns, name))
}

func (s *StubHandlers) CreateGrant(_ context.Context, g v1.Grant) (v1.Grant, error) {
	return stubPut(s, s.grants, "Grant", nsKey(g.Namespace, g.Name), g, false)
}

func (s *StubHandlers) ListGrants(_ context.Context, ns v1.NamespaceName) ([]v1.Grant, error) {
	return stubList(s, s.grants, ns)
}

func (s *StubHandlers) ReplaceGrant(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, g v1.Grant) (v1.Grant, error) {
	return stubPut(s, s.grants, "Grant", nsKey(ns, name), g, true)
}

func (s *StubHandlers) DeleteGrant(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.grants, "Grant", nsKey(ns, name))
}

// ---- EgressPolicy ----

func (s *StubHandlers) GetEgressPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EgressPolicy, error) {
	return stubGet(s, s.egressPolicies, "EgressPolicy", nsKey(ns, name))
}

func (s *StubHandlers) CreateEgressPolicy(_ context.Context, ep v1.EgressPolicy) (v1.EgressPolicy, error) {
	return stubPut(s, s.egressPolicies, "EgressPolicy", nsKey(ep.Namespace, ep.Name), ep, false)
}

func (s *StubHandlers) ListEgressPolicies(_ context.Context, ns v1.NamespaceName) ([]v1.EgressPolicy, error) {
	return stubList(s, s.egressPolicies, ns)
}

func (s *StubHandlers) ReplaceEgressPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, ep v1.EgressPolicy) (v1.EgressPolicy, error) {
	return stubPut(s, s.egressPolicies, "EgressPolicy", nsKey(ns, name), ep, true)
}

func (s *StubHandlers) DeleteEgressPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.egressPolicies, "EgressPolicy", nsKey(ns, name))
}

// ---- Invocation ----

func (s *StubHandlers) GetInvocation(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Invocation, error) {
	return stubGet(s, s.invocations, "Invocation", nsKey(ns, name))
}

func (s *StubHandlers) CreateInvocation(_ context.Context, inv v1.Invocation) (v1.Invocation, error) {
	return stubPut(s, s.invocations, "Invocation", nsKey(inv.Namespace, inv.Name), inv, false)
}

func (s *StubHandlers) ListInvocations(_ context.Context, ns v1.NamespaceName) ([]v1.Invocation, error) {
	return stubList(s, s.invocations, ns)
}

func (s *StubHandlers) ReplaceInvocation(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, inv v1.Invocation) (v1.Invocation, error) {
	return stubPut(s, s.invocations, "Invocation", nsKey(ns, name), inv, true)
}

func (s *StubHandlers) DeleteInvocation(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.invocations, "Invocation", nsKey(ns, name))
}

// ---- RuntimeClass (cluster-scoped) ----

func (s *StubHandlers) GetRuntimeClass(_ context.Context, name v1.ObjectName) (v1.RuntimeClass, error) {
	return stubGet(s, s.runtimeClasses, "RuntimeClass", string(name))
}

func (s *StubHandlers) CreateRuntimeClass(_ context.Context, rc v1.RuntimeClass) (v1.RuntimeClass, error) {
	return stubPut(s, s.runtimeClasses, "RuntimeClass", string(rc.Name), rc, false)
}

func (s *StubHandlers) ListRuntimeClasses(_ context.Context) ([]v1.RuntimeClass, error) {
	return stubList(s, s.runtimeClasses, "")
}

func (s *StubHandlers) ReplaceRuntimeClass(_ context.Context, name v1.ObjectName, rc v1.RuntimeClass) (v1.RuntimeClass, error) {
	return stubPut(s, s.runtimeClasses, "RuntimeClass", string(name), rc, true)
}

func (s *StubHandlers) DeleteRuntimeClass(_ context.Context, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.runtimeClasses, "RuntimeClass", string(name))
}

// ---- WorkerNode (cluster-scoped) ----

func (s *StubHandlers) GetWorkerNode(_ context.Context, name v1.ObjectName) (v1.WorkerNode, error) {
	return stubGet(s, s.workerNodes, "WorkerNode", string(name))
}

func (s *StubHandlers) CreateWorkerNode(_ context.Context, wr v1.WorkerNode) (v1.WorkerNode, error) {
	return stubPut(s, s.workerNodes, "WorkerNode", string(wr.Name), wr, false)
}

func (s *StubHandlers) ListWorkerNodes(_ context.Context) ([]v1.WorkerNode, error) {
	return stubList(s, s.workerNodes, "")
}

func (s *StubHandlers) ReplaceWorkerNode(_ context.Context, name v1.ObjectName, wr v1.WorkerNode) (v1.WorkerNode, error) {
	return stubPut(s, s.workerNodes, "WorkerNode", string(name), wr, true)
}

func (s *StubHandlers) DeleteWorkerNode(_ context.Context, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.workerNodes, "WorkerNode", string(name))
}

// ---- Gateway (cluster-scoped) ----

func (s *StubHandlers) GetGateway(_ context.Context, name v1.ObjectName) (v1.Gateway, error) {
	return stubGet(s, s.gateways, "Gateway", string(name))
}

func (s *StubHandlers) CreateGateway(_ context.Context, gw v1.Gateway) (v1.Gateway, error) {
	return stubPut(s, s.gateways, "Gateway", string(gw.Name), gw, false)
}

func (s *StubHandlers) ListGateways(_ context.Context) ([]v1.Gateway, error) {
	return stubList(s, s.gateways, "")
}

func (s *StubHandlers) ReplaceGateway(_ context.Context, name v1.ObjectName, gw v1.Gateway) (v1.Gateway, error) {
	return stubPut(s, s.gateways, "Gateway", string(name), gw, true)
}

func (s *StubHandlers) DeleteGateway(_ context.Context, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.gateways, "Gateway", string(name))
}

// Compile-time check: StubHandlers implements Handlers.
var _ Handlers = (*StubHandlers)(nil)

// ---- Workflow (ADR-0094) ----

func (s *StubHandlers) GetWorkflow(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Workflow, error) {
	return stubGet(s, s.workflows, "Workflow", nsKey(ns, name))
}

func (s *StubHandlers) CreateWorkflow(_ context.Context, wf v1.Workflow) (v1.Workflow, error) {
	return stubPut(s, s.workflows, "Workflow", nsKey(wf.Namespace, wf.Name), wf, false)
}

func (s *StubHandlers) ListWorkflows(_ context.Context, ns v1.NamespaceName) ([]v1.Workflow, error) {
	return stubList(s, s.workflows, ns)
}

func (s *StubHandlers) ReplaceWorkflow(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, wf v1.Workflow) (v1.Workflow, error) {
	return stubPut(s, s.workflows, "Workflow", nsKey(ns, name), wf, true)
}

func (s *StubHandlers) DeleteWorkflow(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.workflows, "Workflow", nsKey(ns, name))
}

// ---- WorkflowRun (ADR-0094) ----

func (s *StubHandlers) GetWorkflowRun(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowRun, error) {
	return stubGet(s, s.workflowRuns, "WorkflowRun", nsKey(ns, name))
}

func (s *StubHandlers) CreateWorkflowRun(_ context.Context, run v1.WorkflowRun) (v1.WorkflowRun, error) {
	return stubPut(s, s.workflowRuns, "WorkflowRun", nsKey(run.Namespace, run.Name), run, false)
}

func (s *StubHandlers) ListWorkflowRuns(_ context.Context, ns v1.NamespaceName) ([]v1.WorkflowRun, error) {
	return stubList(s, s.workflowRuns, ns)
}

func (s *StubHandlers) ReplaceWorkflowRun(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, run v1.WorkflowRun) (v1.WorkflowRun, error) {
	return stubPut(s, s.workflowRuns, "WorkflowRun", nsKey(ns, name), run, true)
}

func (s *StubHandlers) DeleteWorkflowRun(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.workflowRuns, "WorkflowRun", nsKey(ns, name))
}

func (s *StubHandlers) GetSensor(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Sensor, error) {
	return stubGet(s, s.sensors, "Sensor", nsKey(ns, name))
}

func (s *StubHandlers) CreateSensor(_ context.Context, se v1.Sensor) (v1.Sensor, error) {
	return stubPut(s, s.sensors, "Sensor", nsKey(se.Namespace, se.Name), se, false)
}

func (s *StubHandlers) ListSensors(_ context.Context, ns v1.NamespaceName) ([]v1.Sensor, error) {
	return stubList(s, s.sensors, ns)
}

func (s *StubHandlers) ReplaceSensor(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, se v1.Sensor) (v1.Sensor, error) {
	return stubPut(s, s.sensors, "Sensor", nsKey(ns, name), se, true)
}

func (s *StubHandlers) DeleteSensor(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, _ string) error {
	return stubDelete(s, s.sensors, "Sensor", nsKey(ns, name))
}
