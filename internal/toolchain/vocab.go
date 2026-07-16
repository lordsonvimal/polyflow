package toolchain

import "strings"

// DatastarVocab holds the attribute-key patterns that identify datastar event
// actions and reactive bindings for a specific datastar version. The templ
// parser consults this instead of hardcoded prefix checks, so adding a new
// syntax variant is a data change here, not a parser code change.
type DatastarVocab struct {
	// OnEventPrefixes lists key prefixes that identify event-action attributes.
	// v0: ["data-on-"]  (hyphen)
	// v1: ["data-on:"]  (colon)
	OnEventPrefixes []string
	// ReactiveAttrPrefixes lists key prefixes for reactive signal-read attributes.
	ReactiveAttrPrefixes []string
	// ReactiveAttrExact lists exact keys for reactive signal-read attributes.
	ReactiveAttrExact []string
}

// IsDataOnKey reports whether key is a datastar event-action attribute under
// this vocabulary.
func (v DatastarVocab) IsDataOnKey(key string) bool {
	for _, prefix := range v.OnEventPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// IsReactiveAttrKey reports whether key is a datastar reactive-binding attribute
// that reads signals to drive rendering.
func (v DatastarVocab) IsReactiveAttrKey(key string) bool {
	for _, exact := range v.ReactiveAttrExact {
		if key == exact {
			return true
		}
	}
	for _, prefix := range v.ReactiveAttrPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

var (
	// datastarV0Vocab covers datastar <1.0.0 (hyphen attribute syntax).
	datastarV0Vocab = DatastarVocab{
		OnEventPrefixes:      []string{"data-on-"},
		ReactiveAttrPrefixes: []string{"data-class", "data-attr-", "data-computed"},
		ReactiveAttrExact:    []string{"data-show", "data-when"},
	}

	// datastarV1Vocab covers datastar >=1.0.0 (colon attribute syntax).
	// Both data-attr: and data-attr- are kept because some v1 projects carry
	// residual hyphen-form attr bindings alongside the canonical colon form.
	datastarV1Vocab = DatastarVocab{
		OnEventPrefixes:      []string{"data-on:"},
		ReactiveAttrPrefixes: []string{"data-class", "data-attr:", "data-attr-", "data-computed"},
		ReactiveAttrExact:    []string{"data-show", "data-when"},
	}

	// datastarCombinedVocab is the backward-compatible fallback used when no
	// datastar version has been resolved for the project. Accepts both colon and
	// hyphen syntax so unversioned projects are not silently degraded.
	datastarCombinedVocab = DatastarVocab{
		OnEventPrefixes:      []string{"data-on:", "data-on-"},
		ReactiveAttrPrefixes: []string{"data-class", "data-attr:", "data-attr-", "data-computed"},
		ReactiveAttrExact:    []string{"data-show", "data-when"},
	}
)

// DefaultDatastarVocab returns the DatastarVocab for the given toolchain
// RuleVariant string (e.g. "datastar-v1"). Unknown or empty variants fall
// back to the combined vocab so unversioned projects continue to work.
func DefaultDatastarVocab(variant string) DatastarVocab {
	switch variant {
	case "datastar-v0":
		return datastarV0Vocab
	case "datastar-v1":
		return datastarV1Vocab
	default:
		return datastarCombinedVocab
	}
}
