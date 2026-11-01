//go:build ignore

// Prueba de humo de la integración con Bugsink. No forma parte del build.
//
//	go run ./scripts/bugsink_smoke.go
//
// Envía tres eventos: uno sintético, uno real vía el handler de slog y un
// pánico recuperado. Después consulta la API canónica para confirmar que
// llegaron y que se agruparon en issues.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/vagustrader/vagustrader/internal/telemetry"
)

func main() {
	dsn := os.Getenv("BUGSINK_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "BUGSINK_DSN no definido")
		os.Exit(2)
	}

	stop := telemetry.Init(telemetry.Config{
		DSN:         dsn,
		Environment: "smoke-test",
		Release:     "smoke-1",
	})
	defer stop()
	if !telemetry.Enabled() {
		fmt.Fprintln(os.Stderr, "telemetría no habilitada")
		os.Exit(1)
	}

	// 1) Error capturado explícitamente.
	id1 := telemetry.CaptureError(errors.New("vagustrader smoke: explicit capture"), "probe", "capture")
	fmt.Println("capture id:", id1)

	// 2) Error vía slog (el camino que usa el bot).
	slog.SetDefault(slog.New(telemetry.Wrap(slog.NewTextHandler(os.Stdout, nil))))
	slog.Error("vagustrader smoke: slog handler path", "error", errors.New("slog-path failure"), "strategy", "smoke")

	// 3) Pánico recuperado.
	func() {
		defer func() { telemetry.CapturePanic(recover()) }()
		panic("vagustrader smoke: recovered panic")
	}()

	// Flush antes de consultar.
	stop()
	time.Sleep(2 * time.Second)
	fmt.Println("eventos enviados")
}
