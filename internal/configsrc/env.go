package configsrc

import (
	"bufio"
	"os"
	"sort"
	"strings"
)

// dotenvValues reads all .env* files from dir and returns a map from
// variable name to a list of {value, ref} pairs. Each file is one
// "environment"; if the same variable appears in multiple files all values are
// kept (fan-out, bug-class rule 1). Values have surrounding quotes stripped
// (bug-class rule 6). Only KEY=value lines are read; blank lines and
// #-comments are skipped. The source ref is "rel-path:line".
func dotenvValues(dir string) map[string][]Value {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Directory may not exist; graceful degradation.
		return nil
	}
	var names []string
	for _, de := range entries {
		if de.IsDir() || !isDotenvFile(de.Name()) {
			continue
		}
		names = append(names, de.Name())
	}
	sort.Strings(names)

	result := make(map[string][]Value)
	for _, name := range names {
		// Unreadable file: skip, don't fail the whole load.
		_ = readDotenv(dir+"/"+name, name, result)
	}
	return result
}

func isDotenvFile(name string) bool {
	return name == ".env" ||
		strings.HasPrefix(name, ".env.") ||
		strings.HasSuffix(name, ".env")
}

func readDotenv(path, relName string, out map[string][]Value) error {
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
		val := stripValue(strings.TrimSpace(line[idx+1:]))
		if key == "" || val == "" {
			continue
		}
		out[key] = appendUnique(out[key], Value{Value: val, Ref: ref(relName, lineNum)})
	}
	return scanner.Err()
}
