package v1alpha1

// Route is a namespaced resource that defines HTTP routing rules for functions.
// Status-bearing. Behavioral spec fields (rules, traffic split) owned by F10.
type Route struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       RouteSpec   `json:"spec"`
	Status     RouteStatus `json:"status,omitempty"`
}

// RouteSpec holds the desired state. Behavioral fields owned by F10 (routes / traffic split).
type RouteSpec struct{}

// RouteStatus holds the observed state.
type RouteStatus struct {
	Status `json:",inline"`
}

// GroupVersionKind returns the constant GVK for Route.
func (r *Route) GroupVersionKind() GroupVersionKind { return KindRoute.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (r *Route) Validate() error { return validateMeta(r.TypeMeta, &r.ObjectMeta, KindRoute) }

// GetStatus returns the shared Status pointer, implementing StatusObject.
func (r *Route) GetStatus() *Status { return &r.Status.Status }
