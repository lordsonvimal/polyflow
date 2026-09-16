// Package schemaurl is Tier MS's endpoint-declaring data-asset resolver:
// given a checked-in JSON/YAML asset that names a service's real routes, it
// answers "given this entity and this key, what path?" for a JS/TS call site
// that reads the asset directly, or through a learnt accessor function.
//
// Moved out of internal/linker (Tier FX, schema_url_link + js_prop_clients
// migration) into its own package so internal/factpipe's hub providers can
// use it too, without creating an import cycle: internal/linker imports
// internal/patterns, and internal/patterns imports internal/factpipe, so
// internal/factpipe cannot import internal/linker directly. schemaurl (like
// internal/jsast) has no dependency on any of the three.
//
// internal/linker/js_local_url.go's ResolveJSLocalURLs is still a live
// caller of Resolver/ApplyURL (it was never part of this migration's
// scope — only the two consumers that built and shared a resolver purely
// for their own mint/patch work moved), so this package is a shared
// dependency of both internal/linker and internal/factpipe going forward,
// not an exclusive replacement for the old internal/linker code.
package schemaurl

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
	"github.com/lordsonvimal/polyflow/internal/jsast"
)

// Entry is one declared endpoint: the path as written and as matched.
type Entry struct {
	Raw  string // "/api/standards/${standard_id}/forms/${id}"
	Path string // "/api/standards/*/forms/*"  — query dropped, params wildcarded
	Key  string // the container key it was declared under ("create_url", "endpoints.list")
}

// Table answers "given this entity and this key, what path?" for one
// service, from a checked-in data asset.
//
// It is a lookup table, NOT a producer of nodes: minting clients from it
// would make a data file the caller of every endpoint it names and would
// break trace and impact. See the Core model section of
// docs/schema-driven-url-resolution-plan.md.
//
// Nothing here knows a key vocabulary or a container shape. Entities and
// keys are whatever the discovered asset called them; the asset is the
// configuration.
type Table struct {
	File     string                    // the asset, relative to cwd
	ByEntity map[string]map[string]Entry // entity → key → entry
	Aliases  map[string]string           // self-identifying name → entity
}

// Entities returns every entity name and alias, sorted. This is the
// vocabulary the resolver matches code literals against — the asset
// configures the matcher, so no identifier or container path is hardcoded on
// the consumer side.
func (t *Table) Entities() []string {
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

// Lookup resolves one (entity, key) pair. Fallbacks between keys are NOT
// encoded here: which key stands in for which is a property of the
// application's accessor functions, learnt by reading them (see
// resolver.go).
func (t *Table) Lookup(entity, key string) (Entry, bool) {
	if canon, ok := t.Aliases[entity]; ok {
		entity = canon
	}
	if keys, ok := t.ByEntity[entity]; ok {
		if e, ok := keys[key]; ok {
			return e, true
		}
	}
	return Entry{}, false
}

func (t *Table) hasEntity(name string) bool {
	if _, ok := t.ByEntity[name]; ok {
		return true
	}
	_, ok := t.Aliases[name]
	return ok
}

// ── path normalisation ───────────────────────────────────────────────────────

var (
	placeholderDollar = regexp.MustCompile(`\$\{[^}]*\}`)
	placeholderColon  = regexp.MustCompile(`:[A-Za-z_][A-Za-z0-9_]*`)
	placeholderBrace  = regexp.MustCompile(`\{[^}]*\}`)
	placeholderAngle  = regexp.MustCompile(`<[^>]*>`)
	slashRun          = regexp.MustCompile(`/{2,}`)
	bareToken         = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
)

// NormalizeSchemaPath applies MS.0b: drop query/fragment, wildcard every
// placeholder spelling, collapse repeated slashes, strip a trailing slash. It
// returns ok=false for a leaf that does not begin with "/" after
// normalisation.
//
// This is exactly the `endpoint_table` mapping's normalizer chain
// (query_strip → param_wildcard → trim_slash → require_abs_path). The
// differential test asserts the two stay byte-identical on cedar.
func NormalizeSchemaPath(raw string) (string, bool) {
	s := raw
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = placeholderDollar.ReplaceAllString(s, "*")
	s = placeholderColon.ReplaceAllString(s, "*")
	s = placeholderBrace.ReplaceAllString(s, "*")
	s = placeholderAngle.ReplaceAllString(s, "*")
	s = slashRun.ReplaceAllString(s, "/")
	if len(s) > 1 {
		s = strings.TrimSuffix(s, "/")
	}
	if !strings.HasPrefix(s, "/") {
		return "", false
	}
	return s, true
}

// skipDir reports whether a slash-path lives under a test or fixture
// directory (MS.0c rule 1) — a repo carries a fixture copy of its own asset
// that must never be indexed as the real thing.
func skipDir(slash string) bool {
	if jsast.IsTestFile(slash) {
		return true
	}
	for _, s := range []string{"/testdata/", "/fixtures/", "/__fixtures__/", "/test/fixtures/"} {
		if strings.Contains(slash, s) {
			return true
		}
	}
	return false
}

// isOpenAPIArtifact reports whether the artifact's top level names an OpenAPI
// or Swagger document (MS.0e) — a server contract, already the contract
// engine's job. A top-level scalar `openapi:`/`swagger:` is the version field
// every such document carries.
func isOpenAPIArtifact(a *artifact.Artifact) bool {
	for _, l := range a.Leaves {
		if len(l.Path) == 1 && (l.Path[0] == "openapi" || l.Path[0] == "swagger") {
			return true
		}
	}
	return false
}

// Config holds the corroboration-gate thresholds and declared-asset globs —
// graph.SchemaConfig verbatim (workspace.SchemaConfig's mirror; schemaurl
// cannot import internal/workspace's caller, internal/indexer, without
// forcing every schemaurl caller through indexer's own import graph, so
// callers pass the graph mirror type directly).
type Config = graph.SchemaConfig

// LoadTables discovers endpoint-declaring data assets in each service's
// files and returns one table per service, plus ledger rows for dead
// entries, skipped shapes, and one schema_asset_loaded row per discovered
// asset recording the thresholds that admitted it.
//
// Discovery is by ROUTE CORROBORATION, delegated to the generic
// internal/artifact gate + the `endpoint_table` mapping: a file qualifies
// when enough of its distinct normalised string leaves match a declared
// handler path in the same service. Deliberately no filename rule and no
// key-name rule.
//
// cfg may be the zero value; every threshold has a tested default. Assets
// admitted only because a threshold was loosened from its default are
// ledgered schema_asset_loaded_tuned.
func LoadTables(
	serviceFiles map[string][]string,
	handlerPaths map[string]map[string]bool,
	cfg Config,
) (map[string]*Table, []graph.UnresolvedRef) {
	tables := map[string]*Table{}
	var ledger []graph.UnresolvedRef
	if cfg.Disable {
		return tables, ledger
	}

	mapping, ok := artifact.MappingFor("endpoint_table")
	if !ok {
		return tables, ledger
	}
	minPaths, minRatio, minDisc := effectiveThresholds(cfg)

	gate := mapping.GateSpec()
	gate.MinMatches = minPaths
	gate.MinRatio = minRatio
	gate.MinDiscrimination = minDisc

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
			if skipDir(filepath.ToSlash(abs)) {
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

			declared := fileDeclared(abs, cfg.Assets)

			if isOpenAPIArtifact(art) {
				if declared {
					ledger = append(ledger, graph.UnresolvedRef{
						Service: svc, File: rel, Kind: "schema_asset_skipped",
						Name: rel, Targets: "reason=openapi_or_swagger_document",
					})
				}
				continue
			}

			v := gate.Evaluate(art, handlerPaths, declared)
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

			norm := make([]string, len(art.Leaves))
			for i, l := range art.Leaves {
				if n, ok := gate.Norm(l.Value); ok {
					norm[i] = n
				}
			}

			tbl := &Table{
				File:     rel,
				ByEntity: map[string]map[string]Entry{},
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
						tbl.ByEntity[entity] = map[string]Entry{}
					}
					if _, exists := tbl.ByEntity[entity][key]; !exists {
						tbl.ByEntity[entity][key] = Entry{Raw: l.Value, Path: norm[i], Key: key}
					}
					continue
				}
				// MS.0g: a candidate path in a qualifying asset that matches
				// no handler — a finding (stale config), not a missing route
				// parser.
				if !deadSeen[norm[i]] {
					deadSeen[norm[i]] = true
					ledger = append(ledger, graph.UnresolvedRef{
						Service: svc, File: rel, Kind: "schema_route_dead",
						Name: l.Value, Targets: fmt.Sprintf("entity=%s key=%s", entity, key),
					})
				}
			}

			collectAliases(art.Leaves, norm, depth, tbl)

			if len(tbl.ByEntity) == 0 {
				continue
			}
			tables[svc] = tbl

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
					minPaths, minRatio, minDisc,
					v.MatchCount, v.Distinct, v.Ratio, depth, v.Coverage, v.Disc, declared,
				),
			})
		}
	}

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

// Schema tier threshold defaults — mirrors workspace.SchemaConfig's tested
// defaults (workspace.DefaultMinCorroboratedPaths etc.), duplicated here as
// literal values (not imported) since schemaurl must not depend on
// internal/workspace's own callers. The two are kept in sync by the cedar
// differential test, the same discipline NormalizeSchemaPath's own doc
// already relies on.
const (
	defaultMinCorroboratedPaths    = 5
	defaultMinCorroboratedRatio    = 0.25
	defaultMinEntityDiscrimination = 0.5
)

func effectiveThresholds(cfg Config) (minPaths int, minRatio, minDisc float64) {
	minPaths, minRatio, minDisc = cfg.MinCorroboratedPaths, cfg.MinCorroboratedRatio, cfg.MinEntityDiscrimination
	if minPaths == 0 {
		minPaths = defaultMinCorroboratedPaths
	}
	if minRatio == 0 {
		minRatio = defaultMinCorroboratedRatio
	}
	if minDisc == 0 {
		minDisc = defaultMinEntityDiscrimination
	}
	return minPaths, minRatio, minDisc
}

// fileDeclared reports whether abs matches one of cfg's Assets globs.
func fileDeclared(abs string, globs []string) bool {
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

// collectAliases records a self-identifying token per entity (MS.0d): a
// direct-child string leaf that is not a path and is a bare identifier. The
// container key is preferred on disagreement, so an alias never overwrites
// an entity and never shadows one. norm[i] is the normalised form of leaf i,
// "" when the leaf is not a path.
func collectAliases(leaves []artifact.Leaf, norm []string, depth int, tbl *Table) {
	perEntity := map[string]map[string]bool{}
	for i, l := range leaves {
		if norm[i] != "" || len(l.Path) != depth+1 {
			continue
		}
		if !bareToken.MatchString(l.Value) {
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
