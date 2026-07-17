package config_resolve

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// terraformEnvValues discovers *.tfvars and *.tfvars.json files under dir
// and extracts variable name → value pairs. Each file is one environment
// overlay. Values have surrounding quotes stripped. Source ref is "rel:line".
// HCL2 full parse is avoided intentionally: only simple key = "value" / key =
// value lines are read; complex expressions stay unresolved (they are not
// config values this provider can safely emit). Returns nil for missing dirs.
func terraformEnvValues(dir string) (map[string][]resolvedValue, error) {
	var files []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, ".tfvars") || strings.HasSuffix(name, ".tfvars.json") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	result := make(map[string][]resolvedValue)
	for _, p := range files {
		rel, _ := filepath.Rel(dir, p)
		if err := readTFVars(p, rel, result); err != nil {
			continue
		}
	}
	return result, nil
}

func readTFVars(path, relName string, out map[string][]resolvedValue) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := stripConfigValue(strings.TrimSpace(line[idx+1:]))
		if key == "" || val == "" {
			continue
		}
		// Skip complex HCL expressions (multi-token right-hand side after quotes stripped).
		if strings.ContainsAny(val, "{}[]()$") {
			continue
		}
		ref := configRef(relName, lineNum)
		out[key] = appendUnique(out[key], resolvedValue{value: val, ref: ref})
	}
	return scanner.Err()
}
