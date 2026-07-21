package impact

import (
	"fmt"
	"strings"
)

// GitHubCommentMaxBytes is GitHub's PR comment character limit.
const GitHubCommentMaxBytes = 65536

// FormatGitHubComment renders r as a GitHub PR comment (Markdown), sized to
// fit within maxBytes. maxBytes <= 0 uses GitHubCommentMaxBytes.
// Always uses the file-grouped summary form; trims the files table when
// needed and appends a note, but the verification, unresolved, and unmapped
// sections are never trimmed.
func FormatGitHubComment(r *DiffResult, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = GitHubCommentMaxBytes
	}
	s := r.Summarize()
	return renderGitHubComment(s, maxBytes)
}

// renderGitHubComment renders a DiffSummary as a GitHub PR comment, trimming
// the blast-radius files table if the rendered markdown exceeds maxBytes.
// All non-files sections (verification, unresolved, unmapped) are always present.
func renderGitHubComment(s *DiffSummary, maxBytes int) string {
	// Build the fixed sections (never trimmed).
	var sb strings.Builder
	writeGitHubHeader(&sb, s)
	writeGitHubChangedNodes(&sb, s)
	writeGitHubCrossService(&sb, s)

	fixed := sb.String()
	footer := gitHubFooterSections(s)

	// Available bytes for the blast-radius table.
	tableAllowance := maxBytes - len(fixed) - len(footer)

	table, omitted := gitHubFilesTable(s.Files, tableAllowance)

	var out strings.Builder
	out.WriteString(fixed)
	out.WriteString(table)
	if omitted > 0 {
		fmt.Fprintf(&out, "\n> %d more file(s) omitted — blast radius is large; run `polyflow impact --diff` locally for full detail.\n", omitted)
	}
	out.WriteString(footer)
	return out.String()
}

func writeGitHubHeader(sb *strings.Builder, s *DiffSummary) {
	nodeWord := "nodes"
	if len(s.Targets) == 1 {
		nodeWord = "node"
	}
	fileWord := "files"
	if s.ChangedFiles == 1 {
		fileWord = "file"
	}
	blastWord := "files"
	if len(s.Files) == 1 {
		blastWord = "file"
	}
	fmt.Fprintf(sb, "## Polyflow Impact\n\n")
	fmt.Fprintf(sb, "> **%d changed %s · %d changed %s · %d %s in blast radius**\n\n",
		s.ChangedFiles, fileWord,
		len(s.Targets), nodeWord,
		len(s.Files), blastWord,
	)
}

func writeGitHubChangedNodes(sb *strings.Builder, s *DiffSummary) {
	if len(s.Targets) == 0 {
		return
	}
	sb.WriteString("### Changed nodes\n\n")
	for _, t := range s.Targets {
		fmt.Fprintf(sb, "- `%s`\n", t)
	}
	sb.WriteString("\n")
}

func writeGitHubCrossService(sb *strings.Builder, s *DiffSummary) {
	if len(s.CrossServiceTriggers) == 0 {
		return
	}
	sb.WriteString("### Cross-service triggers\n\n")
	for _, xs := range s.CrossServiceTriggers {
		fmt.Fprintf(sb, "- `%s`: %d http_call edge(s)\n", xs.FromService, xs.EdgeCount)
	}
	sb.WriteString("\n")
}

// gitHubFilesTable builds the blast-radius markdown table, trimming rows to
// stay within allowance bytes. Returns the table string and the number of
// omitted rows.
func gitHubFilesTable(files []FileRollup, allowance int) (string, int) {
	if len(files) == 0 {
		return "### Blast radius\n\n> No files in blast radius.\n\n", 0
	}

	header := "### Blast radius\n\n| Depth | File | Nodes | Via | Service |\n|-------|------|-------|-----|---------|\n"
	// Each row is roughly: "| 1 | `path/to/file.go` | 3 | calls | backend |\n"
	rows := make([]string, 0, len(files))
	for _, f := range files {
		row := fmt.Sprintf("| %d | `%s` | %d | %s | %s |\n",
			f.MinDepth, f.File, f.Nodes,
			strings.Join(f.EdgeTypes, ", "),
			f.Service,
		)
		rows = append(rows, row)
	}

	// Fit as many rows as possible within allowance.
	used := len(header)
	keep := 0
	for keep < len(rows) {
		next := len(rows[keep])
		if allowance > 0 && used+next > allowance {
			break
		}
		used += next
		keep++
	}
	if keep == 0 {
		keep = 1 // always keep at least one row
	}

	var sb strings.Builder
	sb.WriteString(header)
	for i := 0; i < keep; i++ {
		sb.WriteString(rows[i])
	}
	sb.WriteString("\n")
	return sb.String(), len(files) - keep
}

// gitHubFooterSections renders the verification, unresolved, and unmapped
// sections — always present, never trimmed.
func gitHubFooterSections(s *DiffSummary) string {
	var sb strings.Builder

	// Verification summary — always present.
	vs := s.VerificationSummary
	sb.WriteString("### Verification\n\n")
	total := vs.Verified + vs.Candidate + vs.ObservedOnlyGap + vs.Conflicting
	if total == 0 {
		sb.WriteString("> No verification data (edges are pre-fusion static-only).\n\n")
	} else {
		fmt.Fprintf(&sb, "%d verified · %d candidate · %d observed-only-gap · %d conflicting\n\n",
			vs.Verified, vs.Candidate, vs.ObservedOnlyGap, vs.Conflicting)
		if vs.Note != "" {
			fmt.Fprintf(&sb, "> %s\n\n", vs.Note)
		}
	}

	// Unresolved — always present.
	sb.WriteString("### Unresolved references\n\n")
	if len(s.Unresolved) == 0 {
		sb.WriteString("> None.\n\n")
	} else {
		if s.UnresolvedNote != "" {
			fmt.Fprintf(&sb, "> %s\n\n", s.UnresolvedNote)
		}
		sb.WriteString("<details><summary>Show unresolved references</summary>\n\n")
		for _, u := range s.Unresolved {
			fmt.Fprintf(&sb, "- `%s` (%s) — %s:%d\n", u.Name, u.Kind, u.File, u.Line)
		}
		sb.WriteString("\n</details>\n\n")
	}

	// Unmapped hunks — always present.
	sb.WriteString("### Unmapped hunks\n\n")
	if len(s.Unmapped) == 0 {
		sb.WriteString("> None — all changed spans are mapped to graph nodes.\n\n")
	} else {
		for _, u := range s.Unmapped {
			if u.Span != nil {
				fmt.Fprintf(&sb, "- `%s` lines %d–%d: %s\n", u.File, u.Span.Start, u.Span.End, u.Reason)
			} else {
				fmt.Fprintf(&sb, "- `%s`: %s\n", u.File, u.Reason)
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n*Generated by [Polyflow](https://github.com/lordsonvimal/polyflow)*\n")
	return sb.String()
}
