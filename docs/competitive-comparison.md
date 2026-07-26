# Polyflow — Competitive Comparison

Comparison of polyflow against four adjacent code-intelligence / codebase-to-AI tools.
Captured 2026-07-26.

## Tools compared

| Tool | Repository |
|------|-----------|
| Graphify | https://github.com/Graphify-Labs/graphify |
| code-review-graph | https://github.com/tirth8205/code-review-graph |
| repomix | https://github.com/yamadashy/repomix |
| GitNexus | https://github.com/abhigyanpatwari/GitNexus |

## What each tool is

- **Polyflow** — Go static-analysis tool that builds a **cross-service, cross-language
  code-flow graph** (SQLite + in-memory adjacency), serves an interactive Cytoscape.js
  visualization, and exposes an MCP server (`search`, `context`, `impact`, `trace`) with
  **token-budgeted output** aimed at AI-agent context retrieval.
  - Parsing: tree-sitter; languages Go, JS/TS, Ruby, Python, Templ, HTML, ERB, Vue, Svelte (~10).
  - CLI: `init, index (incremental, --full), serve, search, context, trace, impact, deps,
    patterns, service, link, exclude, config, eval, doctor, models`.
  - Differentiators: inter-service comm detection (HTTP / RabbitMQ / Kafka / SSE), variable
    dependency tracking, deployment topology, declarative YAML pattern registry, evidence/trust
    machinery.
- **Graphify** — Codebase → queryable knowledge graph via local tree-sitter AST (no LLM calls).
  Edges tagged `EXTRACTED` vs `INFERRED`. ~40 languages, community detection, multi-format
  ingest (PDF/images/video). Outputs `graph.html`, `GRAPH_REPORT.md`, `graph.json`. Invoked as
  a slash-command inside AI assistants.
- **code-review-graph** — Local-first code intelligence for AI review. Blast-radius analysis,
  incremental re-parse (<2s), 30+ languages, MCP (30 tools), GitHub Action with risk-scored PR
  reviews + merge gates, optional vector embeddings, D3 viz + community detection, exports
  GraphML/Cypher/Obsidian. Claims ~82x token reduction per query.
- **repomix** — Consolidates a repo into a single AI-friendly file (XML/MD/JSON/text). Token
  counting, include/exclude, gitignore-aware, Secretlint secret scanning, tree-sitter
  compression (~70% token cut), local or remote git repos.
- **GitNexus** — Code-intelligence platform building a knowledge graph. 17 MCP tools, impact
  analysis, multi-file rename suggestions with confidence, pre-commit impact reports, process
  traces, AI-generated wiki docs, 14+ languages, runs local CLI or in-browser, ingests git
  URL / ZIP. Positions on "precomputed relational intelligence."

## Feature comparison

| Capability | polyflow | Graphify | code-review-graph | repomix | GitNexus |
|---|---|---|---|---|---|
| Local AST graph (tree-sitter) | ✅ | ✅ | ✅ | ✅ (compress only) | ✅ |
| Cross-file linking | ✅ | ✅ | ✅ | ❌ | ✅ |
| Impact / blast-radius | ✅ | ➖ | ✅ | ❌ | ✅ |
| MCP tools | ✅ (4) | ➖ | ✅ (30) | ➖ | ✅ (17) |
| Token-budgeted context | ✅ | ➖ | ✅ | ✅ (counts) | ✅ |
| Interactive graph viz | ✅ Cytoscape | ✅ HTML | ✅ D3 | ❌ | ✅ |
| Incremental indexing | ✅ | ➖ | ✅ (<2s) | n/a | ➖ |
| Semantic/vector search | ✅ | ❌ (graph-only) | ✅ optional | ❌ | ➖ |
| **Cross-service comm (HTTP/MQ/SSE)** | ✅ **unique** | ❌ | ❌ | ❌ | ❌ |
| **Variable dependency tracking** | ✅ **unique** | ❌ | ❌ | ❌ | ❌ |
| Language breadth | ~10 | ~40 | 30+ | many | 14+ |
| Community / subsystem detection | ❌ | ✅ | ✅ | ❌ | ➖ |
| Remote git-URL / ZIP ingest | ❌ (local only) | ➖ | ✅ | ✅ | ✅ |
| GitHub Action / PR review + merge gate | ❌ | ❌ | ✅ | ➖ | ✅ (pre-commit) |
| Graph export (JSON/GraphML/Cypher/Obsidian) | ➖ (live serve) | ✅ | ✅ | n/a | ✅ |
| Repo → single AI file bundle | ❌ | ❌ | ❌ | ✅ core | ❌ |
| Secret scanning | ❌ | ❌ | ❌ | ✅ | ❌ |
| Multi-format ingest (PDF/docs/images) | ❌ | ✅ | ➖ | ➖ | ➖ |
| Multi-file rename suggestions | ❌ | ❌ | ➖ | ❌ | ✅ |
| AI-generated wiki/docs | ❌ | ✅ (report) | ➖ | ✅ | ✅ |

Legend: ✅ present · ➖ partial/adjacent · ❌ absent

## Verdict

Polyflow already matches the core of all four — code graph, impact analysis, MCP context
delivery, token budgeting, interactive viz, incremental indexing, semantic search. Its **moat
is unmatched**: it is the only one of the five that models *inter-service communication and
variable-level data flow*. The peers are all single-repo, single-graph structural tools. This
aligns with the primary goal (AI-agent context retrieval, graph recall over precision).

## Gaps worth closing (ranked)

1. **Remote repo / ZIP ingestion** — code-review-graph, repomix, and GitNexus all accept a git
   URL; polyflow is local-paths-only (design says v2). Biggest adoption friction. Touched by
   `plan-8-multi-repo`.
2. **CI/PR-review integration** — code-review-graph and GitNexus offer risk-scored PR reviews /
   merge gates. Polyflow only has `eval.yml`. A GitHub Action wrapping `impact` on a diff is
   high-leverage, low-cost (already have `internal/gitdiff` + `impact`).
3. **Community / subsystem detection** — Graphify and code-review-graph auto-cluster into
   subsystems for onboarding/architecture views. Polyflow has none.
4. **Portable graph export** — Peers emit `graph.json` / GraphML / Cypher / Obsidian as
   shareable artifacts. Polyflow serves live but has no graph dump. Cheap; enables offline
   sharing and eval reproducibility.
5. **Language breadth** — ~10 vs 30–40. Covered by `plan-3-language-breadth`; the cross-service
   stacks (Python/Java/Rust/C#) matter more than raw count.

## Deliberately out of scope

- repomix's whole-repo-to-one-file bundling and secret scanning — a different philosophy (dump
  vs. targeted retrieval) that contradicts polyflow's token-savings goal.
- Graphify's PDF/image/video ingestion — orthogonal to code-flow analysis.
