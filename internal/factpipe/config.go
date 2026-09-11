package factpipe

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/configsrc"
)

// config.go is FX.8.P3 — the config_value primitive (plan § FX.0's
// needs-primitive table). Resolving an environment-variable reference
// (`ENV["API_URL"]`, `os.Getenv("API_URL")`) to the value a service actually
// deploys with means reading its checked-in .env / k8s manifest / tfvars /
// shell-script sources — file *contents*, not filenames. That is real disk
// I/O keyed by directory, the one thing resolve_path deliberately never does
// (it only probes an in-memory candidate list), so config_value is its own
// primitive rather than a resolve_path mode.
//
// internal/configsrc already owns dotenv/k8s/terraform/shell parsing
// (extracted for exactly this reason — two independent parsers of the same
// files is how config_resolve and Tier CB's config_baseurl silently drifted
// apart before). This primitive is a thin FactSet adapter over
// configsrc.Load: a `config:` block in the framework YAML names an input
// relation of (File, Line, EnvVar) triples — produced by an ordinary
// `facts:` pattern matching the source-level env-var read — and config_value
// fans that out to (File, Line, EnvVar, Value, Ref) facts, one row per
// distinct checked-in value.
//
// It deliberately does not pick a winner when a variable resolves to more
// than one value across sources: which value counts as "the" value — first
// alphabetically, first by source order, or abstain on disagreement (Tier
// CB's rule, kept for the reason documented in internal/linker/
// config_baseurl.go: a wrong path is a fabricated route, not a hedge) — is
// derivation policy, not extraction. A `.dl` rule reads however many rows
// come out and decides; the primitive's only job is "what values exist and
// where did each come from".
//
// ConfigSpec is one entry of a framework YAML's `config:` list.
//
//	config:
//	  - relation: config_baseurl_value   # output relation (File, Line, EnvVar, Value, Ref)
//	    from: env_var_ref                # input relation; args 0,1,2 = File, Line, EnvVar
type ConfigSpec struct {
	Relation string `yaml:"relation"`
	From     string `yaml:"from"`
}

// CompiledConfig is a validated ConfigSpec.
type CompiledConfig struct{ spec ConfigSpec }

// Relation is the derived relation this config block produces.
func (c CompiledConfig) Relation() string { return c.spec.Relation }

// From is the input relation this config block consumes.
func (c CompiledConfig) From() string { return c.spec.From }

// CompileConfigSpecs validates already-decoded specs (the pipeline path).
func CompileConfigSpecs(specs []ConfigSpec) ([]CompiledConfig, error) {
	out := make([]CompiledConfig, 0, len(specs))
	for _, s := range specs {
		if s.Relation == "" {
			return nil, fmt.Errorf("config: missing relation")
		}
		if s.From == "" {
			return nil, fmt.Errorf("config %q: missing from", s.Relation)
		}
		if s.From == s.Relation {
			return nil, fmt.Errorf("config %q: from and relation must differ", s.Relation)
		}
		out = append(out, CompiledConfig{spec: s})
	}
	return out, nil
}

// ApplyConfig runs every `config:` block against the facts already in fs (its
// input relation, produced by extract) and svcPath, the service's root
// directory on disk. It is a no-op when the framework has no config block or
// svcPath is "", so it is inert for every framework and every corpus-only
// (no real filesystem) test until one opts in.
//
// configsrc.Load runs at most once per call regardless of how many config
// blocks (or how many matching input facts) a framework declares — it walks
// the service tree once and the result is immutable for the rest of Run.
func ApplyConfig(cs []CompiledConfig, svcPath string, fs FactSet) {
	if len(cs) == 0 || svcPath == "" {
		return
	}
	var vals map[string][]configsrc.Value
	loaded := false

	existing := fs.All()
	for _, c := range cs {
		for _, f := range existing {
			if f.Pred != c.From() || len(f.Args) < 3 {
				continue
			}
			if !loaded {
				vals = configsrc.Load(svcPath)
				loaded = true
			}
			file, line, envVar := f.Args[0].Str, int(f.Args[1].Int), f.Args[2].Str
			for _, v := range vals[envVar] {
				fs.Add(Fact{
					Pred: c.Relation(),
					Args: []Atom{Str(file), Int(int64(line)), Str(envVar), Str(v.Value), Str(v.Ref)},
					Origin: Origin{
						Kind:    OriginPrimitive,
						File:    file,
						Line:    line,
						Pattern: c.Relation(),
					},
				})
			}
		}
	}
}
