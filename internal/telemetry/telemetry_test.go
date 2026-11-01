package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

// An empty DSN must leave reporting off and every helper must be a safe no-op.
// This is the configuration used by tests and by anyone running the bot
// without an error tracker.
func TestInitWithoutDSNDisablesReporting(t *testing.T) {
	stop := Init(Config{DSN: "", Environment: "test", Release: "test"})
	defer stop()

	if Enabled() {
		t.Fatal("reporting must be disabled when the DSN is empty")
	}
	if id := CaptureError(errors.New("boom")); id != "" {
		t.Errorf("CaptureError returned id %q with reporting disabled", id)
	}
	// Must not panic, must not block on a network round-trip.
	CapturePanic("recovered value")
}

// The wrapped handler must keep forwarding records to the inner handler
// regardless of telemetry state, otherwise enabling tracking would change
// what lands in the log file.
func TestWrapDelegatesToInnerHandler(t *testing.T) {
	stop := Init(Config{DSN: "", Environment: "test"})
	defer stop()

	rec := &recordingHandler{}
	logger := slog.New(Wrap(rec))

	logger.Info("hello")
	logger.Warn("careful")
	logger.Error("failed", "error", errors.New("boom"))

	if got := rec.messages; len(got) != 3 {
		t.Fatalf("inner handler saw %d records, want 3: %v", len(got), got)
	}
	if rec.messages[2] != "failed" {
		t.Errorf("last record = %q, want %q", rec.messages[2], "failed")
	}
}

// WithAttrs/WithGroup must not drop the decoration: a child logger would
// otherwise lose the attributes added by the caller.
func TestWrapPreservesAttrsAndGroups(t *testing.T) {
	stop := Init(Config{DSN: ""})
	defer stop()

	rec := &recordingHandler{}
	logger := slog.New(Wrap(rec)).With("strategy", "copy_trading").WithGroup("g")
	logger.Error("noisy", "market", "abc")

	if len(rec.messages) != 1 {
		t.Fatalf("inner handler saw %d records, want 1", len(rec.messages))
	}
}

func TestReleaseNeverEmpty(t *testing.T) {
	if got := Release(); got == "" {
		t.Fatal("Release must never return an empty string")
	}
}

type recordingHandler struct {
	messages []string
}

func (r *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (r *recordingHandler) Handle(_ context.Context, rec slog.Record) error {
	r.messages = append(r.messages, rec.Message)
	return nil
}

func (r *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return r }

func (r *recordingHandler) WithGroup(_ string) slog.Handler { return r }
