package resolutionsniper

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

type ResolutionSniperStrategy struct {
	deadTokens map[string]int64
}

func New() *ResolutionSniperStrategy {
	return &ResolutionSniperStrategy{deadTokens: make(map[string]int64)}
}

func (s *ResolutionSniperStrategy) Name() string { return "resolution_sniper" }

func (s *ResolutionSniperStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.ResolutionSniper
	if !config.Enabled {
		slog.Info("ResolutionSniper disabled")
		return nil
	}
	slog.Info("ResolutionSniper started",
		"poll", config.PollIntervalSeconds,
		"price_range", fmt.Sprintf("%.2f-%.2f", config.MinPrice, config.MaxPrice),
		"max_days", config.MaxDaysToEnd,
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("ResolutionSniper tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
	}
}

func (s *ResolutionSniperStrategy) tick(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.ResolutionSniper
	risk := b.GetConfig().Risk

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	positions, err := b.GetPositionsByStrategy("resolution_sniper")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	var scanned, skippedPrice, skippedVolume, skippedExpiry, skippedHeld, skippedDead, opened int
	now := time.Now().Unix()
	cutoff := time.Now().Add(time.Duration(config.MaxDaysToEnd) * 24 * time.Hour)

	for _, m := range markets {
		if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
			continue
		}

		if m.Volume < config.MinVolume {
			skippedVolume++
			continue
		}

		var endDate time.Time
		if m.EndDate != "" {
			endDate, _ = time.Parse(time.RFC3339, m.EndDate)
		}
		if endDate.IsZero() || endDate.After(cutoff) {
			skippedExpiry++
			continue
		}
		if endDate.Before(time.Now()) {
			skippedExpiry++
			continue
		}

		scanned++

		for i, outcome := range m.Outcomes {
			if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
				break
			}

			tokenID := m.TokenIDs[i]
			price := m.OutcomePrices[i]

			if price < config.MinPrice || price > config.MaxPrice {
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
			size := kelly.OptimalSize(kelly.EdgeEstimate{
				WinProb:    winProb,
				Payout:     1 - price,
				LossAmount: price,
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
				Strategy: "resolution_sniper",
			})
			if buyErr != nil {
				if isDeadErr(buyErr) {
					s.deadTokens[tokenID] = now + 3600
				} else if strings.Contains(buyErr.Error(), "insufficient balance") {
					slog.Warn("ResolutionSniper: insufficient balance")
					return nil
				} else {
					slog.Error("ResolutionSniper buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
				}
				continue
			}

			paperTag := ""
			if b.IsPaper() {
				paperTag = " [PAPER]"
			}
			daysLeft := int(endDate.Sub(time.Now()).Hours() / 24)
			slog.Info("ResolutionSniper opened",
				"question", question,
				"outcome", outcome,
				"price", price,
				"size", size,
				"filled", result.FilledSize,
				"days_left", daysLeft,
				"paper", paperTag,
			)

			if b.GetNotifier() != nil {
				mode := "LIVE"
				if b.IsPaper() {
					mode = "PAPER"
				}
				msg := fmt.Sprintf("%s [ResolutionSniper] %s\n-> %s @ %.3f | $%.2f | resolves in %dd",
					mode, question, outcome, price, size, daysLeft)
				b.Notify(msg)
			}

			opened++
			held[tokenID] = struct{}{}
		}
	}

	slog.Info("ResolutionSniper tick end",
		"scanned", scanned,
		"skipped_volume", skippedVolume,
		"skipped_expiry", skippedExpiry,
		"skipped_price", skippedPrice,
		"held", skippedHeld,
		"dead", skippedDead,
		"opened", opened,
	)
	if database := b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "resolution_sniper",
			Scanned:      scanned,
			SkippedPrice: skippedPrice + skippedVolume + skippedExpiry,
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

func minL(a, b int) int {
	if a < b {
		return a
	}
	return b
}
