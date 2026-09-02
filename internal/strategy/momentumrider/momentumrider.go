package momentumrider

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

type snapshotEntry struct {
	Price  float64
	Volume float64
}

type MomentumRiderStrategy struct {
	deadTokens    map[string]int64
	snapshot      map[string]snapshotEntry
	snapshotTime  time.Time
	baselineVolume map[string]float64
}

func New() *MomentumRiderStrategy {
	return &MomentumRiderStrategy{
		deadTokens:     make(map[string]int64),
		snapshot:       make(map[string]snapshotEntry),
		baselineVolume: make(map[string]float64),
	}
}

func (s *MomentumRiderStrategy) Name() string { return "momentum_rider" }

func (s *MomentumRiderStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.MomentumRider
	if !config.Enabled {
		slog.Info("MomentumRider disabled")
		return nil
	}
	slog.Info("MomentumRider started",
		"poll", config.PollIntervalSeconds,
		"price_threshold", config.PriceChangeThreshold,
		"volume_mult", config.VolumeSpikeMultiplier,
	)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("MomentumRider tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
	}
}

func (s *MomentumRiderStrategy) tick(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.MomentumRider
	risk := b.GetConfig().Risk

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	currentSnap := make(map[string]snapshotEntry)
	for _, m := range markets {
		for i, tokenID := range m.TokenIDs {
			if i < len(m.OutcomePrices) {
				currentSnap[tokenID] = snapshotEntry{
					Price:  m.OutcomePrices[i],
					Volume: m.Volume,
				}
			}
		}
	}

	positions, err := b.GetPositionsByStrategy("momentum_rider")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	now := time.Now().Unix()
	var scanned, skippedNoSnap, skippedPriceMove, skippedVolume, skippedHeld, skippedDead, opened int

	if len(s.snapshot) == 0 {
		s.snapshot = currentSnap
		s.snapshotTime = time.Now()
		slog.Info("MomentumRider: initial snapshot saved", "tokens", len(currentSnap))
		return nil
	}

	lookbackDuration := time.Duration(config.LookbackMinutes) * time.Minute
	if time.Since(s.snapshotTime) > lookbackDuration {
		s.snapshot = currentSnap
		s.snapshotTime = time.Now()
		slog.Info("MomentumRider: snapshot refreshed")
		return nil
	}

	for _, m := range markets {
		if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
			continue
		}

		scanned++

		for i, outcome := range m.Outcomes {
			if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
				break
			}

			tokenID := m.TokenIDs[i]
			currentPrice := m.OutcomePrices[i]
			currentVol := m.Volume

			if currentPrice <= 0.05 || currentPrice >= 0.95 {
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

			prev, hadPrev := s.snapshot[tokenID]
			if !hadPrev || prev.Price <= 0 {
				skippedNoSnap++
				continue
			}

			priceChange := (currentPrice - prev.Price) / prev.Price

			if priceChange < config.PriceChangeThreshold {
				skippedPriceMove++
				continue
			}

			volDelta := currentVol - prev.Volume
			avgVol := prev.Volume
			if avgVol > 0 {
				ratio := volDelta / avgVol
				if ratio < config.VolumeSpikeMultiplier {
					skippedVolume++
					continue
				}
			}

			winProb := kelly.EstimateWinProb(currentPrice, "", "", 0)
			winProb += 0.05
			if winProb > 0.95 {
				winProb = 0.95
			}

			size := kelly.OptimalSize(kelly.EdgeEstimate{
				WinProb:    winProb,
				Payout:     1 - currentPrice,
				LossAmount: currentPrice,
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
				Price:    currentPrice,
				SizeUSDC: size,
				TickSize: tickSize,
				NegRisk:  negRisk,
				Strategy: "momentum_rider",
			})
			if buyErr != nil {
				if isDeadErr(buyErr) {
					s.deadTokens[tokenID] = now + 3600
				} else if strings.Contains(buyErr.Error(), "insufficient balance") {
					slog.Warn("MomentumRider: insufficient balance")
					return nil
				} else {
					slog.Error("MomentumRider buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
				}
				continue
			}

			paperTag := ""
			if b.IsPaper() {
				paperTag = " [PAPER]"
			}
			volPctStr := "+0%"
			if avgVol > 0 {
				volPctStr = fmt.Sprintf("%+.0f%%", volDelta/avgVol*100)
			}
			slog.Info("MomentumRider opened",
				"question", question,
				"outcome", outcome,
				"change", fmt.Sprintf("%+.1f%%", priceChange*100),
				"vol_spike", volPctStr,
				"price", currentPrice,
				"size", size,
				"filled", result.FilledSize,
				"paper", paperTag,
			)

			if b.GetNotifier() != nil {
				mode := "LIVE"
				if b.IsPaper() {
					mode = "PAPER"
				}
				msg := fmt.Sprintf("%s [MomentumRider] %s\n-> %s moved %+.1f%% | vol %s | buying @ %.3f | $%.2f",
					mode, question, outcome, priceChange*100, volPctStr, currentPrice, size)
				b.Notify(msg)
			}

			opened++
			held[tokenID] = struct{}{}
		}
	}

	slog.Info("MomentumRider tick end",
		"scanned", scanned,
		"no_snap", skippedNoSnap,
		"price_move", skippedPriceMove,
		"volume", skippedVolume,
		"held", skippedHeld,
		"dead", skippedDead,
		"opened", opened,
	)
	if database := b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "momentum_rider",
			Scanned:      scanned,
			SkippedNotOU: skippedNoSnap,
			SkippedPrice: skippedPriceMove,
			SkippedSport: skippedVolume,
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
