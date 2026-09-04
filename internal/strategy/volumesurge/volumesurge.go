package volumesurge

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

type volumeSnapshot struct {
	Volume float64
	Price  float64
}

type VolumeSurgeStrategy struct {
	deadTokens        map[string]int64
	baseline          map[string]volumeSnapshot
	lastSnapshot      time.Time
}

func New() *VolumeSurgeStrategy {
	return &VolumeSurgeStrategy{
		deadTokens:   make(map[string]int64),
		baseline:     make(map[string]volumeSnapshot),
		lastSnapshot: time.Now(),
	}
}

func (s *VolumeSurgeStrategy) Name() string { return "volume_surge" }

func (s *VolumeSurgeStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.VolumeSurge
	if !config.Enabled {
		slog.Info("VolumeSurge disabled")
		return nil
	}
	slog.Info("VolumeSurge started",
		"poll", config.PollIntervalSeconds,
		"surge_mult", config.SurgeMultiplier,
		"lookback", config.LookbackMinutes,
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("VolumeSurge tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
	}
}

func (s *VolumeSurgeStrategy) tick(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.VolumeSurge
	risk := b.GetConfig().Risk

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	// Update baseline every lookback interval
	now := time.Now()
	baselineAge := now.Sub(s.lastSnapshot)
	if baselineAge > time.Duration(config.LookbackMinutes)*time.Minute || len(s.baseline) == 0 {
		s.baseline = make(map[string]volumeSnapshot, len(markets))
		for _, m := range markets {
			s.baseline[m.ConditionID] = volumeSnapshot{
				Volume: m.Volume,
				Price:  m.OutcomePrices[0],
			}
		}
		s.lastSnapshot = now
		slog.Debug("VolumeSurge baseline updated", "markets", len(s.baseline))
		return nil // First tick is just baseline
	}

	positions, err := b.GetPositionsByStrategy("volume_surge")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	var scanned, skippedLowVol, skippedNoBaseline, skippedSurge, skippedHeld, skippedDead, opened int
	deadNow := now.Unix()

	for _, m := range markets {
		if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
			continue
		}

		baseline, ok := s.baseline[m.ConditionID]
		if !ok {
			skippedNoBaseline++
			continue
		}

		// Skip if baseline volume was too low
		if baseline.Volume < config.MinBaselineVolume {
			skippedLowVol++
			continue
		}

		scanned++

		// Calculate volume surge ratio
		volumeRatio := 1.0
		if baseline.Volume > 0 {
			volumeRatio = m.Volume / baseline.Volume
		}

		if volumeRatio < config.SurgeMultiplier {
			skippedSurge++
			continue
		}

		// Determine direction: which outcome price moved most from baseline
		for i, outcome := range m.Outcomes {
			if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
				break
			}

			tokenID := m.TokenIDs[i]
			price := m.OutcomePrices[i]

			if price <= 0 || price >= 1 {
				continue
			}

			// Only consider the side that moved in the direction of the surge
			baselinePrice := 0.5
			if i == 0 {
				baselinePrice = baseline.Price
			}

			priceChange := price - baselinePrice

			// Skip if not enough price movement direction
			if priceChange < config.MinPriceMove && priceChange > -config.MinPriceMove {
				continue
			}

			if _, h := held[tokenID]; h {
				skippedHeld++
				continue
			}

			if exp, ok := s.deadTokens[tokenID]; ok && exp > deadNow {
				skippedDead++
				continue
			}

			// Price moved up -> momentum is up -> buy YES (or the outcome that went up)
			// This strategy follows the momentum
			winProb := price
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
				Strategy: "volume_surge",
			})
			if buyErr != nil {
				if isDead(buyErr) {
					s.deadTokens[tokenID] = deadNow + 3600
				} else if strings.Contains(buyErr.Error(), "insufficient balance") {
					slog.Warn("VolumeSurge: insufficient balance")
					return nil
				} else {
					slog.Error("VolumeSurge buy error", "token", tokenID[:minLen(12, len(tokenID))], "error", buyErr)
				}
				continue
			}

			paperTag := ""
			if b.IsPaper() {
				paperTag = " [PAPER]"
			}
			slog.Info("VolumeSurge opened",
				"question", question,
				"outcome", outcome,
				"price", price,
				"size", size,
				"filled", result.FilledSize,
				"volume_surge", fmt.Sprintf("%.1fx", volumeRatio),
				"paper", paperTag,
			)

			if b.GetNotifier() != nil {
				mode := "LIVE"
				if b.IsPaper() {
					mode = "PAPER"
				}
				msg := fmt.Sprintf("%s [VolumeSurge] %s\n-> %s @ %.3f | $%.2f | surge %.1fx",
					mode, question, outcome, price, size, volumeRatio)
				b.Notify(msg)
			}

			opened++
			held[tokenID] = struct{}{}
		}
	}

	// Refresh baseline periodically even if not full interval
	age := now.Sub(s.lastSnapshot)
	if age > time.Duration(config.LookbackMinutes/2)*time.Minute {
		for _, m := range markets {
			if _, exists := s.baseline[m.ConditionID]; exists {
				entry := s.baseline[m.ConditionID]
				entry.Volume = m.Volume
				s.baseline[m.ConditionID] = entry
			}
		}
	}

	slog.Info("VolumeSurge tick end",
		"scanned", scanned,
		"skipped_low_vol", skippedLowVol,
		"skipped_no_baseline", skippedNoBaseline,
		"skipped_surge", skippedSurge,
		"held", skippedHeld,
		"dead", skippedDead,
		"opened", opened,
	)
	if database := b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "volume_surge",
			Scanned:      scanned,
			SkippedPrice: skippedLowVol + skippedNoBaseline + skippedSurge,
			SkippedHeld:  skippedHeld,
			SkippedDead:  skippedDead,
			Opened:       opened,
		})
	}
	return nil
}

func isDead(err error) bool {
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
