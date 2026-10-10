package v1alpha1

import (
	"maps"
	"regexp"
	"slices"

	"github.com/pyvvo/funcd/api/fault"
)

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

// Validate performs envelope validation, then requires every data key to be an env-var name.
func (c *ConfigMap) Validate() error {
	if err := validateMeta(c.TypeMeta, &c.ObjectMeta, KindConfigMap); err != nil {
		return err
	}
	return validateEnvKeys("ConfigMap.Validate", c.Spec.Data)
}

// envName is the env-var-name pattern for ConfigMap and Secret data keys (ADR-0048): the keys
// become worker env vars (ADR-0057, ADR-0093), so a key like "A=B" must never reach the worker.
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// validateEnvKeys rejects the first data key, in sorted order, that is not an env-var name.
func validateEnvKeys[V string | []byte](op string, data map[string]V) error {
	for _, k := range slices.Sorted(maps.Keys(data)) {
		if err := validateEnvKey(op, "spec.data key", k); err != nil {
			return err
		}
	}
	return nil
}

// validateEnvKey rejects key, at field, unless it is an env-var name.
func validateEnvKey(op, field, key string) error {
	if !envName.MatchString(key) {
		return fault.Invalidf(op, "%s %q is not an env-var name (must match %s)", field, key, envName)
	}
	return nil
}
