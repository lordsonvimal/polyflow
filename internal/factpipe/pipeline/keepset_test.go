package pipeline

import "testing"

// TestFrameworkKeepSetCoversApplyStageReads is XM.2's audit regression test
// (docs/factpipe-cross-framework-matching-plan.md): frameworkKeepSet's
// union must include every predicate ApplyResolves/ApplyConfig/ApplyTable/
// ApplyDerive actually reads out of the copied base FactSet, or XM.2's
// filter silently starves that stage — no build failure, just an empty
// relation. This walks the real embedded registry rather than a synthetic
// fixture so it catches a future resolve:/config:/table:/derive: block this
// audit forgot, not just today's known set.
func TestFrameworkKeepSetCoversApplyStageReads(t *testing.T) {
	reg, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	var checkedResolve, checkedConfig, checkedTable, checkedDerive int
	for _, fw := range reg.All() {
		keep := frameworkKeepSet(fw)
		for _, r := range fw.Resolves {
			checkedResolve++
			if !keep[r.From()] {
				t.Errorf("framework %s: resolve: from %q missing from keep-set", fw.Name, r.From())
			}
		}
		for _, c := range fw.Configs {
			checkedConfig++
			if !keep[c.From()] {
				t.Errorf("framework %s: config: from %q missing from keep-set", fw.Name, c.From())
			}
		}
		for _, tb := range fw.Tables {
			if against := tb.Against(); against != "" {
				checkedTable++
				if !keep[against] {
					t.Errorf("framework %s: table: against %q missing from keep-set", fw.Name, against)
				}
			}
		}
		if len(fw.Derives) > 0 {
			checkedDerive++
			if !keep["node_meta"] {
				t.Errorf("framework %s: derive: block present but keep-set missing node_meta", fw.Name)
			}
		}
	}
	// resolve: is the one of these four blocks the embedded registry actually
	// uses today (js_lazy_import_calls, sprockets_directives,
	// sprockets_includes) — require this one non-vacuous so the test can't
	// silently stop guarding ApplyResolves. config:/table:/derive: have no
	// current embedded consumer (rails_devise/schema_url_link/gorm_tables/
	// rails_model_tables use the hub or extract:-verb path instead, not
	// these YAML blocks) — the loop above still checks correctness for any
	// that do exist, but a zero count for these three is today's real state,
	// not a test bug.
	if checkedResolve == 0 {
		t.Error("no embedded framework declares a resolve: block — test is vacuous for ApplyResolves")
	}
	t.Logf("checked: resolve=%d config=%d table=%d derive=%d", checkedResolve, checkedConfig, checkedTable, checkedDerive)
}
