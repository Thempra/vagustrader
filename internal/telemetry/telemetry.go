// Package telemetry wires the process into the Bugsink error tracker
// (https://tracker.thempra.net), which speaks the Sentry wire protocol.
//
// Design notes:
//   - Reporting is entirely opt-in through BUGSINK_DSN. An empty DSN is a
//     valid, silent configuration: every helper in this package degrades to a
//     no-op so tests and local runs never touch the network.
//   - Only ERROR-level slog records become events. INFO/WARN stay in the
//     rotating file log; otherwise a trading bot would burn the project's
//     event quota with routine noise.
//   - Bugsink implements error tracking only, so tracing, sessions, metrics
//     and auto-instrumentation are all disabled.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
)

// EnvDSN names the environment variable holding the project DSN. The DSN is a
// secret (it embeds the ingestion key): keep it in .env, never in git.
const EnvDSN = "BUGSINK_DSN"

// Config controls Init.
type Config struct {
	// DSN is the Bugsink project DSN. Empty disables reporting.
	DSN string
	// Environment is the Bugsink environment tag ("production", "paper", ...).
	Environment string
	// Release is the version tag events are attributed to.
	Release string
}

var (
	mu       sync.RWMutex
	enabled  bool
	shutdown func()
)

// Init configures the global Sentry/Bugsink client and installs a capped
// background flusher. It returns a stop function that flushes pending events;
// calling it more than once is safe. Init never fails hard: an unusable DSN
// disables reporting and reports the problem through slog instead of aborting
// the process, because error tracking must never take trading down.
func Init(cfg Config) func() {
	mu.Lock()
	defer mu.Unlock()

	if enabled && shutdown != nil {
		return shutdown
	}

	dsn := strings.TrimSpace(cfg.DSN)
	if dsn == "" {
		slog.Info("error tracking disabled", "reason", EnvDSN+" not set")
		enabled = false
		return func() {}
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      cfg.Environment,
		Release:          cfg.Release,
		AttachStacktrace: true,
		// Bugsink has no tracing/session/metrics support.
		EnableTracing:    false,
		TracesSampleRate: 0,
		SendDefaultPII:   false,
		// Keep the payload small: the trading bot logs large structs.
		MaxBreadcrumbs: 0,
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			event.ServerName = hostname()
			return event
		},
	})
	if err != nil {
		slog.Warn("error tracking disabled: invalid DSN", "error", err)
		enabled = false
		return func() {}
	}

	enabled = true
	stop := func() {
		mu.Lock()
		enabled = false
		mu.Unlock()
		sentry.Flush(3 * time.Second)
	}
	shutdown = stop
	slog.Info("error tracking enabled", "release", cfg.Release, "environment", cfg.Environment)
	return stop
}

// Enabled reports whether events are currently being shipped.
func Enabled() bool {
	mu.RLock()
	defer mu.RUnlock()
	return enabled
}

// CaptureError ships err to Bugsink, tagged with the given attributes, and
// returns the event ID (empty when reporting is disabled). Use it for errors
// that are handled rather than logged: anything that reaches slog at Error
// level is already captured by the Handler wrapper.
func CaptureError(err error, attrs ...any) string {
	if err == nil || !Enabled() {
		return ""
	}
	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetLevel(sentry.LevelError)
	applyAttrs(hub.Scope(), attrs)
	if id := hub.CaptureException(err); id != nil {
		return string(*id)
	}
	return ""
}

// CapturePanic ships a recovered panic value together with the stack that was
// being unwound. Call it from a deferred recover:
//
//	defer func() { telemetry.CapturePanic(recover()) }()
func CapturePanic(rec any) {
	if rec == nil {
		return
	}
	hub := sentry.CurrentHub().Clone()
	event := sentry.NewEvent()
	event.Level = sentry.LevelFatal
	event.Message = fmt.Sprintf("panic: %v", rec)

	// The unwinding stack is taken from here: the goroutine is still inside
	// the deferred call, so the frames below are the ones that panicked.
	if st := sentry.NewStacktrace(); st != nil {
		event.Threads = []sentry.Thread{{
			Current:    true,
			Stacktrace: st,
			Crashed:    true,
		}}
	}
	if err, ok := rec.(error); ok {
		event.Exception = []sentry.Exception{{
			Type:  fmt.Sprintf("%T", rec),
			Value: err.Error(),
		}}
	} else {
		event.Exception = []sentry.Exception{{
			Type:  "panic",
			Value: fmt.Sprintf("%v", rec),
		}}
	}
	if Enabled() {
		hub.CaptureEvent(event)
		sentry.Flush(3 * time.Second)
	}
}

// Wrap decorates a slog.Handler so that Error records are also shipped as
// Bugsink events. The record still flows to the inner handler, so file logging
// is unaffected.
func Wrap(inner slog.Handler) slog.Handler {
	if inner == nil {
		return nil
	}
	return &handler{inner: inner}
}

type handler struct {
	inner slog.Handler
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &handler{inner: h.inner.WithAttrs(attrs)}
}

func (h *handler) WithGroup(name string) slog.Handler {
	return &handler{inner: h.inner.WithGroup(name)}
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= slog.LevelError && Enabled() {
		h.capture(r)
	}
	return h.inner.Handle(ctx, r)
}

// capture converts a slog record into a Bugsink event. The record's "error"
// attribute becomes the exception; anything else rides along as extra tags so
// the event is debuggable without the log file.
func (h *handler) capture(r slog.Record) {
	var (
		captured error
		attrs    []any
	)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "error" && a.Value.Kind() == slog.KindAny {
			if err, ok := a.Value.Any().(error); ok {
				captured = err
				return true
			}
		}
		attrs = append(attrs, a.Key, a.Value.Resolve().Any())
		return true
	})

	hub := sentry.CurrentHub().Clone()
	hub.Scope().SetLevel(sentry.LevelError)
	applyAttrs(hub.Scope(), attrs)

	event := sentry.NewEvent()
	event.Message = r.Message
	event.Level = sentry.LevelError
	event.Timestamp = r.Time
	if captured != nil {
		event.SetException(captured, 10)
		// Fingerprint on the error location chain so the same bug groups even
		// when the message carries variable data (market IDs, amounts).
		event.Fingerprint = []string{"{{ default }}", r.Message}
	}
	hub.CaptureEvent(event)
}

func applyAttrs(scope *sentry.Scope, kv []any) {
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok || key == "" || key == "error" {
			continue
		}
		scope.SetAttributes(attribute.String(key, fmt.Sprint(kv[i+1])))
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// Release derives a release tag for the running binary: the VCS revision baked
// in by the Go toolchain when available, otherwise "unknown". Callers that know
// better (a Makefile ldflag) should pass their own value instead.
func Release() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			if len(s.Value) > 12 {
				return s.Value[:12]
			}
			return s.Value
		}
	}
	return "unknown"
}

// ErrDisabled is returned by helpers that require reporting when no DSN is
// configured. It exists so callers can distinguish "not tracked" from "failed".
var ErrDisabled = errors.New("error tracking disabled: " + EnvDSN + " not set")
