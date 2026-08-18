package contrarian

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/strategy/kelly"
)

type ContrarianStrategy struct {
	deadTokens    map[string]int64
	priceSnapshot map[string]float64
	snapshotTime  time.Time
}

func New() *ContrarianStrategy {
	return &ContrarianStrategy{
		deadTokens:    make(map[string]int64),
		priceSnapshot: make(map[string]float64),
	}
}

func (s *ContrarianStrategy) Name() string { return "contrarian" }

func (s *ContrarianStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.Contrarian
	if !config.Enabled {
		slog.Info("Contrarian disabled")
		return nil
	}
	slog.Info("Contrarian started", "poll", config.PollIntervalSeconds, "threshold", config.PriceChangeThreshold)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(b); err != nil {
			slog.Error("Contrarian tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
	}
}

func (s *ContrarianStrategy) tick(b *broker.Broker) error {
	config := b.GetConfig().Strategies.Contrarian
	risk := b.GetConfig().Risk

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	currentPrices := make(map[string]float64)
	for _, m := range markets {
		for i, tokenID := range m.TokenIDs {
			if i < len(m.OutcomePrices) {
				currentPrices[tokenID] = m.OutcomePrices[i]
			}
		}
	}

	positions, err := b.GetPositionsByStrategy("contrarian")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	now := time.Now().Unix()
	var opened int

	if len(s.priceSnapshot) == 0 {
		s.priceSnapshot = currentPrices
		s.snapshotTime = time.Now()
		slog.Info("Contrarian: initial price snapshot saved", "tokens", len(currentPrices))
		return nil
	}

	lookbackDuration := time.Duration(config.LookbackMinutes) * time.Minute
	if time.Since(s.snapshotTime) > lookbackDuration {
		s.priceSnapshot = currentPrices
		s.snapshotTime = time.Now()
		slog.Info("Contrarian: price snapshot refreshed")
		return nil
	}

	for _, m := range markets {
		if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
			continue
		}

		for i, outcome := range m.Outcomes {
			if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
				break
			}

			tokenID := m.TokenIDs[i]
			currentPrice := m.OutcomePrices[i]

			if currentPrice <= 0.05 || currentPrice >= 0.95 {
				continue
			}

			if _, h := held[tokenID]; h {
				continue
			}

			if exp, ok := s.deadTokens[tokenID]; ok && exp > now {
				continue
			}

			previousPrice, hadPrevious := s.priceSnapshot[tokenID]
			if !hadPrevious || previousPrice <= 0 {
				continue
			}

			priceChange := (currentPrice - previousPrice) / previousPrice

			if priceChange > config.PriceChangeThreshold {
				oppositeIdx := 0
				if i == 0 {
					oppositeIdx = 1
				}
				if oppositeIdx >= len(m.TokenIDs) || oppositeIdx >= len(m.OutcomePrices) {
					continue
				}

				oppositeTokenID := m.TokenIDs[oppositeIdx]
				oppositePrice := m.OutcomePrices[oppositeIdx]
				oppositeOutcome := m.Outcomes[oppositeIdx]

				if _, h := held[oppositeTokenID]; h {
					continue
				}
				if oppositePrice <= 0.05 || oppositePrice >= 0.95 {
					continue
				}

				winProb := kelly.EstimateWinProb(oppositePrice, "", "", 0)
				if winProb < 0.35 {
					continue
				}

				size := kelly.OptimalSize(kelly.EdgeEstimate{
					WinProb:    winProb,
					Payout:     1 - oppositePrice,
					LossAmount: oppositePrice,
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

				result, buyErr := b.Buy(context.Background(), broker.BuyRequest{
					TokenID:  oppositeTokenID,
					MarketID: m.ConditionID,
					Outcome:  oppositeOutcome,
					Price:    oppositePrice,
					SizeUSDC: size,
					TickSize: tickSize,
					NegRisk:  negRisk,
					Strategy: "contrarian",
				})
				if buyErr != nil {
					if isDeadErr(buyErr) {
						s.deadTokens[oppositeTokenID] = now + 3600
					} else if strings.Contains(buyErr.Error(), "insufficient balance") {
						slog.Warn("Contrarian: insufficient balance")
						return nil
					} else {
						slog.Error("Contrarian buy error", "token", oppositeTokenID[:minL(12, len(oppositeTokenID))], "error", buyErr)
					}
					continue
				}

				paperTag := ""
				if b.IsPaper() {
					paperTag = " [PAPER]"
				}
				slog.Info("Contrarian opened",
					"question", question,
					"moved", outcome,
					"change", fmt.Sprintf("%+.1f%%", priceChange*100),
					"buying", oppositeOutcome,
					"price", oppositePrice,
					"size", size,
					"filled", result.FilledSize,
					"paper", paperTag,
				)

				if b.GetNotifier() != nil {
					mode := "LIVE"
					if b.IsPaper() {
						mode = "PAPER"
					}
					msg := fmt.Sprintf("%s [Contrarian] %s\n-> %s moved %+.1f%%, buying %s @ %.3f | $%.2f",
						mode, question, outcome, priceChange*100, oppositeOutcome, oppositePrice, size)
					b.Notify(msg)
				}

				opened++
				held[oppositeTokenID] = struct{}{}
			}
		}
	}

	slog.Info("Contrarian tick", "opened", opened)
	return nil
}

func isDeadErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "dead orderbook") || strings.Contains(msg, "does not exist")
}

func minL(a, b int) int {
	if a < b {
		return a
	}
	return b
}
