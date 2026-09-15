package factpipe

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// resolve.go is FX.8 — the resolve_path primitive (plan § FX.0). Resolving a
// path reference — a Sass `@import` specifier, a Sprockets `//= require`
// directive, a Rails `render "users/row"` — is candidate enumeration (append an
// extension, try an `_partial` prefix, an `/index` file, walk the load roots)
// followed by a first-hit lookup against the real file set.
//
// Datalog cannot build the candidate strings: its builtins (contains, prefix,
// le, ne) are always-complete tests, never generators, and parse.go forbids a
// builtin in the head. The FX.2 `extract:` verbs are per-capture and blind to
// the service's file list. So path resolution is a primitive: a `resolve:`
// block in the framework YAML, compiled like `emit:` and run between extract
// and derive, that turns an input relation of (File, Line, Spec) triples into
// a resolved relation of (File, Line, Spec, Target, Rank) facts a `.dl` rule
// then joins — Target is a real repo-relative path in service_file, Rank is the
// 0-based precedence of the candidate that hit (so a rule can min() when one
// spec resolves via several input rows).
//
// The primitive names no framework and hard-codes no directory: every root,
// extension and prefix is a YAML knob. It is generic by the plan's test —
// stylesheet_imports, sprockets_assets and rails_views are all one `resolve:`
// block apiece.

// ResolveSpec is one entry of a framework YAML's `resolve:` list.
//
//	resolve:
//	  - relation: sass_target          # output relation (File, Line, Spec, Target, Rank)
//	    from: sass_import              # input relation; args 0,1,2 = File, Line, Spec
//	    own_dir_first: true            # try the importing file's directory before roots
//	    roots: ["app/assets/stylesheets", "vendor/assets/stylesheets"]
//	    extensions: [".scss", ".css"]
//	    partial_prefix: "_"            # also try _<base> in the same directory
//	    index_files: ["index"]         # a bare directory ref -> <dir>/index<ext>
//
// `dir: true` switches from file lookup to directory-fanout resolution
// (Sprockets' `require_tree`/`require_directory`): the input relation's arg 3
// is an optional extension filter, spec names a directory rather than a file,
// and a hit produces one output row per file the winning candidate directory
// contains — `recursive: true` walks subdirectories (require_tree), false
// stops at immediate children (require_directory). Sprockets' own rule (the
// first load path holding *any* matching file wins, matching the plain file
// mode's first-candidate-wins precedence) is why this is still one candidate
// walk, just over directory contents instead of an exact name.
type ResolveSpec struct {
	Relation      string   `yaml:"relation"`
	From          string   `yaml:"from"`
	Roots         []string `yaml:"roots"`
	Extensions    []string `yaml:"extensions"`
	PartialPrefix string   `yaml:"partial_prefix"`
	IndexFiles    []string `yaml:"index_files"`
	OwnDirFirst   bool     `yaml:"own_dir_first"`
	Dir           bool     `yaml:"dir"`
	Recursive     bool     `yaml:"recursive"`
	// StripExt lists specifier extensions tried BOTH verbatim and with the
	// extension dropped and every configured Extensions candidate tried
	// against the bare stem — the Node16/TS module-resolution convention
	// where a relative specifier names the compiled ".js" output but the
	// indexed source is ".ts" (js_lazy_import_calls' confirmed live shape,
	// mirroring internal/linker/import_edges.go's resolveJSImportPath, kept
	// hand-written Go precisely for this rule). Verbatim always wins first
	// (added before the stripped candidates), so a real same-extension file
	// is never shadowed by the swap.
	StripExt []string `yaml:"strip_ext"`
}

// CompiledResolve is a validated ResolveSpec.
type CompiledResolve struct{ spec ResolveSpec }

// Relation is the derived relation this resolve produces.
func (r CompiledResolve) Relation() string { return r.spec.Relation }

// From is the input relation this resolve consumes.
func (r CompiledResolve) From() string { return r.spec.From }

// CompileResolveSpecs validates already-decoded specs (the pipeline path).
func CompileResolveSpecs(specs []ResolveSpec) ([]CompiledResolve, error) {
	out := make([]CompiledResolve, 0, len(specs))
	for _, s := range specs {
		if s.Relation == "" {
			return nil, fmt.Errorf("resolve: missing relation")
		}
		if s.From == "" {
			return nil, fmt.Errorf("resolve %q: missing from", s.Relation)
		}
		if s.From == s.Relation {
			return nil, fmt.Errorf("resolve %q: from and relation must differ", s.Relation)
		}
		out = append(out, CompiledResolve{spec: s})
	}
	return out, nil
}

func (r CompiledResolve) exts() []string {
	if len(r.spec.Extensions) == 0 {
		return []string{""}
	}
	return r.spec.Extensions
}

func (r CompiledResolve) stripExtSet() map[string]bool {
	m := make(map[string]bool, len(r.spec.StripExt))
	for _, e := range r.spec.StripExt {
		m[e] = true
	}
	return m
}

// anchors is the ordered list of base directories to resolve a spec against.
func (r CompiledResolve) anchors(fromFile string) []string {
	var a []string
	if r.spec.OwnDirFirst {
		a = append(a, path.Dir(fromFile))
	}
	a = append(a, r.spec.Roots...)
	if len(a) == 0 {
		a = append(a, path.Dir(fromFile))
	}
	return a
}

// candidates lists the repo-relative paths spec could name, most-preferred
// first, deduped. A candidate that escapes the repo root (a `../` that walks
// past it) or does not name a file is dropped.
func (r CompiledResolve) candidates(fromFile, spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = path.Clean(p)
		if p == "." || p == "/" || strings.HasPrefix(p, "../") {
			return
		}
		p = strings.TrimPrefix(p, "/")
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	for _, anchor := range r.anchors(fromFile) {
		stem := path.Join(anchor, spec)
		dir, base := path.Split(stem)
		dir = strings.TrimSuffix(dir, "/")

		// the spec already carries an extension (`jquery.min.js`, `_mixins.scss`)
		specExt := path.Ext(base)
		if specExt != "" {
			add(stem)
		}
		if specExt != "" && r.stripExtSet()[specExt] {
			stemNoExt := stem[:len(stem)-len(specExt)]
			for _, ext := range r.exts() {
				add(stemNoExt + ext)
			}
		}
		for _, ext := range r.exts() {
			add(stem + ext)
			if r.spec.PartialPrefix != "" {
				add(path.Join(dir, r.spec.PartialPrefix+base) + ext)
			}
		}
		for _, idx := range r.spec.IndexFiles {
			for _, ext := range r.exts() {
				add(path.Join(stem, idx) + ext)
				if r.spec.PartialPrefix != "" {
					add(path.Join(stem, r.spec.PartialPrefix+idx) + ext)
				}
			}
		}
	}
	return out
}

// resolve returns the first candidate present in rank, with its file rank.
func (r CompiledResolve) resolve(fromFile, spec string, rank map[string]int) (target string, rk int, ok bool) {
	for _, c := range r.candidates(fromFile, spec) {
		if n, hit := rank[c]; hit {
			return c, n, true
		}
	}
	return "", 0, false
}

// dirCandidates lists the directories spec could name, most-preferred first
// (own directory before roots, root order otherwise) — the directory-fanout
// sibling of candidates(), with no extension trial since a directory has none.
func (r CompiledResolve) dirCandidates(fromFile, spec string) []string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, anchor := range r.anchors(fromFile) {
		p := path.Clean(path.Join(anchor, spec))
		if p == "/" || strings.HasPrefix(p, "../") {
			continue
		}
		p = strings.TrimPrefix(p, "/")
		if p == "." {
			p = ""
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// resolveDir walks dirCandidates in order and returns every file the first
// matching directory contains (respecting Recursive and an optional ext
// filter), mirroring Sprockets' "first load path holding the directory wins"
// rule — the same precedence resolve() applies to a single file, just tested
// against a directory's contents instead of one exact name.
func (r CompiledResolve) resolveDir(fromFile, spec, ext string, files []string) ([]string, bool) {
	for _, c := range r.dirCandidates(fromFile, spec) {
		var out []string
		for _, f := range files {
			if f == "" || f == fromFile {
				continue
			}
			if !underResolvedDir(f, c, r.spec.Recursive) {
				continue
			}
			if ext != "" && !strings.EqualFold(path.Ext(f), ext) {
				continue
			}
			out = append(out, f)
		}
		if len(out) > 0 {
			sort.Strings(out)
			return out, true
		}
	}
	return nil, false
}

// underResolvedDir reports whether file sits under dir — immediately (dir ==
// path.Dir(file)) or, when recursive, anywhere beneath it. dir == "" is the
// repo root.
func underResolvedDir(file, dir string, recursive bool) bool {
	if recursive {
		if dir == "" {
			return true
		}
		return strings.HasPrefix(file, dir+"/")
	}
	if dir == "" {
		return path.Dir(file) == "."
	}
	return path.Dir(file) == dir
}

// ApplyResolves runs every `resolve:` block against the facts already in fs
// (its input relation, produced by extract or the bridge) and the service file
// list, adding one Target fact per resolved reference. It is a no-op when the
// framework has no resolve block, so it is inert for every framework until one
// opts in. files is Snapshot.Files — the same set the bridge's service_file
// relation carries.
func ApplyResolves(rs []CompiledResolve, files []string, fs FactSet) {
	if len(rs) == 0 {
		return
	}
	rank := fileRank(files)
	existing := fs.All()
	for _, r := range rs {
		for _, f := range existing {
			if f.Pred != r.From() || len(f.Args) < 3 {
				continue
			}
			fromFile, spec := f.Args[0].Str, f.Args[2].Str
			origin := Origin{Kind: OriginPrimitive, File: fromFile, Line: int(f.Args[1].Int), Pattern: r.Relation()}

			if r.spec.Dir {
				ext := ""
				if len(f.Args) > 3 {
					ext = f.Args[3].Str
				}
				targets, ok := r.resolveDir(fromFile, spec, ext, files)
				if !ok {
					continue
				}
				for _, t := range targets {
					fs.Add(Fact{
						Pred:   r.Relation(),
						Args:   []Atom{f.Args[0], f.Args[1], Str(spec), Str(t), Int(int64(rank[t]))},
						Origin: origin,
					})
				}
				continue
			}

			target, rk, ok := r.resolve(fromFile, spec, rank)
			if !ok {
				continue
			}
			fs.Add(Fact{
				Pred:   r.Relation(),
				Args:   []Atom{f.Args[0], f.Args[1], Str(spec), Str(target), Int(int64(rk))},
				Origin: origin,
			})
		}
	}
}

// fileRank maps each distinct path to its 0-based lexical rank — the same dense
// ordering the FX.1 bridge's file_rank relation assigns node files.
func fileRank(files []string) map[string]int {
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	rank := make(map[string]int, len(sorted))
	n := 0
	for _, f := range sorted {
		if f == "" {
			continue
		}
		if _, dup := rank[f]; dup {
			continue
		}
		rank[f] = n
		n++
	}
	return rank
}
