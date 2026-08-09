package strategy

import (
	"context"

	"github.com/vagustrader/vagustrader/internal/broker"
)

type Strategy interface {
	Name() string
	Run(ctx context.Context, b *broker.Broker) error
}
