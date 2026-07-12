package db

import (
	"encoding/json"
	"time"

	"gopkg.in/yaml.v3"
)

// yamlToJSON converts a vm.yaml file's bytes into a JSON string suitable for
// storage, and extracts the template name and created_at timestamp.
func yamlToJSON(data []byte) (cfgJSON, template string, createdAt time.Time, err error) {
	var raw map[string]any
	if err = yaml.Unmarshal(data, &raw); err != nil {
		return cfgJSON, template, createdAt, err
	}

	if t, ok := raw["template"].(string); ok {
		template = t
	}
	if s, ok := raw["created_at"].(string); ok {
		createdAt, _ = time.Parse(time.RFC3339, s)
	}
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	b, err := json.Marshal(raw)
	if err != nil {
		return cfgJSON, template, createdAt, err
	}
	cfgJSON = string(b)
	return cfgJSON, template, createdAt, err
}
