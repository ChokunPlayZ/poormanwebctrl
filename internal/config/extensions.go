package config

import (
	"encoding/json"
	"fmt"
)

// DecodeExtension decodes feature-owned configuration without coupling that
// feature's schema to the core Config type. The boolean is false when the
// extension is not configured.
func (c Config) DecodeExtension(name string, destination any) (bool, error) {
	value, ok := c.Extensions[name]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(value, destination); err != nil {
		return true, fmt.Errorf("decode extension %q: %w", name, err)
	}
	return true, nil
}

// SetExtension encodes feature-owned configuration under a stable name.
func (c *Config) SetExtension(name string, value any) error {
	if !extensionKeyRE.MatchString(name) {
		return fmt.Errorf("invalid extension name %q", name)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode extension %q: %w", name, err)
	}
	if c.Extensions == nil {
		c.Extensions = make(map[string]json.RawMessage)
	}
	c.Extensions[name] = encoded
	return nil
}

// RemoveExtension removes feature-owned settings without affecting core
// configuration.
func (c *Config) RemoveExtension(name string) {
	delete(c.Extensions, name)
}
