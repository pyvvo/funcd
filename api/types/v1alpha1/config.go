package v1alpha1

// Config is a namespaced, pure-data resource for non-sensitive configuration key/values.
// Config does NOT have status — it implements Object, not StatusObject.
type Config struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       ConfigSpec `json:"spec"`
}

// ConfigSpec holds the configuration data. map[string]string is allowed under ADR-0002
// (the any-ban targets interface{}/any/map[string]any).
type ConfigSpec struct {
	Data map[string]string `json:"data,omitempty"`
}

// GroupVersionKind returns the constant GVK for Config.
func (c *Config) GroupVersionKind() GroupVersionKind { return KindConfig.GVK() }

// Validate performs envelope validation via the shared validateMeta helper.
func (c *Config) Validate() error { return validateMeta(c.TypeMeta, &c.ObjectMeta, KindConfig) }
