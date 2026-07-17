package config_resolve

import (
	"bufio"
	"os"
	"strings"
)

// dotenvValues reads all .env* files from dir and returns a map from
// variable name to a list of {value, sourceRef} pairs. Each file is one
// "environment"; if the same variable appears in multiple files all values are
// kept (fan-out, bug-class rule 1). Values have surrounding quotes stripped
// (bug-class rule 6). Only KEY=value lines are read; blank lines and
// #-comments are skipped. The source ref is "rel-path:line".
func dotenvValues(dir string) (map[string][]resolvedValue, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory may not exist; graceful degradation.
		return nil, nil
	}
	result := make(map[string][]resolvedValue)
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || !isDotenvFile(name) {
			continue
		}
		path := dir + "/" + name
		if err2 := readDotenv(path, name, result); err2 != nil {
			// Unreadable file: skip, don't fail the whole provider.
			continue
		}
	}
	return result, nil
}

func isDotenvFile(name string) bool {
	return name == ".env" ||
		strings.HasPrefix(name, ".env.") ||
		strings.HasSuffix(name, ".env")
}

func readDotenv(path, relName string, out map[string][]resolvedValue) error {
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
		if line == "" || strings.HasPrefix(line, "#") {
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
		ref := configRef(relName, lineNum)
		out[key] = appendUnique(out[key], resolvedValue{value: val, ref: ref})
	}
	return scanner.Err()
}
