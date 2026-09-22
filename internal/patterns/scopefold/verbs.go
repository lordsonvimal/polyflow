package scopefold

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/railsinflect"
)

// applyVerb runs a "|"-chained verb pipeline (e.g. "segment|upcase") against
// raw captured text. This is a small, fixed, framework-agnostic registry —
// every grammar shares it; nothing here is Rails-specific despite reusing
// railsinflect (general English inflection, not a Rails DSL concern — the
// same leaf package internal/patterns/extract.go's own generic `inflect`
// verb already reuses for the identical reason).
func applyVerb(spec, raw string) string {
	val := raw
	for _, step := range strings.Split(spec, "|") {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		val = applyOneVerb(step, val)
	}
	return val
}

func applyOneVerb(step, val string) string {
	name, arg, _ := strings.Cut(step, ":")
	switch name {
	case "", "text":
		return val
	case "segment":
		return segment(val)
	case "upcase":
		return strings.ToUpper(val)
	case "suffix_id":
		return val + "_id"
	case "colon_prefix":
		if val == "" {
			return ""
		}
		return ":" + val
	case "before_last_slash":
		// The module-prefix half of a namespaced override value ("users/
		// sessions" -> "users") — a generic split, not Rails-specific;
		// Rails' `controller:`/`to:`/devise `controllers:` overrides all use
		// this same "namespace/basename" convention.
		if i := strings.LastIndex(val, "/"); i >= 0 {
			return val[:i]
		}
		return ""
	case "after_last_slash":
		if i := strings.LastIndex(val, "/"); i >= 0 {
			return val[i+1:]
		}
		return val
	case "before_hash":
		if i := strings.Index(val, "#"); i >= 0 {
			return val[:i]
		}
		return ""
	case "after_hash":
		if i := strings.Index(val, "#"); i >= 0 {
			return val[i+1:]
		}
		return ""
	case "path_helper_name":
		// Rails' auto-name for a string-literal route: strip quotes and
		// slashes, then underscore-join whatever segments remain — "" for
		// anything with a dynamic segment, since Rails generates no helper
		// for those (pathHelperName's rule, generalized off one capture
		// instead of a whole nameScope + literal pair).
		lit := segment(val)
		if lit == "" || strings.Contains(lit, ":") || strings.Contains(lit, "*") {
			return ""
		}
		return strings.ReplaceAll(lit, "/", "_")
	case "inflect":
		switch arg {
		case "singularize":
			return railsinflect.Singularize(val)
		case "pluralize":
			return railsinflect.Pluralize(val)
		case "collection_name":
			// ActionDispatch's own collection_name rule: a resource whose
			// singular and plural forms are the same word ("sso") suffixes
			// the *collection* name with "_index", since one name cannot
			// name both the collection and a member.
			if railsinflect.Singularize(val) == val {
				return val + "_index"
			}
			return val
		}
	}
	return val
}

// segment reduces a raw captured symbol/string to a bare path segment: a
// leading ":" (simple_symbol) or surrounding quotes (string) stripped, then
// leading/trailing "/" trimmed. Generalizes
// internal/parser/ruby_route_paths.go's literalSegment to be
// language-agnostic (Python/JS string quoting is the same shape).
func segment(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, ":") {
		raw = raw[1:]
	} else if len(raw) >= 2 {
		first, last := raw[0], raw[len(raw)-1]
		if (first == '"' || first == '\'' || first == '`') && first == last {
			raw = raw[1 : len(raw)-1]
		}
	}
	return strings.Trim(raw, "/")
}

// joinSegments composes a stack of segments into an absolute path — "/" for
// an empty stack, "/a/b/c" otherwise.
func joinSegments(segs []string) string {
	if len(segs) == 0 {
		return "/"
	}
	return "/" + strings.Join(segs, "/")
}
