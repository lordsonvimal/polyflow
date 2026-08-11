package agentbench

import (
	"encoding/json"
	"regexp"
	"strings"
)

// FailureClass says what kind of thing went wrong, which is the difference
// between "the agent answered badly" and "the trial never happened".
//
// The 2026-07-30 juniper run is the reason this type exists: 5 of 7
// without_polyflow trials came back with a 429 session limit, were recorded as
// recall 0 / 0 tokens, and were then averaged in as if they were measurements.
// That produced a 0.286 control-arm recall that no agent ever earned, and a
// verdict computed from it.
type FailureClass string

const (
	// FailureNone means the run produced a real transcript.
	FailureNone FailureClass = ""
	// FailureQuota means the account is out of session/rate budget. Retrying
	// in-run is futile — the reset is hours away — and every further call
	// burns the remaining tasks into fake zeros, so the run must stop.
	FailureQuota FailureClass = "quota"
	// FailureTransient means a retry has a real chance: 5xx, timeouts,
	// unparsable output from a CLI that died mid-write.
	FailureTransient FailureClass = "transient"
	// FailureFatal means retrying changes nothing (bad flags, missing binary,
	// a refusal). Recorded, not retried, and not counted as a measurement.
	FailureFatal FailureClass = "fatal"
)

// failureEnvelope is the subset of the `claude -p --output-format json` result
// envelope that describes *why* a run failed. The CLI prints a well-formed
// envelope on stdout even when it exits non-zero, so the reason is available
// structurally and does not have to be scraped out of an error string.
//
// Note `subtype` is "success" on a 429, so it cannot be used to detect failure.
type failureEnvelope struct {
	Type           string `json:"type"`
	IsError        bool   `json:"is_error"`
	APIErrorStatus int    `json:"api_error_status"`
	TerminalReason string `json:"terminal_reason"`
	Result         string `json:"result"`
}

// quotaRe matches the human-readable forms of "you have no budget left".
// It is a backstop for the structural check on api_error_status, not the
// primary signal.
var quotaRe = regexp.MustCompile(`(?i)(session limit|usage limit|rate limit|quota exceeded|out of credit|insufficient credit)`)

// ClassifyFailure decides how to treat one `claude` invocation from its stdout
// and the exec error, if any.
//
// A nil execErr with clean output is FailureNone. Everything else is classified
// from the envelope when one can be parsed, and conservatively as transient
// when the output is unusable — matching the ~10-15% observed rate of the CLI
// simply falling over.
func ClassifyFailure(stdout []byte, execErr error) (FailureClass, string) {
	var env failureEnvelope
	parsed := json.Unmarshal(stdout, &env) == nil && env.Type == "result"

	if parsed && !env.IsError && execErr == nil {
		return FailureNone, ""
	}

	if parsed {
		detail := strings.TrimSpace(env.Result)
		switch {
		case env.APIErrorStatus == 429 || quotaRe.MatchString(detail):
			return FailureQuota, detail
		case env.APIErrorStatus >= 500, env.APIErrorStatus == 408,
			env.TerminalReason == "network_error":
			return FailureTransient, detail
		case env.IsError:
			return FailureFatal, detail
		}
	}

	if execErr == nil {
		// Exit 0 but nothing parseable came back: no transcript, no reason.
		return FailureTransient, "unparsable transcript"
	}
	if notFound(execErr) {
		return FailureFatal, execErr.Error()
	}
	return FailureTransient, execErr.Error()
}

// notFound reports whether err means the `claude` binary itself is missing,
// which no amount of retrying will fix.
func notFound(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory")
}

// Retryable reports whether a failure class is worth another attempt inside the
// same run.
func (c FailureClass) Retryable() bool { return c == FailureTransient }
