package main

// Tests for how `polyflow eval` renders the D.1/D.2 precision half. These are
// presentation-only, but the message they produce is the one thing a failing
// CI run shows first, and it used to name the wrong defect class.

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/eval"
)

func TestEvalHardFailReason(t *testing.T) {
	silent := eval.Report{Repo: "r", Results: []eval.CaseResult{
		{CaseID: "a", HardFail: true, SilentMisses: 1},
	}}
	forbidden := eval.Report{Repo: "r", Results: []eval.CaseResult{
		{CaseID: "b", HardFail: true, ForbiddenHits: []string{"cam_client.go"}},
	}}

	// The D.2 case that motivated this: the whole fleet-juniper corpus
	// hard-fails on precision with recall 1.000 everywhere. Reporting a missed
	// file would point at the resolver's recall side, the opposite end from
	// the defect.
	assert.Equal(t, "must_not_include file returned — a hand-verified false positive",
		evalHardFailReason([]eval.Report{forbidden}))
	assert.Equal(t, "must_not_miss file silently missed",
		evalHardFailReason([]eval.Report{silent}))
	assert.Equal(t, "must_not_miss file silently missed, and must_not_include file returned",
		evalHardFailReason([]eval.Report{silent, forbidden}))

	// A hard fail with neither counter set is possible (an honest miss the
	// ledger did not cover); say so rather than inventing a cause.
	bare := eval.Report{Repo: "r", Results: []eval.CaseResult{{CaseID: "c", HardFail: true}}}
	assert.Equal(t, "see the case lines above", evalHardFailReason([]eval.Report{bare}))

	// A forbidden hit on a case that is NOT hard-failing cannot happen today
	// (ApplyPrecision sets both together), but the reason must key off the
	// hard fail, not off the presence of the slice.
	notFailing := eval.Report{Repo: "r", Results: []eval.CaseResult{
		{CaseID: "d", ForbiddenHits: []string{"x.go"}},
	}}
	assert.Equal(t, "see the case lines above", evalHardFailReason([]eval.Report{notFailing}))
}

func TestEvalPrecisionRendering(t *testing.T) {
	p := 0.75
	assert.Equal(t, "0.750", evalCasePrecision(eval.CaseResult{Precision: &p, Exhaustive: true}))
	assert.Equal(t, "n/a", evalCasePrecision(eval.CaseResult{}),
		"a non-exhaustive case must show no number at all, not 0.000")

	assert.Equal(t, "0.750 (2 exhaustive)", evalRepoPrecision(eval.Report{Precision: &p, ExhaustiveCases: 2}),
		"the repo figure must publish its own denominator")
	assert.Equal(t, "n/a (0 exhaustive cases)", evalRepoPrecision(eval.Report{}))

	assert.Equal(t, "  FORBIDDEN: a.go, b.go",
		evalForbidden(eval.CaseResult{ForbiddenHits: []string{"a.go", "b.go"}}))
	assert.Equal(t, "", evalForbidden(eval.CaseResult{}))
}
