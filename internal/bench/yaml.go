package bench

import "gopkg.in/yaml.v3"

// yamlUnmarshal is a thin wrapper so the Aider source does not pull the YAML import into the rest of the file.
func yamlUnmarshal(b []byte, out any) error { return yaml.Unmarshal(b, out) }
