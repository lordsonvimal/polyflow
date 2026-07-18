package patterns

// StripStringLiteral is the exported form of stripStringLiteral.
// It removes surrounding string delimiters from a captured source value.
// Exported so callers (linkers, HTTP enrichers) can apply the same
// normalisation that MatchToGraph uses for meta fields.
func StripStringLiteral(s string) string { return stripStringLiteral(s) }
