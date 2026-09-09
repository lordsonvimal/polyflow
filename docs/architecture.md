# Architecture — the layer contract

This is the contract new work is reviewed against. It is enforced, as far as an
import graph can enforce it, by `internal/arch/layers_test.go`, which runs in
the default `go test ./...`.

**Position statement.** Static analysis is polyflow's primary source of edges.
Runtime evidence is a *verification* signal (L6) and never a coverage
substitute. "Static" is not the same as "code parsing": config files, route
manifests, checked-in specs, schema dumps and lockfiles are all static, all
in-tree, and all fact sources (L1).

## The seven layers

Each layer has **exactly one extension mechanism**. Work that needs to touch
two layers is mis-partitioned; that is the review question.

```
L6  Verification        evidence SCORES the graph, never supplies it
      extend with: a provider (Go)

L5  Policy & minting    abstain vs mint, fan-out caps, grading, ledger
      extend with: Go — and it must stay small

L4  Contract matching   producer key <-> consumer key, per channel
      extend with: contracts/*.yaml

L3  Derivation          inheritance, mixins, route groups, filter chains,
                        containment, import closure
      extend with: rules

L2  Value resolution    "what string can this expression be?"
      extend with: valuegraph/<lang>.yaml

L1  Fact extraction     ├─ code:      patterns/<lang>/*.yaml
                        └─ artifacts: reader (Go) + artifacts/*.yaml

L0  Grammar adapters    tree-sitter binding + language quirks
      extend with: Go, ~200 LOC per language

    cross-cutting: Normalizers (Go, closed set)
                   Plugin boundary (sdk/linkplugin) — novel mechanisms go here
```

## The two invariants

**1. Only L5 holds policy.** L2 says what a value *is* and never what to do
about it. L3 derives facts and never decides whether to mint.

This is not stylistic. `LinkReactPropURLs` **abstains** when a prop resolves to
two paths — for the Rails→React boundary, disagreement means the resolver bound
the wrong component. Tier UB.2 deliberately **does not** abstain: a reusable
modal genuinely has many create URLs, and abstaining loses every one of those
flows. Both consume the same union-of-two-paths shape from L2. Any layer below
L5 that "resolves" that disagreement silently breaks one of them.

**2. Unresolved is never empty.** Every site a layer cannot resolve produces a
ledger row with a reason, never a silent drop. This is the recall-preserving
contract; L2's `Opaque` origin and L3's rule attribution both exist to carry the
reason forward. When you reuse an existing ledger kind string, reuse it
verbatim — a near-miss name gets replaced by a generic row and the site is lost
twice.

## Where new work goes

Ask in order; the first yes wins.

1. New **syntactic shape**? → L1 code YAML (`patterns/<lang>/*.yaml`).
2. New **in-tree artifact** holding graph facts? → L1 artifact reader + mapping YAML.
3. New way a **value hides**? → L2 spec rule.
4. New **relationship derived from existing facts**? → L3 rules.
5. New **channel**? → L4 YAML (`contracts/*.yaml`).
6. Otherwise it is a genuinely novel mechanism → **plugin** (`sdk/linkplugin`),
   not core.

With runtime evidence off the table as a coverage source, step 6 is where the
tail lands, which makes the plugin path load-bearing.

## Enforcement

`internal/arch/layers_test.go` pins a table of "package X may not import
package Y" rules and checks them against the real import graph, direct and
transitive, using `golang.org/x/tools/go/packages`. A rule naming a package
that does not exist yet is inert and logged as such, so a typo does not hide.

To grant an exception, add an entry to the `exceptions` table in that file with
the date it was granted and the condition that retires it. **An exception with
no date is a permanent one.**

The guard checks direction, not intent. The first request to put a `where:` or
`when:` clause into a valuegraph spec, or a threshold into a derivation rule, is
policy leaking below L5, and no import-graph test can catch it. Reviewers should
treat "it's only one condition" as the signal it is.

## Status

The layers marked as future extension points above are landing incrementally:
L2 as `internal/valuegraph`, L1-artifacts as `internal/artifact`, L3 rules as
`internal/datalog`, and per-edge layer/rule provenance on `graph.SourceRef`.
`internal/arch/layers_test.go` already pins their dependency direction, so the
rules are in place before the packages are. Until each lands, the corresponding
work is Go in
`internal/linker` — that is the cost this plan exists to remove, not a licence
to add more of it.
