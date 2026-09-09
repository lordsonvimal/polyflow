package linker

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/lordsonvimal/polyflow/internal/artifact"
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

// ── path normalisation ───────────────────────────────────────────────────────

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
//
// This is exactly the `endpoint_table` mapping's normalizer chain
// (query_strip → param_wildcard → trim_slash → require_abs_path); it stays here
// as a named helper because callers outside the artifact gate use it directly
// (link_passes builds handlerPaths with it, schema_url_link resolves with it).
// The differential test asserts the two stay byte-identical on cedar.
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
// must never be indexed as the real thing.
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

// isOpenAPIArtifact reports whether the artifact's top level names an OpenAPI or
// Swagger document (MS.0e) — a server contract, already the contract engine's
// job. A top-level scalar `openapi:` / `swagger:` is the version field every
// such document carries.
func isOpenAPIArtifact(a *artifact.Artifact) bool {
	for _, l := range a.Leaves {
		if len(l.Path) == 1 && (l.Path[0] == "openapi" || l.Path[0] == "swagger") {
			return true
		}
	}
	return false
}

// LoadSchemaURLTables discovers endpoint-declaring data assets in each service's
// files and returns one table per service, plus ledger rows for dead entries,
// skipped shapes, and one schema_asset_loaded row per discovered asset recording
// the thresholds that admitted it.
//
// Discovery is by ROUTE CORROBORATION, delegated to the generic
// internal/artifact gate + the `endpoint_table` mapping: a file qualifies when
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

	mapping, ok := artifact.MappingFor("endpoint_table")
	if !ok {
		return tables, ledger
	}
	eff, _ := cfg.Effective()

	// The mapping carries the tested defaults; a workspace threshold overrides
	// them. `gate` admits under the effective thresholds; the default-threshold
	// comparison (for the _tuned ledger kind) reuses the same match counts.
	gate := mapping.GateSpec()
	gate.MinMatches = eff.MinCorroboratedPaths
	gate.MinRatio = eff.MinCorroboratedRatio
	gate.MinDiscrimination = eff.MinEntityDiscrimination

	cwd, _ := os.Getwd()

	svcNames := make([]string, 0, len(serviceFiles))
	for s := range serviceFiles {
		svcNames = append(svcNames, s)
	}
	sort.Strings(svcNames)

	// known: service → normalised handler path. handlerPaths already arrives
	// normalised by NormalizeSchemaPath (link_passes.go), which is the same
	// chain the gate applies, so it is a valid `known` map as-is.
	known := handlerPaths

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
			art, ok := artifact.ReadFile(abs, data)
			if !ok {
				continue
			}
			art.Service = svc

			declared := schemaFileDeclared(abs, cfg.Assets)

			if isOpenAPIArtifact(art) {
				if declared {
					ledger = append(ledger, graph.UnresolvedRef{
						Service: svc, File: rel, Kind: "schema_asset_skipped",
						Name: rel, Targets: "reason=openapi_or_swagger_document",
					})
				}
				continue
			}

			v := gate.Evaluate(art, known, declared)
			if v.Distinct == 0 {
				continue
			}
			if !v.GatePassed && !declared {
				continue
			}
			if v.EntityDepth == 0 {
				ledger = append(ledger, graph.UnresolvedRef{
					Service: svc, File: rel, Kind: "schema_asset_skipped",
					Name: rel, Targets: "reason=no_entity_level",
				})
				continue
			}
			depth := v.EntityDepth

			// Parallel normalised value per leaf, for entity/key extraction.
			norm := make([]string, len(art.Leaves))
			for i, l := range art.Leaves {
				if n, ok := gate.Norm(l.Value); ok {
					norm[i] = n
				}
			}

			tbl := &SchemaURLTable{
				File:     rel,
				ByEntity: map[string]map[string]schemaURLEntry{},
				Aliases:  map[string]string{},
			}
			deadSeen := map[string]bool{}
			for i, l := range art.Leaves {
				if norm[i] == "" || len(l.Path) <= depth {
					continue
				}
				entity := l.Path[depth-1]
				key := strings.Join(l.Path[depth:], ".")
				if handlers[norm[i]] {
					if tbl.ByEntity[entity] == nil {
						tbl.ByEntity[entity] = map[string]schemaURLEntry{}
					}
					if _, exists := tbl.ByEntity[entity][key]; !exists {
						tbl.ByEntity[entity][key] = schemaURLEntry{Raw: l.Value, Path: norm[i], Key: key}
					}
					continue
				}
				// MS.0g: a candidate path in a qualifying asset that matches no
				// handler — a finding (stale config), not a missing route parser.
				if !deadSeen[norm[i]] {
					deadSeen[norm[i]] = true
					ledger = append(ledger, graph.UnresolvedRef{
						Service: svc, File: rel, Kind: "schema_route_dead",
						Name: l.Value, Targets: fmt.Sprintf("entity=%s key=%s", entity, key),
					})
				}
			}

			schemaCollectAliases(art.Leaves, norm, depth, tbl)

			if len(tbl.ByEntity) == 0 {
				continue
			}
			tables[svc] = tbl

			// _tuned: admitted under the effective thresholds but not under the
			// tested defaults.
			gatePassDef := v.MatchCount >= artifact.DefaultMinMatches &&
				v.Ratio >= artifact.DefaultMinRatio
			kind := "schema_asset_loaded"
			if v.GatePassed && !gatePassDef {
				kind = "schema_asset_loaded_tuned"
			}
			ledger = append(ledger, graph.UnresolvedRef{
				Service: svc, File: rel, Kind: kind, Name: rel,
				Targets: fmt.Sprintf(
					"min_corroborated_paths=%d min_corroborated_ratio=%.3g min_entity_discrimination=%.3g "+
						"matched=%d distinct=%d ratio=%.3g entity_depth=%d coverage=%.3g discrimination=%.3g declared=%t",
					eff.MinCorroboratedPaths, eff.MinCorroboratedRatio, eff.MinEntityDiscrimination,
					v.MatchCount, v.Distinct, v.Ratio, depth, v.Coverage, v.Disc, declared,
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

// schemaFileDeclared reports whether abs matches one of the cfg Assets globs.
func schemaFileDeclared(abs string, globs []string) bool {
	if len(globs) == 0 {
		return false
	}
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
// entity and never shadows one. norm[i] is the normalised form of leaf i, ""
// when the leaf is not a path.
func schemaCollectAliases(leaves []artifact.Leaf, norm []string, depth int, tbl *SchemaURLTable) {
	perEntity := map[string]map[string]bool{}
	for i, l := range leaves {
		if norm[i] != "" || len(l.Path) != depth+1 {
			continue
		}
		if !schemaBareToken.MatchString(l.Value) {
			continue
		}
		entity := l.Path[depth-1]
		if _, isEntity := tbl.ByEntity[entity]; !isEntity {
			continue
		}
		if perEntity[entity] == nil {
			perEntity[entity] = map[string]bool{}
		}
		perEntity[entity][l.Value] = true
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
