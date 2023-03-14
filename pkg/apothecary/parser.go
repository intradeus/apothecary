package apothecary

import (
	"gopkg.in/yaml.v2"
)

func ParseYaml(data []byte) (map[string]interface{}, error) {
	var parsed map[string]interface{}
	err := yaml.Unmarshal(data, &parsed)
	return parsed, err
}
