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
	Data map[string]string `json:"data,omitempty" doc:"Keys and their string values, for settings that are not secret. Each key is an environment variable name: ASCII letters, digits and _, not starting with a digit. Each value must be UTF-8 text with no NUL byte: a Function or a Lake that binds a ConfigMap holding another value does not start. Each worker of a Function that lists the ConfigMap in bindings.config gets each key as an environment variable when it starts, except a key that starts with FUNCD_ or that a bound Secret also holds; and while the S3 gateway is on, a Function that binds a Bucket gets the AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY that funcd issues it, in place of the ConfigMap's. A Lake that lists it gives its engine only the keys that the Lake's bindings.config lets through."`
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
