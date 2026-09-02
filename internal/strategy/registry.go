package strategy

import (
	"fmt"
	"log/slog"

	"github.com/vagustrader/vagustrader/internal/strategy/arbitrage"
	"github.com/vagustrader/vagustrader/internal/strategy/contrarian"
	"github.com/vagustrader/vagustrader/internal/strategy/copytrading"
	"github.com/vagustrader/vagustrader/internal/strategy/momentumrider"
	"github.com/vagustrader/vagustrader/internal/strategy/resolutionsniper"
	"github.com/vagustrader/vagustrader/internal/strategy/settlement"
	"github.com/vagustrader/vagustrader/internal/strategy/underhunter"
	"github.com/vagustrader/vagustrader/internal/strategy/whaleconsensus"
)

type Factory func() Strategy

var registry = map[string]Factory{
	"copy_trading": func() Strategy {
		return copytrading.New(nil)
	},
	"settlement": func() Strategy {
		return settlement.New(nil)
	},
	"under_hunter": func() Strategy {
		return underhunter.New()
	},
	"whale_consensus": func() Strategy {
		return whaleconsensus.New()
	},
	"arbitrage": func() Strategy {
		return arbitrage.New()
	},
	"contrarian": func() Strategy {
		return contrarian.New()
	},
	"resolution_sniper": func() Strategy {
		return resolutionsniper.New()
	},
	"momentum_rider": func() Strategy {
		return momentumrider.New()
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
