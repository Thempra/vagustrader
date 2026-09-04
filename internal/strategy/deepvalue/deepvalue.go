package deepvalue

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/strategy/kelly"
)

type DeepValueStrategy struct {
	deadTokens map[string]int64
}

func New() *DeepValueStrategy {
	return &DeepValueStrategy{deadTokens: make(map[string]int64)}
}

func (s *DeepValueStrategy) Name() string { return "deep_value" }

func (s *DeepValueStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.DeepValue
	if !config.Enabled {
		slog.Info("DeepValue disabled")
		return nil
	}
	slog.Info("DeepValue started",
		"poll", config.PollIntervalSeconds,
		"price_range", fmt.Sprintf("0.01-%.2f", config.MaxPrice),
		"min_volume", config.MinVolume,
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("DeepValue tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
	}
}

func (s *DeepValueStrategy) tick(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.DeepValue
	risk := b.GetConfig().Risk

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	positions, err := b.GetPositionsByStrategy("deep_value")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	var scanned, skippedVolume, skippedPrice, skippedHeld, skippedDead, opened int
	now := time.Now().Unix()

	for _, m := range markets {
		if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
			continue
		}

		// Filter by minimum volume
		if m.Volume < config.MinVolume {
			skippedVolume++
			continue
		}

		scanned++

		for i, outcome := range m.Outcomes {
			if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
				break
			}

			tokenID := m.TokenIDs[i]
			price := m.OutcomePrices[i]

			// Only consider very cheap tokens (value bets)
			if price <= 0 || price > config.MaxPrice {
				skippedPrice++
				continue
			}

			if _, h := held[tokenID]; h {
				skippedHeld++
				continue
			}

			if exp, ok := s.deadTokens[tokenID]; ok && exp > now {
				skippedDead++
				continue
			}

			winProb := price
			// For deep value: payout is large (1/price - 1), loss is small (price)
			payout := 1 - price
			lossAmount := price

			size := kelly.OptimalSize(kelly.EdgeEstimate{
				WinProb:    winProb,
				Payout:     payout,
				LossAmount: lossAmount,
			}, risk.MaxPositionUSDC*10, config.MaxPosition, config.KellyFraction)

			if size <= 0 {
				continue
			}

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

			result, buyErr := b.Buy(ctx, broker.BuyRequest{
				TokenID:  tokenID,
				MarketID: m.ConditionID,
				Outcome:  outcome,
				Price:    price,
				SizeUSDC: size,
				TickSize: tickSize,
				NegRisk:  negRisk,
				Strategy: "deep_value",
			})
			if buyErr != nil {
				if isDeadErr(buyErr) {
					s.deadTokens[tokenID] = now + 3600
				} else if strings.Contains(buyErr.Error(), "insufficient balance") {
					slog.Warn("DeepValue: insufficient balance")
					return nil
				} else {
					slog.Error("DeepValue buy error", "token", tokenID[:minLen(12, len(tokenID))], "error", buyErr)
				}
				continue
			}

			paperTag := ""
			if b.IsPaper() {
				paperTag = " [PAPER]"
			}
			expectedReturn := (1-price)/price*size - size
			slog.Info("DeepValue opened",
				"question", question,
				"outcome", outcome,
				"price", price,
				"size", size,
				"filled", result.FilledSize,
				"exp_return", fmt.Sprintf("%.2f", expectedReturn),
				"paper", paperTag,
			)

			if b.GetNotifier() != nil {
				mode := "LIVE"
				if b.IsPaper() {
					mode = "PAPER"
				}
				msg := fmt.Sprintf("%s [DeepValue] %s\n-> %s @ %.3f | $%.2f | EV: $%+.2f",
					mode, question, outcome, price, size, expectedReturn)
				b.Notify(msg)
			}

			opened++
			held[tokenID] = struct{}{}
		}
	}

	slog.Info("DeepValue tick end",
		"scanned", scanned,
		"skipped_volume", skippedVolume,
		"skipped_price", skippedPrice,
		"held", skippedHeld,
		"dead", skippedDead,
		"opened", opened,
	)
	if database := b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "deep_value",
			Scanned:      scanned,
			SkippedPrice: skippedPrice + skippedVolume,
			SkippedHeld:  skippedHeld,
			SkippedDead:  skippedDead,
			Opened:       opened,
		})
	}
	return nil
}

func isDeadErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "dead orderbook") || strings.Contains(msg, "does not exist")
}

func minLen(a, b int) int {
	if a < b {
		return a
	}
	return b
}
