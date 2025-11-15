package cpanel

import "gopkg.in/yaml.v3"

// yamlUnmarshal is a helper so export.go can unmarshal without importing yaml directly there.
func yamlUnmarshal(data []byte, out interface{}) error {
    return yaml.Unmarshal(data, out)
}
