package config

import "encoding/json"

// FlowToMap converts a flow to the TOML map shape the config patcher writes.
func FlowToMap(f FlowConfig) (map[string]any, error) {
	buf, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, err
	}
	NormalizeNumbers(m)
	return m, nil
}
