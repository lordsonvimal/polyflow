package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
)

// Each capture subcommand has its own flag variable set. They share the same
// names but bind to separate addresses so there is no cross-command state.
var (
	captureStartSession  string
	captureStartHTTPPort int
	captureStartGRPCPort int

	captureStopSession string

	captureRunSession  string
	captureRunHTTPPort int
	captureRunGRPCPort int
)

var captureCmd = &cobra.Command{
	Use:   "capture",
	Short: "Manage OTLP capture sessions (start / stop / run)",
}

var captureStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start an OTLP receiver recording spans into a capture session",
	Long: `Start an embedded OTLP receiver (default HTTP :4318, gRPC :4317) and
record every span received into a capture session until SIGTERM/SIGINT.

Run in the background to keep your shell free:

  polyflow capture start --session my-session &
  polyflow capture stop  --session my-session`,
	RunE: runCaptureStart,
}

var captureStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop a running capture session (sends SIGTERM to the start process)",
	RunE:  runCaptureStop,
}

var captureRunCmd = &cobra.Command{
	Use:   "run [--session <name>] -- <command...>",
	Short: "Run a command with an embedded OTLP receiver; exit code mirrors the command",
	Long: `Starts an embedded OTLP receiver, injects OTEL_EXPORTER_OTLP_ENDPOINT,
OTEL_EXPORTER_OTLP_PROTOCOL, and OTEL_TRACES_EXPORTER into the child
environment, runs the command, then stops the receiver.

Example:

  polyflow capture run --session e2e -- go test ./e2e/...`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCaptureRun,
}

func init() {
	captureStartCmd.Flags().StringVar(&captureStartSession, "session", "", "session name (default: timestamp)")
	captureStartCmd.Flags().IntVar(&captureStartHTTPPort, "http-port", 4318, "OTLP/HTTP listener port")
	captureStartCmd.Flags().IntVar(&captureStartGRPCPort, "grpc-port", 4317, "OTLP/gRPC listener port")

	captureStopCmd.Flags().StringVar(&captureStopSession, "session", "", "session name to stop (required)")

	captureRunCmd.Flags().StringVar(&captureRunSession, "session", "", "session name (default: timestamp)")
	captureRunCmd.Flags().IntVar(&captureRunHTTPPort, "http-port", 4318, "OTLP/HTTP listener port")
	captureRunCmd.Flags().IntVar(&captureRunGRPCPort, "grpc-port", 4317, "OTLP/gRPC listener port")

	captureCmd.AddCommand(captureStartCmd, captureStopCmd, captureRunCmd)
	rootCmd.AddCommand(captureCmd)
}

// ─── capture start ────────────────────────────────────────────────────────────

func runCaptureStart(cmd *cobra.Command, args []string) error {
	name := sessionName(captureStartSession)

	sess, err := trace_ingest.NewSession(capturesBase(), name, "partial")
	if err != nil {
		return fmt.Errorf("capture start: %w", err)
	}

	recv := trace_ingest.NewReceiver(sess, captureStartHTTPPort, captureStartGRPCPort)
	if err := recv.Start(); err != nil {
		return fmt.Errorf("capture start: receiver: %w", err)
	}

	if err := sess.WritePID(); err != nil {
		recv.Stop()
		return fmt.Errorf("capture start: write pid: %w", err)
	}

	fmt.Printf("Capture session %q started.\n", name)
	fmt.Printf("  OTLP/HTTP  http://localhost:%d/v1/traces\n", recv.HTTPPort())
	fmt.Printf("  OTLP/gRPC  localhost:%d\n", recv.GRPCPort())
	fmt.Printf("\nSet OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:%d in your app.\n", recv.HTTPPort())
	fmt.Printf("Stop: polyflow capture stop --session %s\n\n", name)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	fmt.Printf("\nStopping capture session %q...\n", name)
	recv.Stop()
	<-recv.Done()
	sess.RemovePID()
	if err := sess.Finalize(""); err != nil {
		fmt.Fprintf(os.Stderr, "capture start: finalize: %v\n", err)
	}
	fmt.Printf("Session %q finalised in %s\n", name, sess.Dir())
	return nil
}

// ─── capture stop ─────────────────────────────────────────────────────────────

func runCaptureStop(cmd *cobra.Command, args []string) error {
	if captureStopSession == "" {
		return fmt.Errorf("capture stop: --session is required")
	}
	sessDir := filepath.Join(capturesBase(), captureStopSession)
	pid, err := trace_ingest.ReadSessionPID(sessDir)
	if err != nil {
		return fmt.Errorf("capture stop: read pidfile for session %q: %w", captureStopSession, err)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("capture stop: find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("capture stop: signal process %d: %w", pid, err)
	}
	fmt.Printf("Sent SIGTERM to session %q (pid %d); session will be finalised shortly.\n", captureStopSession, pid)
	return nil
}

// ─── capture run ──────────────────────────────────────────────────────────────

func runCaptureRun(cmd *cobra.Command, args []string) error {
	name := sessionName(captureRunSession)

	sess, err := trace_ingest.NewSession(capturesBase(), name, "full")
	if err != nil {
		return fmt.Errorf("capture run: %w", err)
	}

	recv := trace_ingest.NewReceiver(sess, captureRunHTTPPort, captureRunGRPCPort)
	if err := recv.Start(); err != nil {
		return fmt.Errorf("capture run: receiver: %w", err)
	}

	endpoint := fmt.Sprintf("http://localhost:%d", recv.HTTPPort())
	child := exec.Command(args[0], args[1:]...)
	child.Env = injectOTELEnv(os.Environ(), endpoint)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin

	fmt.Printf("Capture run session %q: %s\n", name, strings.Join(args, " "))

	runErr := child.Run()
	recv.Stop()
	<-recv.Done()
	if finalErr := sess.Finalize(strings.Join(args, " ")); finalErr != nil {
		fmt.Fprintf(os.Stderr, "capture run: finalize: %v\n", finalErr)
	}

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("capture run: execute: %w", runErr)
	}
	fmt.Printf("Capture session %q complete. Inspect: polyflow flows --session %s\n", name, name)
	return nil
}

// injectOTELEnv returns env with the three OTLP exporter variables set,
// overriding any existing values. This is the env-injection contract from
// docs/runtime-flow-plan.md §capture run.
func injectOTELEnv(env []string, endpoint string) []string {
	inject := map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": endpoint,
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_TRACES_EXPORTER":        "otlp",
	}
	out := make([]string, 0, len(env)+len(inject))
	for _, kv := range env {
		key := kv
		if idx := strings.Index(kv, "="); idx >= 0 {
			key = kv[:idx]
		}
		if _, overridden := inject[key]; !overridden {
			out = append(out, kv)
		}
	}
	for k, v := range inject {
		out = append(out, k+"="+v)
	}
	return out
}
