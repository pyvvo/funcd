package controlplane

import (
	"context"
	"sync"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// StubHandlers is an in-memory Handlers implementation for spec-gen and tests.
// P-L/F07 replaces it with real store/controller-backed handlers.
type StubHandlers struct {
	mu sync.RWMutex

	namespaces     map[string]v1.Namespace
	resourceGroups map[string]v1.ResourceGroup
	functions      map[string]v1.Function
	revisions      map[string]v1.Revision
	routes         map[string]v1.Route
	services       map[string]v1.Service
	eventSources   map[string]v1.EventSource
	configMaps        map[string]v1.ConfigMap
	secrets        map[string]v1.Secret
	grants         map[string]v1.Grant
	egressPolicies map[string]v1.EgressPolicy
	invocations    map[string]v1.Invocation
	runtimeClasses map[string]v1.RuntimeClass
	workerNodes    map[string]v1.WorkerNode
	gateways       map[string]v1.Gateway
	kvstores       map[string]v1.KVStore
	policies       map[string]v1.Policy
}

// NewStubHandlers returns an initialized StubHandlers.
func NewStubHandlers() *StubHandlers {
	return &StubHandlers{
		namespaces:     make(map[string]v1.Namespace),
		resourceGroups: make(map[string]v1.ResourceGroup),
		functions:      make(map[string]v1.Function),
		revisions:      make(map[string]v1.Revision),
		routes:         make(map[string]v1.Route),
		services:       make(map[string]v1.Service),
		eventSources:   make(map[string]v1.EventSource),
		configMaps:        make(map[string]v1.ConfigMap),
		secrets:        make(map[string]v1.Secret),
		grants:         make(map[string]v1.Grant),
		egressPolicies: make(map[string]v1.EgressPolicy),
		invocations:    make(map[string]v1.Invocation),
		runtimeClasses: make(map[string]v1.RuntimeClass),
		workerNodes:    make(map[string]v1.WorkerNode),
		gateways:       make(map[string]v1.Gateway),
		kvstores:       make(map[string]v1.KVStore),
		policies:       make(map[string]v1.Policy),
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

func (s *StubHandlers) DeleteNamespace(_ context.Context, name v1.ObjectName) error {
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

func (s *StubHandlers) DeleteResourceGroup(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
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

func (s *StubHandlers) DeleteFunction(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
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

func (s *StubHandlers) CreateRevision(_ context.Context, rev v1.Revision) (v1.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(rev.Namespace, rev.Name)
	if _, exists := s.revisions[key]; exists {
		return v1.Revision{}, fault.Conflictf("StubHandlers.CreateRevision", "Revision %s already exists", key)
	}
	s.revisions[key] = rev
	return rev, nil
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

func (s *StubHandlers) ReplaceRevision(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, rev v1.Revision) (v1.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.revisions[key]; !exists {
		return v1.Revision{}, fault.NotFoundf("StubHandlers.ReplaceRevision", "Revision %s not found", key)
	}
	s.revisions[key] = rev
	return rev, nil
}

func (s *StubHandlers) DeleteRevision(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.revisions[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteRevision", "Revision %s not found", key)
	}
	delete(s.revisions, key)
	return nil
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

func (s *StubHandlers) DeleteRoute(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
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
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.services[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.Service{}, fault.NotFoundf("StubHandlers.GetService", "Service %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateService(_ context.Context, svc v1.Service) (v1.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(svc.Namespace, svc.Name)
	if _, exists := s.services[key]; exists {
		return v1.Service{}, fault.Conflictf("StubHandlers.CreateService", "Service %s already exists", key)
	}
	s.services[key] = svc
	return svc, nil
}

func (s *StubHandlers) ListServices(_ context.Context, ns v1.NamespaceName) ([]v1.Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Service, 0)
	for _, v := range s.services {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, svc v1.Service) (v1.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.services[key]; !exists {
		return v1.Service{}, fault.NotFoundf("StubHandlers.ReplaceService", "Service %s not found", key)
	}
	s.services[key] = svc
	return svc, nil
}

func (s *StubHandlers) DeleteService(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.services[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteService", "Service %s not found", key)
	}
	delete(s.services, key)
	return nil
}

// ---- EventSource ----

func (s *StubHandlers) GetEventSource(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EventSource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.eventSources[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.EventSource{}, fault.NotFoundf("StubHandlers.GetEventSource", "EventSource %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateEventSource(_ context.Context, es v1.EventSource) (v1.EventSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(es.Namespace, es.Name)
	if _, exists := s.eventSources[key]; exists {
		return v1.EventSource{}, fault.Conflictf("StubHandlers.CreateEventSource", "EventSource %s already exists", key)
	}
	s.eventSources[key] = es
	return es, nil
}

func (s *StubHandlers) ListEventSources(_ context.Context, ns v1.NamespaceName) ([]v1.EventSource, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.EventSource, 0)
	for _, v := range s.eventSources {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceEventSource(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, es v1.EventSource) (v1.EventSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.eventSources[key]; !exists {
		return v1.EventSource{}, fault.NotFoundf("StubHandlers.ReplaceEventSource", "EventSource %s not found", key)
	}
	s.eventSources[key] = es
	return es, nil
}

func (s *StubHandlers) DeleteEventSource(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.eventSources[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteEventSource", "EventSource %s not found", key)
	}
	delete(s.eventSources, key)
	return nil
}

// ---- ConfigMap ----

func (s *StubHandlers) GetConfigMap(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.ConfigMap, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.configMaps[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.ConfigMap{}, fault.NotFoundf("StubHandlers.GetConfigMap", "ConfigMap %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateConfigMap(_ context.Context, cfg v1.ConfigMap) (v1.ConfigMap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(cfg.Namespace, cfg.Name)
	if _, exists := s.configMaps[key]; exists {
		return v1.ConfigMap{}, fault.Conflictf("StubHandlers.CreateConfigMap", "ConfigMap %s already exists", key)
	}
	s.configMaps[key] = cfg
	return cfg, nil
}

func (s *StubHandlers) ListConfigMaps(_ context.Context, ns v1.NamespaceName) ([]v1.ConfigMap, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.ConfigMap, 0)
	for _, v := range s.configMaps {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceConfigMap(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, cfg v1.ConfigMap) (v1.ConfigMap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.configMaps[key]; !exists {
		return v1.ConfigMap{}, fault.NotFoundf("StubHandlers.ReplaceConfigMap", "ConfigMap %s not found", key)
	}
	s.configMaps[key] = cfg
	return cfg, nil
}

func (s *StubHandlers) DeleteConfigMap(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.configMaps[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteConfigMap", "ConfigMap %s not found", key)
	}
	delete(s.configMaps, key)
	return nil
}

// ---- Secret ----

func (s *StubHandlers) GetSecret(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.secrets[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.Secret{}, fault.NotFoundf("StubHandlers.GetSecret", "Secret %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateSecret(_ context.Context, sec v1.Secret) (v1.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(sec.Namespace, sec.Name)
	if _, exists := s.secrets[key]; exists {
		return v1.Secret{}, fault.Conflictf("StubHandlers.CreateSecret", "Secret %s already exists", key)
	}
	s.secrets[key] = sec
	return sec, nil
}

func (s *StubHandlers) ListSecrets(_ context.Context, ns v1.NamespaceName) ([]v1.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Secret, 0)
	for _, v := range s.secrets {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceSecret(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, sec v1.Secret) (v1.Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.secrets[key]; !exists {
		return v1.Secret{}, fault.NotFoundf("StubHandlers.ReplaceSecret", "Secret %s not found", key)
	}
	s.secrets[key] = sec
	return sec, nil
}

func (s *StubHandlers) DeleteSecret(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.secrets[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteSecret", "Secret %s not found", key)
	}
	delete(s.secrets, key)
	return nil
}

// ---- KVStore (ADR-0072) ----

func (s *StubHandlers) GetKVStore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.KVStore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.kvstores[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.KVStore{}, fault.NotFoundf("StubHandlers.GetKVStore", "KVStore %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateKVStore(_ context.Context, ks v1.KVStore) (v1.KVStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ks.Namespace, ks.Name)
	if _, exists := s.kvstores[key]; exists {
		return v1.KVStore{}, fault.Conflictf("StubHandlers.CreateKVStore", "KVStore %s already exists", key)
	}
	s.kvstores[key] = ks
	return ks, nil
}

func (s *StubHandlers) ListKVStores(_ context.Context, ns v1.NamespaceName) ([]v1.KVStore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.KVStore, 0)
	for _, v := range s.kvstores {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceKVStore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, ks v1.KVStore) (v1.KVStore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.kvstores[key]; !exists {
		return v1.KVStore{}, fault.NotFoundf("StubHandlers.ReplaceKVStore", "KVStore %s not found", key)
	}
	s.kvstores[key] = ks
	return ks, nil
}

func (s *StubHandlers) DeleteKVStore(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.kvstores[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteKVStore", "KVStore %s not found", key)
	}
	delete(s.kvstores, key)
	return nil
}

// ---- Policy (ADR-0074) ----

func (s *StubHandlers) GetPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.policies[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.Policy{}, fault.NotFoundf("StubHandlers.GetPolicy", "Policy %s/%s not found", ns, name)
}

func (s *StubHandlers) CreatePolicy(_ context.Context, pol v1.Policy) (v1.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(pol.Namespace, pol.Name)
	if _, exists := s.policies[key]; exists {
		return v1.Policy{}, fault.Conflictf("StubHandlers.CreatePolicy", "Policy %s already exists", key)
	}
	s.policies[key] = pol
	return pol, nil
}

func (s *StubHandlers) ListPolicies(_ context.Context, ns v1.NamespaceName) ([]v1.Policy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Policy, 0)
	for _, v := range s.policies {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplacePolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, pol v1.Policy) (v1.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.policies[key]; !exists {
		return v1.Policy{}, fault.NotFoundf("StubHandlers.ReplacePolicy", "Policy %s not found", key)
	}
	s.policies[key] = pol
	return pol, nil
}

func (s *StubHandlers) DeletePolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.policies[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeletePolicy", "Policy %s not found", key)
	}
	delete(s.policies, key)
	return nil
}

// ---- Grant ----

func (s *StubHandlers) GetGrant(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.grants[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.Grant{}, fault.NotFoundf("StubHandlers.GetGrant", "Grant %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateGrant(_ context.Context, g v1.Grant) (v1.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(g.Namespace, g.Name)
	if _, exists := s.grants[key]; exists {
		return v1.Grant{}, fault.Conflictf("StubHandlers.CreateGrant", "Grant %s already exists", key)
	}
	s.grants[key] = g
	return g, nil
}

func (s *StubHandlers) ListGrants(_ context.Context, ns v1.NamespaceName) ([]v1.Grant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Grant, 0)
	for _, v := range s.grants {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceGrant(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, g v1.Grant) (v1.Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.grants[key]; !exists {
		return v1.Grant{}, fault.NotFoundf("StubHandlers.ReplaceGrant", "Grant %s not found", key)
	}
	s.grants[key] = g
	return g, nil
}

func (s *StubHandlers) DeleteGrant(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.grants[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteGrant", "Grant %s not found", key)
	}
	delete(s.grants, key)
	return nil
}

// ---- EgressPolicy ----

func (s *StubHandlers) GetEgressPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.EgressPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.egressPolicies[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.EgressPolicy{}, fault.NotFoundf("StubHandlers.GetEgressPolicy", "EgressPolicy %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateEgressPolicy(_ context.Context, ep v1.EgressPolicy) (v1.EgressPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ep.Namespace, ep.Name)
	if _, exists := s.egressPolicies[key]; exists {
		return v1.EgressPolicy{}, fault.Conflictf("StubHandlers.CreateEgressPolicy", "EgressPolicy %s already exists", key)
	}
	s.egressPolicies[key] = ep
	return ep, nil
}

func (s *StubHandlers) ListEgressPolicies(_ context.Context, ns v1.NamespaceName) ([]v1.EgressPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.EgressPolicy, 0)
	for _, v := range s.egressPolicies {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceEgressPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, ep v1.EgressPolicy) (v1.EgressPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.egressPolicies[key]; !exists {
		return v1.EgressPolicy{}, fault.NotFoundf("StubHandlers.ReplaceEgressPolicy", "EgressPolicy %s not found", key)
	}
	s.egressPolicies[key] = ep
	return ep, nil
}

func (s *StubHandlers) DeleteEgressPolicy(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.egressPolicies[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteEgressPolicy", "EgressPolicy %s not found", key)
	}
	delete(s.egressPolicies, key)
	return nil
}

// ---- Invocation ----

func (s *StubHandlers) GetInvocation(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.Invocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.invocations[nsKey(ns, name)]; ok {
		return v, nil
	}
	return v1.Invocation{}, fault.NotFoundf("StubHandlers.GetInvocation", "Invocation %s/%s not found", ns, name)
}

func (s *StubHandlers) CreateInvocation(_ context.Context, inv v1.Invocation) (v1.Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(inv.Namespace, inv.Name)
	if _, exists := s.invocations[key]; exists {
		return v1.Invocation{}, fault.Conflictf("StubHandlers.CreateInvocation", "Invocation %s already exists", key)
	}
	s.invocations[key] = inv
	return inv, nil
}

func (s *StubHandlers) ListInvocations(_ context.Context, ns v1.NamespaceName) ([]v1.Invocation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Invocation, 0)
	for _, v := range s.invocations {
		if v.Namespace == ns {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *StubHandlers) ReplaceInvocation(_ context.Context, ns v1.NamespaceName, name v1.ObjectName, inv v1.Invocation) (v1.Invocation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.invocations[key]; !exists {
		return v1.Invocation{}, fault.NotFoundf("StubHandlers.ReplaceInvocation", "Invocation %s not found", key)
	}
	s.invocations[key] = inv
	return inv, nil
}

func (s *StubHandlers) DeleteInvocation(_ context.Context, ns v1.NamespaceName, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := nsKey(ns, name)
	if _, exists := s.invocations[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteInvocation", "Invocation %s not found", key)
	}
	delete(s.invocations, key)
	return nil
}

// ---- RuntimeClass (cluster-scoped) ----

func (s *StubHandlers) GetRuntimeClass(_ context.Context, name v1.ObjectName) (v1.RuntimeClass, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.runtimeClasses[string(name)]; ok {
		return v, nil
	}
	return v1.RuntimeClass{}, fault.NotFoundf("StubHandlers.GetRuntimeClass", "RuntimeClass %q not found", name)
}

func (s *StubHandlers) CreateRuntimeClass(_ context.Context, rc v1.RuntimeClass) (v1.RuntimeClass, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(rc.Name)
	if _, exists := s.runtimeClasses[key]; exists {
		return v1.RuntimeClass{}, fault.Conflictf("StubHandlers.CreateRuntimeClass", "RuntimeClass %q already exists", key)
	}
	s.runtimeClasses[key] = rc
	return rc, nil
}

func (s *StubHandlers) ListRuntimeClasses(_ context.Context) ([]v1.RuntimeClass, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.RuntimeClass, 0, len(s.runtimeClasses))
	for _, v := range s.runtimeClasses {
		out = append(out, v)
	}
	return out, nil
}

func (s *StubHandlers) ReplaceRuntimeClass(_ context.Context, name v1.ObjectName, rc v1.RuntimeClass) (v1.RuntimeClass, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.runtimeClasses[key]; !exists {
		return v1.RuntimeClass{}, fault.NotFoundf("StubHandlers.ReplaceRuntimeClass", "RuntimeClass %q not found", key)
	}
	s.runtimeClasses[key] = rc
	return rc, nil
}

func (s *StubHandlers) DeleteRuntimeClass(_ context.Context, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.runtimeClasses[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteRuntimeClass", "RuntimeClass %q not found", key)
	}
	delete(s.runtimeClasses, key)
	return nil
}

// ---- WorkerNode (cluster-scoped) ----

func (s *StubHandlers) GetWorkerNode(_ context.Context, name v1.ObjectName) (v1.WorkerNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.workerNodes[string(name)]; ok {
		return v, nil
	}
	return v1.WorkerNode{}, fault.NotFoundf("StubHandlers.GetWorkerNode", "WorkerNode %q not found", name)
}

func (s *StubHandlers) CreateWorkerNode(_ context.Context, wr v1.WorkerNode) (v1.WorkerNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(wr.Name)
	if _, exists := s.workerNodes[key]; exists {
		return v1.WorkerNode{}, fault.Conflictf("StubHandlers.CreateWorkerNode", "WorkerNode %q already exists", key)
	}
	s.workerNodes[key] = wr
	return wr, nil
}

func (s *StubHandlers) ListWorkerNodes(_ context.Context) ([]v1.WorkerNode, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.WorkerNode, 0, len(s.workerNodes))
	for _, v := range s.workerNodes {
		out = append(out, v)
	}
	return out, nil
}

func (s *StubHandlers) ReplaceWorkerNode(_ context.Context, name v1.ObjectName, wr v1.WorkerNode) (v1.WorkerNode, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.workerNodes[key]; !exists {
		return v1.WorkerNode{}, fault.NotFoundf("StubHandlers.ReplaceWorkerNode", "WorkerNode %q not found", key)
	}
	s.workerNodes[key] = wr
	return wr, nil
}

func (s *StubHandlers) DeleteWorkerNode(_ context.Context, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.workerNodes[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteWorkerNode", "WorkerNode %q not found", key)
	}
	delete(s.workerNodes, key)
	return nil
}

// ---- Gateway (cluster-scoped) ----

func (s *StubHandlers) GetGateway(_ context.Context, name v1.ObjectName) (v1.Gateway, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.gateways[string(name)]; ok {
		return v, nil
	}
	return v1.Gateway{}, fault.NotFoundf("StubHandlers.GetGateway", "Gateway %q not found", name)
}

func (s *StubHandlers) CreateGateway(_ context.Context, gw v1.Gateway) (v1.Gateway, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(gw.Name)
	if _, exists := s.gateways[key]; exists {
		return v1.Gateway{}, fault.Conflictf("StubHandlers.CreateGateway", "Gateway %q already exists", key)
	}
	s.gateways[key] = gw
	return gw, nil
}

func (s *StubHandlers) ListGateways(_ context.Context) ([]v1.Gateway, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]v1.Gateway, 0, len(s.gateways))
	for _, v := range s.gateways {
		out = append(out, v)
	}
	return out, nil
}

func (s *StubHandlers) ReplaceGateway(_ context.Context, name v1.ObjectName, gw v1.Gateway) (v1.Gateway, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.gateways[key]; !exists {
		return v1.Gateway{}, fault.NotFoundf("StubHandlers.ReplaceGateway", "Gateway %q not found", key)
	}
	s.gateways[key] = gw
	return gw, nil
}

func (s *StubHandlers) DeleteGateway(_ context.Context, name v1.ObjectName) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := string(name)
	if _, exists := s.gateways[key]; !exists {
		return fault.NotFoundf("StubHandlers.DeleteGateway", "Gateway %q not found", key)
	}
	delete(s.gateways, key)
	return nil
}

// Compile-time check: StubHandlers implements Handlers.
var _ Handlers = (*StubHandlers)(nil)
