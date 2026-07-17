package config_resolve

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// k8sEnvValues discovers Kubernetes manifest YAML files under dir (recursively)
// and extracts env var name → value pairs from Pod/Deployment/StatefulSet/Job
// container env: sections. Each distinct YAML file is one overlay/environment
// (fan-out for same-var-different-file). Values have quotes stripped. The
// source ref is "rel-path:line". Returns nil on missing or unreadable dirs.
func k8sEnvValues(dir string) (map[string][]resolvedValue, error) {
	var yamlFiles []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".yaml" || ext == ".yml" {
			yamlFiles = append(yamlFiles, p)
		}
		return nil
	})
	sort.Strings(yamlFiles)

	result := make(map[string][]resolvedValue)
	for _, p := range yamlFiles {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(dir, p)
		extractK8sEnvVars(data, rel, result)
	}
	return result, nil
}

// k8sDoc is the minimal subset of a Kubernetes manifest we inspect.
type k8sDoc struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers     []k8sContainer `yaml:"containers"`
				InitContainers []k8sContainer `yaml:"initContainers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type k8sContainer struct {
	Env []struct {
		Name  string `yaml:"name"`
		Value string `yaml:"value"`
	} `yaml:"env"`
}

func extractK8sEnvVars(data []byte, relPath string, out map[string][]resolvedValue) {
	var doc k8sDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return
	}
	containers := append(doc.Spec.Template.Spec.Containers, doc.Spec.Template.Spec.InitContainers...)
	for _, c := range containers {
		for i, e := range c.Env {
			if e.Name == "" || e.Value == "" {
				continue
			}
			val := stripConfigValue(e.Value)
			if val == "" {
				continue
			}
			ref := configRef(relPath, i+1)
			out[e.Name] = appendUnique(out[e.Name], resolvedValue{value: val, ref: ref})
		}
	}
}
