package configsrc

import (
	"reflect"
	"testing"
)

// TestLoad_ShellSource verifies SH2's fixture: a literal `export`, a
// command-substitution `export` (must ledger — here, "ledger" means the
// existing k8s/terraform literal-only stance of skipping it entirely, not
// guessing a value), and a same-name override across two files (fan-out,
// both must appear — bug-class rule 1).
func TestLoad_ShellSource(t *testing.T) {
	dir := writeSvc(t, map[string]string{
		"deploy.sh": "export DATABASE_URL=postgres://a.internal/db\n" +
			"export BUILD_ID=$(date +%s)\n" +
			"export FROM_VAR=$OTHER\n",
		"entrypoint.sh": "export DATABASE_URL=postgres://b.internal/db\n" +
			"REGION=us-east-1 some-command\n" +
			"LOCAL_ONLY=nope\n",
	})

	got := Load(dir)

	want := []Value{
		{Value: "postgres://a.internal/db", Ref: "deploy.sh:1"},
		{Value: "postgres://b.internal/db", Ref: "entrypoint.sh:1"},
	}
	if !reflect.DeepEqual(got["DATABASE_URL"], want) {
		t.Fatalf("DATABASE_URL fan-out mismatch:\n got %#v\nwant %#v", got["DATABASE_URL"], want)
	}

	if _, ok := got["BUILD_ID"]; ok {
		t.Errorf("BUILD_ID is command-substitution built — must not resolve, got %#v", got["BUILD_ID"])
	}
	if _, ok := got["FROM_VAR"]; ok {
		t.Errorf("FROM_VAR is another-variable built — must not resolve, got %#v", got["FROM_VAR"])
	}

	wantRegion := []Value{{Value: "us-east-1", Ref: "entrypoint.sh:2"}}
	if !reflect.DeepEqual(got["REGION"], wantRegion) {
		t.Fatalf("REGION (leading-assignment form) mismatch:\n got %#v\nwant %#v", got["REGION"], wantRegion)
	}

	// A bare local assignment with no trailing command is not captured —
	// it's a local shell variable, not necessarily an exported env fact.
	if _, ok := got["LOCAL_ONLY"]; ok {
		t.Errorf("LOCAL_ONLY is a bare local assignment — must not be captured, got %#v", got["LOCAL_ONLY"])
	}
}

// TestLoad_ShellSource_QuotedAndBash verifies quoted export values strip
// correctly and .bash files are scanned too.
func TestLoad_ShellSource_QuotedAndBash(t *testing.T) {
	dir := writeSvc(t, map[string]string{
		"run.bash": "export API_KEY=\"abc123\"\n",
	})
	got := Load(dir)
	want := []Value{{Value: "abc123", Ref: "run.bash:1"}}
	if !reflect.DeepEqual(got["API_KEY"], want) {
		t.Fatalf("API_KEY mismatch:\n got %#v\nwant %#v", got["API_KEY"], want)
	}
}

// TestShellEnvValues_MissingDirIsEmpty mirrors the k8s/terraform sources'
// own "absence of config is a result, not an error" contract.
func TestShellEnvValues_MissingDirIsEmpty(t *testing.T) {
	got := shellEnvValues(t.TempDir() + "/does-not-exist")
	if len(got) != 0 {
		t.Fatalf("expected empty map for a missing dir, got %v", got)
	}
}
