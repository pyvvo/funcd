package v1alpha1

// ConfigMap is a namespaced, pure-data resource for non-sensitive configuration key/values.
// ConfigMap does NOT have status — it implements Object, not StatusObject.
type ConfigMap struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       ConfigMapSpec `json:"spec"`
}

// ConfigMapSpec holds the configuration data. map[string]string is allowed under ADR-0002
// (the any-ban targets interface{}/any/map[string]any).
type ConfigMapSpec struct {
	Data map[string]string `json:"data,omitempty"`
}

// GroupVersionKind returns the constant GVK for ConfigMap.
func (c *ConfigMap) GroupVersionKind() GroupVersionKind { return KindConfigMap.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (c *ConfigMap) Validate() error { return validateMeta(c.TypeMeta, &c.ObjectMeta, KindConfigMap) }
