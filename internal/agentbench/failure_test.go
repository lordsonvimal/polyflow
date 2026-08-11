package agentbench_test

import (
	"errors"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/agentbench"
)

// The literal stdout of a without_polyflow arm from
// eval/agent-bench/results/2026-07-30-datascience.json. Note exit status was 1,
// `subtype` says "success", and `is_error` is the only structural admission
// besides api_error_status.
const quotaEnvelope = `{"type":"result","subtype":"success","is_error":true,` +
	`"api_error_status":429,"duration_ms":1199,"duration_api_ms":0,"num_turns":1,` +
	`"result":"You've hit your session limit · resets 10:40pm (Asia/Calcutta)",` +
	`"stop_reason":"stop_sequence","session_id":"b78e13dc","terminal_reason":"api_error"}`

func TestClassifyFailure_QuotaFromRealRun(t *testing.T) {
	class, detail := agentbench.ClassifyFailure([]byte(quotaEnvelope), errors.New("exit status 1"))
	if class != agentbench.FailureQuota {
		t.Fatalf("class = %q, want %q", class, agentbench.FailureQuota)
	}
	if detail == "" {
		t.Error("detail should carry the session-limit message")
	}
	if class.Retryable() {
		t.Error("a session limit must not be retried in-run: the reset is hours away")
	}
}

func TestClassifyFailure_Success(t *testing.T) {
	ok := `{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"a/b.go"}`
	class, _ := agentbench.ClassifyFailure([]byte(ok), nil)
	if class != agentbench.FailureNone {
		t.Fatalf("class = %q, want none", class)
	}
}

// A clean exit code with is_error set must still be a failure: otherwise the
// error message itself gets scored as if it were the agent's answer.
func TestClassifyFailure_ErrorEnvelopeWithZeroExit(t *testing.T) {
	body := `{"type":"result","subtype":"success","is_error":true,"result":"tool refused"}`
	class, detail := agentbench.ClassifyFailure([]byte(body), nil)
	if class != agentbench.FailureFatal {
		t.Fatalf("class = %q, want fatal", class)
	}
	if detail != "tool refused" {
		t.Errorf("detail = %q", detail)
	}
}

func TestClassifyFailure_Transient(t *testing.T) {
	body := `{"type":"result","is_error":true,"api_error_status":503,"result":"overloaded"}`
	class, _ := agentbench.ClassifyFailure([]byte(body), errors.New("exit status 1"))
	if class != agentbench.FailureTransient {
		t.Fatalf("class = %q, want transient", class)
	}
	if !class.Retryable() {
		t.Error("a 503 should be retried")
	}
}

func TestClassifyFailure_MissingBinaryIsFatal(t *testing.T) {
	class, _ := agentbench.ClassifyFailure(nil, errors.New(`exec: "claude": executable file not found in $PATH`))
	if class != agentbench.FailureFatal {
		t.Fatalf("class = %q, want fatal", class)
	}
	if class.Retryable() {
		t.Error("a missing binary must not be retried")
	}
}

func TestClassifyFailure_UnparsableOutputIsTransient(t *testing.T) {
	class, _ := agentbench.ClassifyFailure([]byte("panic: boom"), errors.New("exit status 2"))
	if class != agentbench.FailureTransient {
		t.Fatalf("class = %q, want transient", class)
	}
}

// A quota message with no api_error_status still has to be caught — the
// structural field is the primary signal, the text is the backstop.
func TestClassifyFailure_QuotaTextWithoutStatus(t *testing.T) {
	body := `{"type":"result","is_error":true,"result":"Usage limit reached for this account"}`
	class, _ := agentbench.ClassifyFailure([]byte(body), nil)
	if class != agentbench.FailureQuota {
		t.Fatalf("class = %q, want quota", class)
	}
}
