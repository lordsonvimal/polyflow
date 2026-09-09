package artifact

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	artifactdata "github.com/lordsonvimal/polyflow/artifacts"
)

// Mapping is one artifacts/<kind>.yaml file: which formats it applies to, the
// gate that admits an artifact, and how a qualifying artifact's leaves become
// facts.
type Mapping struct {
	Kind    string   `yaml:"kind"`
	Formats []string `yaml:"formats"`
	Gate    struct {
		Against                 string   `yaml:"against"`
		Normalizers             []string `yaml:"normalizers"`
		MinMatches              int      `yaml:"min_matches"`
		MinRatio                float64  `yaml:"min_ratio"`
		MinEntityDiscrimination float64  `yaml:"min_entity_discrimination"`
	} `yaml:"gate"`
	Emit struct {
		Fact   string `yaml:"fact"`
		Entity string `yaml:"entity"`
		Key    string `yaml:"key"`
		Value  string `yaml:"value"`
	} `yaml:"emit"`

	// Source is the embedded file this mapping was read from, e.g.
	// "endpoint_table.yaml" — used verbatim in provenance Rule strings.
	Source string `yaml:"-"`
}

// GateSpec projects the mapping's gate section onto a Gate.
func (m Mapping) GateSpec() Gate {
	return Gate{
		Against:           m.Gate.Against,
		Normalizers:       m.Gate.Normalizers,
		MinMatches:        m.Gate.MinMatches,
		MinRatio:          m.Gate.MinRatio,
		MinDiscrimination: m.Gate.MinEntityDiscrimination,
	}
}

// Rule returns the SA.1 provenance Rule for a fact this mapping emitted from
// artifactFile: "artifacts/<kind>#<artifact file>".
func (m Mapping) Rule(artifactFile string) string {
	return "artifacts/" + m.Kind + "#" + artifactFile
}

var (
	mappingsOnce sync.Once
	mappingsAll  []Mapping
	mappingsErr  error
)

// Mappings returns every built-in mapping, sorted by kind. Parsed once.
func Mappings() ([]Mapping, error) {
	mappingsOnce.Do(func() { mappingsAll, mappingsErr = loadMappings() })
	return mappingsAll, mappingsErr
}

// MappingFor returns the mapping for one fact kind.
func MappingFor(kind string) (Mapping, bool) {
	all, err := Mappings()
	if err != nil {
		return Mapping{}, false
	}
	for _, m := range all {
		if m.Kind == kind {
			return m, true
		}
	}
	return Mapping{}, false
}

func loadMappings() ([]Mapping, error) {
	entries, err := fs.ReadDir(artifactdata.FS, ".")
	if err != nil {
		return nil, err
	}
	var out []Mapping
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		raw, err := fs.ReadFile(artifactdata.FS, e.Name())
		if err != nil {
			return nil, err
		}
		var m Mapping
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("artifacts/%s: %w", e.Name(), err)
		}
		m.Source = e.Name()
		if err := m.validate(); err != nil {
			return nil, fmt.Errorf("artifacts/%s: %w", e.Name(), err)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

func (m Mapping) validate() error {
	if m.Kind == "" {
		return fmt.Errorf("missing kind")
	}
	if m.Gate.Against == "" {
		return fmt.Errorf("missing gate.against")
	}
	if resolveChain(m.Gate.Normalizers).bad != "" {
		return fmt.Errorf("unknown normalizer %q", resolveChain(m.Gate.Normalizers).bad)
	}
	if m.Emit.Fact == "" {
		return fmt.Errorf("missing emit.fact")
	}
	return nil
}
