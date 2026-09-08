package linker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// schemaURLEntry is one declared endpoint: the path as written and as matched.
type schemaURLEntry struct {
	Raw  string // "/api/standards/${standard_id}/forms/${id}"
	Path string // "/api/standards/*/forms/*"  — query dropped, params wildcarded
	Key  string // the container key it was declared under ("create_url", "endpoints.list")
}

// SchemaURLTable answers "given this entity and this key, what path?" for one
// service, from a checked-in data asset.
//
// It is a lookup table, NOT a producer of nodes: minting clients from it would
// make a data file the caller of every endpoint it names and would break trace
// and impact. See the Core model section of
// docs/schema-driven-url-resolution-plan.md.
//
// Nothing here knows a key vocabulary or a container shape. Entities and keys
// are whatever the discovered asset called them; the asset is the configuration.
type SchemaURLTable struct {
	File     string                               // the asset, relative to cwd
	ByEntity map[string]map[string]schemaURLEntry // entity → key → entry
	Aliases  map[string]string                    // self-identifying name → entity
}

// Entities returns every entity name and alias, sorted. This is the vocabulary
// MS.1 matches code literals against — the asset configures the matcher, so no
// identifier or container path is hardcoded on the consumer side.
func (t *SchemaURLTable) Entities() []string {
	seen := map[string]bool{}
	for e := range t.ByEntity {
		seen[e] = true
	}
	for a := range t.Aliases {
		seen[a] = true
	}
	out := make([]string, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

// Lookup resolves one (entity, key) pair. Fallbacks between keys are NOT encoded
// here: which key stands in for which is a property of the application's accessor
// functions, and MS.2 learns it by reading them.
func (t *SchemaURLTable) Lookup(entity, key string) (schemaURLEntry, bool) {
	if canon, ok := t.Aliases[entity]; ok {
		entity = canon
	}
	if keys, ok := t.ByEntity[entity]; ok {
		if e, ok := keys[key]; ok {
			return e, true
		}
	}
	return schemaURLEntry{}, false
}

// ── discovery ────────────────────────────────────────────────────────────────

var (
	schemaPlaceholderDollar = regexp.MustCompile(`\$\{[^}]*\}`)
	schemaPlaceholderColon  = regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`)
	schemaPlaceholderBrace  = regexp.MustCompile(`\{[^}]*\}`)
	schemaPlaceholderAngle  = regexp.MustCompile(`<[^>]*>`)
	schemaSlashRun          = regexp.MustCompile(`/{2,}`)
	schemaBareToken         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
)

// NormalizeSchemaPath applies MS.0b: drop query/fragment, wildcard every
// placeholder spelling, collapse repeated slashes, strip a trailing slash. It
// returns ok=false for a leaf that does not begin with "/" after normalisation.
func NormalizeSchemaPath(raw string) (string, bool) {
	s := raw
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = schemaPlaceholderDollar.ReplaceAllString(s, "*")
	s = schemaPlaceholderColon.ReplaceAllString(s, "*")
	s = schemaPlaceholderBrace.ReplaceAllString(s, "*")
	s = schemaPlaceholderAngle.ReplaceAllString(s, "*")
	s = schemaSlashRun.ReplaceAllString(s, "/")
	if len(s) > 1 {
		s = strings.TrimSuffix(s, "/")
	}
	if !strings.HasPrefix(s, "/") {
		return "", false
	}
	return s, true
}

// schemaSkipDir reports whether a slash-path lives under a test or fixture
// directory (MS.0c rule 1) — cedar carries a fixture copy of its own asset that
// must never be indexed as the real thing. Reuses crIsTestFile and adds the
// data-fixture dir conventions walkAllFiles does not filter.
func schemaSkipDir(slash string) bool {
	if crIsTestFile(slash) {
		return true
	}
	for _, s := range []string{"/testdata/", "/fixtures/", "/__fixtures__/", "/test/fixtures/"} {
		if strings.Contains(slash, s) {
			return true
		}
	}
	return false
}

// schemaLeaf is one string leaf of a data file with its container chain (the
// chain's last element is the leaf's own key).
type schemaLeaf struct {
	chain []string
	raw   string
	norm  string // "" when the raw string is not a candidate path
}

// flattenSchema walks an arbitrary JSON/YAML tree recording every string leaf
// with its container chain. Objects, arrays and nested combinations flatten the
// same way; an array index contributes its number as a chain element.
func flattenSchema(node interface{}, chain []string, out *[]schemaLeaf) {
	switch v := node.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			flattenSchema(v[k], append(chain, k), out)
		}
	case map[interface{}]interface{}: // yaml.v3 non-string keys
		keys := make([]string, 0, len(v))
		m := make(map[string]interface{}, len(v))
		for k, val := range v {
			ks := fmt.Sprint(k)
			keys = append(keys, ks)
			m[ks] = val
		}
		sort.Strings(keys)
		for _, k := range keys {
			flattenSchema(m[k], append(chain, k), out)
		}
	case []interface{}:
		for i, item := range v {
			flattenSchema(item, append(chain, strconv.Itoa(i)), out)
		}
	case string:
		norm, _ := NormalizeSchemaPath(v)
		cp := make([]string, len(chain))
		copy(cp, chain)
		*out = append(*out, schemaLeaf{chain: cp, raw: v, norm: norm})
	}
}

// parseSchemaFile decodes a .json/.yaml/.yml file into a generic tree.
func parseSchemaFile(path string, data []byte) (interface{}, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	var root interface{}
	if ext == ".json" {
		if err := json.Unmarshal(data, &root); err != nil {
			return nil, false
		}
		return root, true
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, false
	}
	return root, true
}

// isOpenAPIShape reports whether the top level names an OpenAPI/Swagger document
// (MS.0e) — a server contract, already the contract engine's job.
func isOpenAPIShape(root interface{}) bool {
	m, ok := root.(map[string]interface{})
	if !ok {
		return false
	}
	_, a := m["openapi"]
	_, b := m["swagger"]
	return a || b
}

// schemaGateResult is the outcome of evaluating the MS.0c corroboration gate.
type schemaGateResult struct {
	distinct int
	matched  int
	ratio    float64
	pass     bool
}

func evalSchemaGate(distinctPaths map[string]bool, handlers map[string]bool, cfg workspace.SchemaConfig) schemaGateResult {
	matched := 0
	for p := range distinctPaths {
		if handlers[p] {
			matched++
		}
	}
	r := schemaGateResult{distinct: len(distinctPaths), matched: matched}
	if r.distinct > 0 {
		r.ratio = float64(matched) / float64(r.distinct)
	}
	r.pass = matched >= cfg.MinCorroboratedPaths && r.ratio >= cfg.MinCorroboratedRatio
	return r
}

// chooseEntityDepth applies MS.0d: pick the shallowest depth whose corroborated
// leaves are all nested below it (coverage 1.0), that has at least two distinct
// containers (entities are plural), and whose discrimination exceeds the
// configured minimum. Returns 0 when no depth qualifies.
func chooseEntityDepth(leaves []schemaLeaf, corroborated map[int]bool, minDisc float64) (depth int, coverage, disc float64) {
	maxLen := 0
	for _, l := range leaves {
		if len(l.chain) > maxLen {
			maxLen = len(l.chain)
		}
	}
	var corrIdx []int
	for i := range leaves {
		if corroborated[i] {
			corrIdx = append(corrIdx, i)
		}
	}
	if len(corrIdx) == 0 {
		return 0, 0, 0
	}
	for d := 1; d < maxLen; d++ {
		containers := map[string]bool{}
		for _, l := range leaves {
			if len(l.chain) > d {
				containers[strings.Join(l.chain[:d], "\x00")] = true
			}
		}
		if len(containers) < 2 {
			continue
		}
		covered := 0
		owners := map[string]bool{}
		for _, i := range corrIdx {
			if len(leaves[i].chain) > d {
				covered++
				owners[strings.Join(leaves[i].chain[:d], "\x00")] = true
			}
		}
		cov := float64(covered) / float64(len(corrIdx))
		dsc := float64(len(owners)) / float64(len(containers))
		if cov == 1.0 && dsc > minDisc {
			return d, cov, dsc
		}
	}
	return 0, 0, 0
}

// LoadSchemaURLTables discovers endpoint-declaring data assets in each service's
// files and returns one table per service, plus ledger rows for dead entries,
// skipped shapes, and one schema_asset_loaded row per discovered asset recording
// the thresholds that admitted it.
//
// Discovery is by ROUTE CORROBORATION: a .json/.yaml/.yml file qualifies when
// enough of its distinct normalised string leaves match a declared handler path
// in the same service. Deliberately no filename rule and no key-name rule.
//
// cfg may be the zero value; every threshold has a tested default. Assets
// admitted only because a threshold was loosened from its default are ledgered
// schema_asset_loaded_tuned.
func LoadSchemaURLTables(
	serviceFiles map[string][]string,
	handlerPaths map[string]map[string]bool,
	cfg workspace.SchemaConfig,
) (map[string]*SchemaURLTable, []graph.UnresolvedRef) {
	tables := map[string]*SchemaURLTable{}
	var ledger []graph.UnresolvedRef
	if cfg.Disable {
		return tables, ledger
	}
	eff, _ := cfg.Effective()
	def, _ := workspace.SchemaConfig{}.Effective()
	cwd, _ := os.Getwd()

	svcNames := make([]string, 0, len(serviceFiles))
	for s := range serviceFiles {
		svcNames = append(svcNames, s)
	}
	sort.Strings(svcNames)

	for _, svc := range svcNames {
		files := append([]string(nil), serviceFiles[svc]...)
		sort.Strings(files)
		handlers := handlerPaths[svc]

		for _, abs := range files {
			ext := strings.ToLower(filepath.Ext(abs))
			if ext != ".json" && ext != ".yaml" && ext != ".yml" {
				continue
			}
			rel := abs
			if r, err := filepath.Rel(cwd, abs); err == nil {
				rel = r
			}
			if schemaSkipDir(filepath.ToSlash(abs)) {
				continue
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				continue
			}
			root, ok := parseSchemaFile(abs, data)
			if !ok {
				continue
			}

			declared := schemaFileDeclared(abs, cfg.Assets)

			if isOpenAPIShape(root) {
				if declared {
					ledger = append(ledger, graph.UnresolvedRef{
						Service: svc, File: rel, Kind: "schema_asset_skipped",
						Name: rel, Targets: "reason=openapi_or_swagger_document",
					})
				}
				continue
			}

			var leaves []schemaLeaf
			flattenSchema(root, nil, &leaves)

			distinct := map[string]bool{}
			for _, l := range leaves {
				if l.norm != "" {
					distinct[l.norm] = true
				}
			}
			if len(distinct) == 0 {
				continue
			}

			gate := evalSchemaGate(distinct, handlers, eff)
			gateDef := evalSchemaGate(distinct, handlers, def)
			if !gate.pass && !declared {
				continue
			}

			corroborated := map[int]bool{}
			for i, l := range leaves {
				if l.norm != "" && handlers[l.norm] {
					corroborated[i] = true
				}
			}

			depth, cov, disc := chooseEntityDepth(leaves, corroborated, eff.MinEntityDiscrimination)
			if depth == 0 {
				ledger = append(ledger, graph.UnresolvedRef{
					Service: svc, File: rel, Kind: "schema_asset_skipped",
					Name: rel, Targets: "reason=no_entity_level",
				})
				continue
			}

			tbl := &SchemaURLTable{
				File:     rel,
				ByEntity: map[string]map[string]schemaURLEntry{},
				Aliases:  map[string]string{},
			}
			deadSeen := map[string]bool{}
			for i, l := range leaves {
				if l.norm == "" {
					continue
				}
				if len(l.chain) <= depth {
					continue
				}
				entity := l.chain[depth-1]
				key := strings.Join(l.chain[depth:], ".")
				if handlers[l.norm] {
					if tbl.ByEntity[entity] == nil {
						tbl.ByEntity[entity] = map[string]schemaURLEntry{}
					}
					if _, exists := tbl.ByEntity[entity][key]; !exists {
						tbl.ByEntity[entity][key] = schemaURLEntry{Raw: l.raw, Path: l.norm, Key: key}
					}
					continue
				}
				// MS.0g: a candidate path in a qualifying asset that matches no
				// handler — a finding (stale config), not a missing route parser.
				if !deadSeen[l.norm] {
					deadSeen[l.norm] = true
					ledger = append(ledger, graph.UnresolvedRef{
						Service: svc, File: rel, Kind: "schema_route_dead",
						Name: l.raw, Targets: fmt.Sprintf("entity=%s key=%s", entity, key),
					})
				}
				_ = i
			}

			schemaCollectAliases(leaves, depth, tbl)

			if len(tbl.ByEntity) == 0 {
				continue
			}
			tables[svc] = tbl

			kind := "schema_asset_loaded"
			if gate.pass && !gateDef.pass {
				kind = "schema_asset_loaded_tuned"
			}
			ledger = append(ledger, graph.UnresolvedRef{
				Service: svc, File: rel, Kind: kind, Name: rel,
				Targets: fmt.Sprintf(
					"min_corroborated_paths=%d min_corroborated_ratio=%.3g min_entity_discrimination=%.3g "+
						"matched=%d distinct=%d ratio=%.3g entity_depth=%d coverage=%.3g discrimination=%.3g declared=%t",
					eff.MinCorroboratedPaths, eff.MinCorroboratedRatio, eff.MinEntityDiscrimination,
					gate.matched, gate.distinct, gate.ratio, depth, cov, disc, declared,
				),
			})
		}
	}

	// Deterministic ledger order.
	sort.SliceStable(ledger, func(i, j int) bool {
		a, b := ledger[i], ledger[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return tables, ledger
}

// schemaFileDeclared reports whether abs matches one of the cfg Assets globs,
// evaluated relative to the owning service's root.
func schemaFileDeclared(abs string, globs []string) bool {
	if len(globs) == 0 {
		return false
	}
	// Match the glob against the absolute path; a bare glob is also matched
	// against the path tail via a "**/" prefix, so "config/x.json" works.
	slashAbs := filepath.ToSlash(abs)
	for _, g := range globs {
		g = filepath.ToSlash(g)
		if ok, _ := doublestar.Match(g, slashAbs); ok {
			return true
		}
		if ok, _ := doublestar.Match("**/"+strings.TrimPrefix(g, "**/"), slashAbs); ok {
			return true
		}
	}
	return false
}

// schemaCollectAliases records a self-identifying token per entity (MS.0d):
// a direct-child string leaf that is not a path and is a bare identifier. The
// container key is preferred on disagreement, so an alias never overwrites an
// entity and never shadows one.
func schemaCollectAliases(leaves []schemaLeaf, depth int, tbl *SchemaURLTable) {
	perEntity := map[string]map[string]bool{}
	for _, l := range leaves {
		if l.norm != "" || len(l.chain) != depth+1 {
			continue
		}
		if !schemaBareToken.MatchString(l.raw) {
			continue
		}
		entity := l.chain[depth-1]
		if _, isEntity := tbl.ByEntity[entity]; !isEntity {
			continue
		}
		if perEntity[entity] == nil {
			perEntity[entity] = map[string]bool{}
		}
		perEntity[entity][l.raw] = true
	}
	for entity, vals := range perEntity {
		if len(vals) != 1 {
			continue
		}
		for v := range vals {
			if v == entity {
				continue
			}
			if _, clash := tbl.ByEntity[v]; clash {
				continue
			}
			if _, dup := tbl.Aliases[v]; dup {
				continue
			}
			tbl.Aliases[v] = entity
		}
	}
}
