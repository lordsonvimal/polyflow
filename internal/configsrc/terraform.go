package configsrc

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
// config values a consumer can safely emit). Returns nil for missing dirs.
func terraformEnvValues(dir string) map[string][]Value {
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

	result := make(map[string][]Value)
	for _, p := range files {
		rel, _ := filepath.Rel(dir, p)
		_ = readTFVars(p, rel, result)
	}
	return result
}

func readTFVars(path, relName string, out map[string][]Value) error {
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
		val := stripValue(strings.TrimSpace(line[idx+1:]))
		if key == "" || val == "" {
			continue
		}
		// Skip complex HCL expressions (multi-token right-hand side after quotes stripped).
		if strings.ContainsAny(val, "{}[]()$") {
			continue
		}
		out[key] = appendUnique(out[key], Value{Value: val, Ref: ref(relName, lineNum)})
	}
	return scanner.Err()
}
