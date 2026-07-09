package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"golang.org/x/mod/modfile"
)

// skipDirs are never descended into during discovery.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"tmp": true, "log": true, ".polyflow": true, "coverage": true,
}

// Discover walks root and auto-detects services from manifests:
// go.work (each used module), go.mod, package.json (npm/yarn workspaces
// expanded; Nx project.json treated as service roots), and Gemfile.
// Yarn portal:/link: dependencies become link hints. Paths are stored
// relative to root so the config is portable across machines.
func Discover(root string) (*WorkspaceConfig, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	cfg := &WorkspaceConfig{
		Name:    filepath.Base(absRoot),
		Version: "1",
		Index: IndexConfig{Exclude: DefaultExcludes()},
	}

	seen := map[string]bool{} // relative service path → already added

	addService := func(dir, language string) {
		rel, err := filepath.Rel(absRoot, dir)
		if err != nil || strings.HasPrefix(rel, "..") {
			return
		}
		if seen[rel] {
			return
		}
		seen[rel] = true

		name := filepath.Base(dir)
		if rel == "." {
			name = filepath.Base(absRoot)
		}
		svc := Service{Name: uniqueName(cfg.Services, name), Path: "./" + filepath.ToSlash(rel), Language: language}
		if rel == "." {
			svc.Path = "."
		}

		hints, _ := DetectFrameworks(dir)
		for _, h := range hints {
			if svc.Language == "" {
				svc.Language = h.Language
			}
			switch h.Name {
			case "go-module", "node", "bundler", "pip", "cargo":
				continue
			}
			svc.Frameworks = append(svc.Frameworks, h.Name)
		}
		cfg.Services = append(cfg.Services, svc)
	}

	// go.work at root: each used module is a service.
	if data, err := os.ReadFile(filepath.Join(absRoot, "go.work")); err == nil {
		if wf, err := modfile.ParseWork("go.work", data, nil); err == nil {
			for _, use := range wf.Use {
				addService(filepath.Join(absRoot, use.Path), "go")
			}
		}
	}

	var portalLinks []portalDep

	walkErr := filepath.WalkDir(absRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != absRoot && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		dir := filepath.Dir(path)
		switch d.Name() {
		case "go.mod":
			addService(dir, "go")
		case "Gemfile":
			addService(dir, "ruby")
		case "project.json": // Nx project marker
			addService(dir, "")
		case "package.json":
			workspaces, portals := parsePackageJSON(path)
			if len(workspaces) > 0 {
				// Workspace root: expand member globs; only members become services.
				for _, pattern := range workspaces {
					matches, _ := doublestar.Glob(os.DirFS(dir), filepath.ToSlash(pattern))
					for _, m := range matches {
						memberDir := filepath.Join(dir, m)
						if _, err := os.Stat(filepath.Join(memberDir, "package.json")); err == nil {
							addService(memberDir, "")
						}
					}
				}
			} else {
				addService(dir, "")
			}
			portalLinks = append(portalLinks, portals...)
		}
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	// Portal/link dependencies become link hints between discovered services.
	for _, p := range portalLinks {
		fromRel, err := filepath.Rel(absRoot, p.fromDir)
		if err != nil {
			continue
		}
		from := serviceByPath(cfg.Services, fromRel)
		targetDir := filepath.Join(p.fromDir, p.targetPath)
		toRel, err := filepath.Rel(absRoot, targetDir)
		if err != nil {
			continue
		}
		to := serviceByPath(cfg.Services, toRel)
		if from == "" {
			continue
		}
		link := Link{From: from, To: to, Via: "portal", Hint: p.pkg + "=" + p.targetPath}
		if to == "" {
			link.To = p.pkg // cross-repo portal target outside this workspace
		}
		cfg.Links = append(cfg.Links, link)
	}

	sort.Slice(cfg.Services, func(i, j int) bool { return cfg.Services[i].Path < cfg.Services[j].Path })
	return cfg, nil
}

type portalDep struct {
	fromDir    string
	pkg        string
	targetPath string
}

// parsePackageJSON extracts workspace member globs and portal:/link:
// dependencies from a package.json file.
func parsePackageJSON(path string) (workspaces []string, portals []portalDep) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	var pkg struct {
		Workspaces      json.RawMessage   `json:"workspaces"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, nil
	}

	if len(pkg.Workspaces) > 0 {
		var list []string
		if json.Unmarshal(pkg.Workspaces, &list) != nil {
			var obj struct {
				Packages []string `json:"packages"`
			}
			if json.Unmarshal(pkg.Workspaces, &obj) == nil {
				list = obj.Packages
			}
		}
		workspaces = list
	}

	dir := filepath.Dir(path)
	collect := func(m map[string]string) {
		for name, spec := range m {
			for _, prefix := range []string{"portal:", "link:"} {
				if strings.HasPrefix(spec, prefix) {
					portals = append(portals, portalDep{
						fromDir:    dir,
						pkg:        name,
						targetPath: strings.TrimPrefix(spec, prefix),
					})
				}
			}
		}
	}
	collect(pkg.Dependencies)
	collect(pkg.DevDependencies)
	return workspaces, portals
}

// serviceByPath finds a discovered service whose path matches rel.
func serviceByPath(services []Service, rel string) string {
	rel = filepath.ToSlash(rel)
	for _, s := range services {
		p := strings.TrimPrefix(s.Path, "./")
		if p == rel || (rel == "." && s.Path == ".") {
			return s.Name
		}
	}
	return ""
}

// uniqueName disambiguates duplicate directory base names (e.g. two "api"
// dirs) by suffixing a counter.
func uniqueName(services []Service, name string) string {
	taken := map[string]bool{}
	for _, s := range services {
		taken[s.Name] = true
	}
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := name + "-" + string(rune('0'+i))
		if !taken[candidate] {
			return candidate
		}
	}
}
