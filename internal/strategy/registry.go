package strategy

import (
	"fmt"
	"log/slog"

	"github.com/vagustrader/vagustrader/internal/strategy/copytrading"
	"github.com/vagustrader/vagustrader/internal/strategy/settlement"
)

type Factory func() Strategy

var registry = map[string]Factory{
	"copy_trading": func() Strategy {
		return copytrading.New(nil)
	},
	"settlement": func() Strategy {
		return settlement.New(nil)
	},
}

func Create(name string) (Strategy, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown strategy: %s", name)
	}
	return f(), nil
}

func Registered() []string {
	names := make([]string, 0, len(registry))
	for k := range registry {
		names = append(names, k)
	}
	return names
}

func Register(name string, f Factory) {
	if _, exists := registry[name]; exists {
		slog.Warn("Overwriting strategy registration", "name", name)
	}
	registry[name] = f
}
