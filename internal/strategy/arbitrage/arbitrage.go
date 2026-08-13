package arbitrage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
)

type ArbitrageStrategy struct{}

func New() *ArbitrageStrategy {
	return &ArbitrageStrategy{}
}

func (s *ArbitrageStrategy) Name() string { return "arbitrage" }

func (s *ArbitrageStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.Arbitrage
	if !config.Enabled {
		slog.Info("Arbitrage disabled")
		return nil
	}
	slog.Info("Arbitrage started", "poll", config.PollIntervalSeconds, "min_profit", config.MinProfitUSDC)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(b); err != nil {
			slog.Error("Arbitrage tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
	}
}

func (s *ArbitrageStrategy) tick(b *broker.Broker) error {
	config := b.GetConfig().Strategies.Arbitrage

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	positions, err := b.GetPositionsByStrategy("arbitrage")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	var scanned, arbFound int

	for _, m := range markets {
		if len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
			continue
		}

		totalPrice := 0.0
		for _, p := range m.OutcomePrices {
			totalPrice += p
		}

		if totalPrice >= 1.0 {
			scanned++
			continue
		}

		discount := 1.0 - totalPrice
		if discount <= 0 {
			continue
		}

		allHeld := true
		for _, tid := range m.TokenIDs {
			if _, h := held[tid]; !h {
				allHeld = false
				break
			}
		}
		if allHeld {
			continue
		}

		estimatedProfit := discount * config.MaxPosition
		if estimatedProfit < config.MinProfitUSDC {
			continue
		}

		perOutcomeSize := config.MaxPosition / float64(len(m.OutcomePrices))

		mktData := b.GetMarketData(m.ConditionID)
		tickSize := "0.01"
		negRisk := false
		if mktData != nil {
			if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
				tickSize = ts
			}
			negRisk, _ = mktData["neg_risk"].(bool)
		}

		question := m.Question
		if len(question) > 60 {
			question = question[:60]
		}

		slog.Info("Arbitrage found",
			"question", question,
			"total_price", fmt.Sprintf("%.4f", totalPrice),
			"discount", fmt.Sprintf("%.4f", discount),
			"est_profit", fmt.Sprintf("$%.2f", estimatedProfit),
			"outcomes", len(m.OutcomePrices),
		)

		for i, outcome := range m.Outcomes {
			if i >= len(m.TokenIDs) || i >= len(m.OutcomePrices) {
				break
			}

			tokenID := m.TokenIDs[i]
			price := m.OutcomePrices[i]

			if price <= 0 {
				continue
			}

			if _, h := held[tokenID]; h {
				continue
			}

			result, buyErr := b.Buy(context.Background(), broker.BuyRequest{
				TokenID:  tokenID,
				MarketID: m.ConditionID,
				Outcome:  outcome,
				Price:    price,
				SizeUSDC: perOutcomeSize,
				TickSize: tickSize,
				NegRisk:  negRisk,
				Strategy: "arbitrage",
			})
			if buyErr != nil {
				if strings.Contains(buyErr.Error(), "insufficient balance") {
					slog.Warn("Arbitrage: insufficient balance")
					return nil
				}
				slog.Error("Arbitrage buy error", "outcome", outcome, "error", buyErr)
				continue
			}

			paperTag := ""
			if b.IsPaper() {
				paperTag = " [PAPER]"
			}
			slog.Info("Arbitrage leg",
				"outcome", outcome,
				"price", price,
				"size", perOutcomeSize,
				"filled", result.FilledSize,
				"paper", paperTag,
			)

			held[tokenID] = struct{}{}
		}

		arbFound++

		if b.GetNotifier() != nil {
			mode := "LIVE"
			if b.IsPaper() {
				mode = "PAPER"
			}
			msg := fmt.Sprintf("%s [Arbitrage] %s\n-> Sum: %.4f | Discount: %.2f%% | Est profit: $%.2f",
				mode, question, totalPrice, discount*100, estimatedProfit)
			b.Notify(msg)
		}
	}

	slog.Info("Arbitrage tick", "scanned", scanned, "arb_found", arbFound)
	return nil
}
