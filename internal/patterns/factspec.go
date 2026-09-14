package patterns

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// FactSpec is one `facts:` entry in a pattern YAML — a predicate name plus an
// ordered list of argument bindings. Each match of the pattern produces one
// fact per predicate (or N facts, when an arg binding fans out; see ArgList).
//
// This is the FX.2 replacement for the flat `extract: {node_type, edge_type,
// attributes}` block: instead of building a graph node/edge directly, a
// pattern asserts structured facts that a `<framework>.dl` rule derives over
// and an `emit:` spec (FX.5) turns back into edges.
type FactSpec struct {
	Pred string  `yaml:"pred"`
	Args ArgList `yaml:"args"`
}

// ArgList preserves the YAML mapping order of a `facts[].args` block: datalog
// relations are positional, so `klass:` before `callback:` in the YAML means
// arg 0 vs arg 1 in the tuple. A plain map[string]ArgSpec would lose that.
type ArgList []ArgSpec

// ArgSpec binds one tuple position. Exactly one of Literal / Capture (with an
// optional Extract verb) selects the source; Field descends into a named child
// of the captured node before the verb runs; Then chains a second verb over
// the first verb's result(s) (`keyword_arg(only)` then `list_elements`).
type ArgSpec struct {
	Name string `yaml:"-"`

	Capture string  `yaml:"capture"` // capture name; empty ⇒ the pattern's anchor node
	Field   string  `yaml:"field"`   // optional child field to descend into first
	Literal *string `yaml:"literal"` // a constant value (mutually exclusive with Capture/Extract)
	Extract string  `yaml:"extract"` // verb spec, e.g. "enclosing_name(class)" (default: "text")
	Then    string  `yaml:"then"`    // optional chained verb

	// PathTransform is path_transform's config (FX.8.P5). It rides alongside
	// Extract == "path_transform" instead of being packed into a string arg —
	// its match/replace/action rules are structured, not a single value a
	// generic "(arg)" parser can split.
	PathTransform *PathTransformSpec `yaml:"path_transform"`
}

// UnmarshalYAML reads the args mapping in document order.
func (a *ArgList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("facts[].args must be a mapping, got kind %d", value.Kind)
	}
	out := make(ArgList, 0, len(value.Content)/2)
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i]
		val := value.Content[i+1]
		var spec ArgSpec
		if err := val.Decode(&spec); err != nil {
			return fmt.Errorf("facts[].args[%s]: %w", key.Value, err)
		}
		spec.Name = key.Value
		out = append(out, spec)
	}
	*a = out
	return nil
}

// Validate checks a fact spec is well-formed at load time.
func (f FactSpec) Validate() error {
	if f.Pred == "" {
		return fmt.Errorf("facts entry has no pred")
	}
	if len(f.Args) == 0 {
		return fmt.Errorf("fact %q has no args", f.Pred)
	}
	for _, a := range f.Args {
		if a.Literal != nil && (a.Capture != "" || a.Extract != "" || a.Then != "") {
			return fmt.Errorf("fact %q arg %q: literal is exclusive with capture/extract/then", f.Pred, a.Name)
		}
		if (a.Extract == "path_transform") != (a.PathTransform != nil) {
			return fmt.Errorf("fact %q arg %q: extract: path_transform requires a path_transform: block, and vice versa", f.Pred, a.Name)
		}
	}
	return nil
}
