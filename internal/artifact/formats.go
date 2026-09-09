package artifact

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

func init() {
	Register(jsonReader{})
	Register(yamlReader{})
	Register(envReader{})
}

// jsonReader flattens a JSON document. Line numbers are not reported
// (encoding/json discards position).
type jsonReader struct{}

func (jsonReader) Exts() []string { return []string{".json"} }

func (jsonReader) Read(file string, src []byte) (*Artifact, error) {
	var root interface{}
	if err := json.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	a := &Artifact{Format: "json"}
	flattenTree(root, nil, &a.Leaves)
	return a, nil
}

// yamlReader flattens a single-document YAML file. Multi-document streams read
// only their first document — matches the previous linker behaviour.
type yamlReader struct{}

func (yamlReader) Exts() []string { return []string{".yaml", ".yml"} }

func (yamlReader) Read(file string, src []byte) (*Artifact, error) {
	var root interface{}
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	a := &Artifact{Format: "yaml"}
	flattenTree(root, nil, &a.Leaves)
	return a, nil
}

// envReader flattens a dotenv-style file: one KEY=VALUE per line. Blank lines
// and lines beginning with '#' are ignored; surrounding quotes on the value are
// stripped; a leading "export " is dropped. Path is the single-element key
// chain, and Line is reported.
type envReader struct{}

func (envReader) Exts() []string { return []string{".env"} }

func (envReader) Read(file string, src []byte) (*Artifact, error) {
	a := &Artifact{Format: "env"}
	for i, raw := range strings.Split(string(src), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		a.Leaves = append(a.Leaves, Leaf{Path: []string{key}, Value: val, Line: i + 1})
	}
	return a, nil
}
