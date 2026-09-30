package volumesurge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/gamma"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/strategy/kelly"
)

type volumeSnapshot struct {
	Volume float64
	Price  float64
}

type VolumeSurgeStrategy struct {
	deadTokens   map[string]int64
	baseline     map[string]volumeSnapshot
	lastSnapshot time.Time
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("VolumeSurge tick error", "error", err)
		}
		if err := waitOrDone(ctx, time.Duration(config.PollIntervalSeconds)*time.Second); err != nil {
			return err
		}
	}
}

// waitOrDone blocks for d and returns ctx.Err() if ctx is cancelled first,
// or nil when the full duration elapsed. Semantically identical to a
// select over ctx.Done() against time.After(d).
func waitOrDone(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// tickStats holds the per-tick scan counters.
type tickStats struct {
	scanned           int
	skippedLowVol     int
	skippedNoBaseline int
	skippedSurge      int
	skippedHeld       int
	skippedDead       int
	opened            int
}

// run carries all mutable tick state so the extracted scan steps stay
// low-arity methods instead of long parameter lists.
type run struct {
	ctx    context.Context
	b      *broker.Broker
	s      *VolumeSurgeStrategy
	config cfg.VolumeSurgeConfig
	risk   cfg.RiskConfig
	held   map[string]struct{}
	now    time.Time
	dead   int64
	stats  tickStats
}

// baselineDecision reports whether tick must return early after the
// baseline-refresh step.
type baselineDecision int

const (
	continueTick baselineDecision = iota
	endTick
)

func (s *VolumeSurgeStrategy) tick(ctx context.Context, b *broker.Broker) error {
	r := &run{
		ctx:    ctx,
		b:      b,
		s:      s,
		config: b.GetConfig().Strategies.VolumeSurge,
		risk:   b.GetConfig().Risk,
		now:    time.Now(),
	}

	gammaClient := b.GetGamma()
	if gammaClient == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gammaClient.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	// Update baseline every lookback interval
	if r.refreshBaseline(markets) == endTick {
		return nil // First tick is just baseline
	}

	positions, err := b.GetPositionsByStrategy("volume_surge")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	r.held = make(map[string]struct{}, len(positions))
	for _, p := range positions {
		r.held[p.TokenID] = struct{}{}
	}
	r.dead = r.now.Unix()

	for _, m := range markets {
		if r.scanMarket(m) == stopTick {
			return nil
		}
	}

	// Refresh baseline periodically even if not full interval
	r.refreshExistingBaselines(markets)

	r.report()
	return nil
}

// scanMarket applies the market-level surge filters and scans its outcomes.
func (r *run) scanMarket(m gamma.Market) outcomeDecision {
	if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
		return skipOutcome
	}

	baseline, ok := r.s.baseline[m.ConditionID]
	if !ok {
		r.stats.skippedNoBaseline++
		return skipOutcome
	}

	// Skip if baseline volume was too low
	if baseline.Volume < r.config.MinBaselineVolume {
		r.stats.skippedLowVol++
		return skipOutcome
	}

	r.stats.scanned++

	// Calculate volume surge ratio
	volumeRatio := 1.0
	if baseline.Volume > 0 {
		volumeRatio = m.Volume / baseline.Volume
	}

	if volumeRatio < r.config.SurgeMultiplier {
		r.stats.skippedSurge++
		return skipOutcome
	}

	// Determine direction: which outcome price moved most from baseline
	for i, outcome := range m.Outcomes {
		if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
			break
		}

		if r.tryOutcome(m, i, outcome, baseline, volumeRatio) == stopTick {
			return stopTick
		}
	}
	return skipOutcome
}

// refreshBaseline rebuilds the full baseline when it is stale or empty,
// returning endTick when the scan must not run this tick.
func (r *run) refreshBaseline(markets []gamma.Market) baselineDecision {
	baselineAge := r.now.Sub(r.s.lastSnapshot)
	if baselineAge > time.Duration(r.config.LookbackMinutes)*time.Minute || len(r.s.baseline) == 0 {
		r.s.baseline = make(map[string]volumeSnapshot, len(markets))
		for _, m := range markets {
			r.s.baseline[m.ConditionID] = volumeSnapshot{
				Volume: m.Volume,
				Price:  m.OutcomePrices[0],
			}
		}
		r.s.lastSnapshot = r.now
		slog.Debug("VolumeSurge baseline updated", "markets", len(r.s.baseline))
		return endTick
	}
	return continueTick
}

// refreshExistingBaselines updates the stored volume of known markets once
// half a lookback interval has elapsed.
func (r *run) refreshExistingBaselines(markets []gamma.Market) {
	age := r.now.Sub(r.s.lastSnapshot)
	if age > time.Duration(r.config.LookbackMinutes/2)*time.Minute {
		for _, m := range markets {
			if _, exists := r.s.baseline[m.ConditionID]; exists {
				entry := r.s.baseline[m.ConditionID]
				entry.Volume = m.Volume
				r.s.baseline[m.ConditionID] = entry
			}
		}
	}
}

// outcomeDecision replays the original continue/return-nil control flow.
type outcomeDecision int

const (
	skipOutcome outcomeDecision = iota
	stopTick
)

// tryOutcome applies the outcome-level surge filters and, when the
// candidate passes, places the buy order on the surging side.
func (r *run) tryOutcome(m gamma.Market, i int, outcome string, baseline volumeSnapshot, volumeRatio float64) outcomeDecision {
	tokenID := m.TokenIDs[i]
	price := m.OutcomePrices[i]

	if price <= 0 || price >= 1 {
		return skipOutcome
	}

	// Only consider the side that moved in the direction of the surge
	baselinePrice := 0.5
	if i == 0 {
		baselinePrice = baseline.Price
	}

	priceChange := price - baselinePrice

	// Skip if not enough price movement direction
	if priceChange < r.config.MinPriceMove && priceChange > -r.config.MinPriceMove {
		return skipOutcome
	}

	if _, h := r.held[tokenID]; h {
		r.stats.skippedHeld++
		return skipOutcome
	}

	if exp, ok := r.s.deadTokens[tokenID]; ok && exp > r.dead {
		r.stats.skippedDead++
		return skipOutcome
	}

	size := r.kellySize(price)
	if size <= 0 {
		return skipOutcome
	}

	tickSize, negRisk := r.orderBookParams(m.ConditionID)

	question := m.Question
	if len(question) > 60 {
		question = question[:60]
	}

	result, buyErr := r.b.Buy(r.ctx, broker.BuyRequest{
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
		return r.handleBuyError(tokenID, buyErr)
	}

	r.announceOpen(question, outcome, price, size, result.FilledSize, volumeRatio)
	r.stats.opened++
	r.held[tokenID] = struct{}{}
	return skipOutcome
}

// kellySize computes the stake. Price moved up -> momentum is up -> buy YES
// (or the outcome that went up). This strategy follows the momentum.
func (r *run) kellySize(price float64) float64 {
	return kelly.OptimalSize(kelly.EdgeEstimate{
		WinProb:    price,
		Payout:     1 - price,
		LossAmount: price,
	}, r.risk.MaxPositionUSDC*10, r.config.MaxPosition, r.config.KellyFraction)
}

// orderBookParams reads tick size and neg-risk flags from a single market
// data fetch, mirroring the original inline lookups.
func (r *run) orderBookParams(conditionID string) (string, bool) {
	tickSize := "0.01"
	negRisk := false
	mktData := r.b.GetMarketData(conditionID)
	if mktData != nil {
		if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
			tickSize = ts
		}
		negRisk, _ = mktData["neg_risk"].(bool)
	}
	return tickSize, negRisk
}

// handleBuyError classifies a failed buy exactly as the original inline
// if/else-if chain did: dead orderbook marks the token dead, insufficient
// balance aborts the whole tick, anything else just logs.
func (r *run) handleBuyError(tokenID string, buyErr error) outcomeDecision {
	if isDead(buyErr) {
		r.s.deadTokens[tokenID] = r.dead + 3600
		return skipOutcome
	}
	if strings.Contains(buyErr.Error(), "insufficient balance") {
		slog.Warn("VolumeSurge: insufficient balance")
		return stopTick
	}
	slog.Error("VolumeSurge buy error", "token", tokenID[:minLen(12, len(tokenID))], "error", buyErr)
	return skipOutcome
}

// announceOpen logs and notifies a successful open, keeping the original
// field names, ordering and message formats.
func (r *run) announceOpen(question, outcome string, price, size, filled, volumeRatio float64) {
	paperTag := ""
	if r.b.IsPaper() {
		paperTag = " [PAPER]"
	}
	slog.Info("VolumeSurge opened",
		"question", question,
		"outcome", outcome,
		"price", price,
		"size", size,
		"filled", filled,
		"volume_surge", fmt.Sprintf("%.1fx", volumeRatio),
		"paper", paperTag,
	)

	if r.b.GetNotifier() != nil {
		mode := "LIVE"
		if r.b.IsPaper() {
			mode = "PAPER"
		}
		msg := fmt.Sprintf("%s [VolumeSurge] %s\n-> %s @ %.3f | $%.2f | surge %.1fx",
			mode, question, outcome, price, size, volumeRatio)
		r.b.Notify(msg)
	}
}

// report emits the tick-end summary log and diagnostic record.
func (r *run) report() {
	slog.Info("VolumeSurge tick end",
		"scanned", r.stats.scanned,
		"skipped_low_vol", r.stats.skippedLowVol,
		"skipped_no_baseline", r.stats.skippedNoBaseline,
		"skipped_surge", r.stats.skippedSurge,
		"held", r.stats.skippedHeld,
		"dead", r.stats.skippedDead,
		"opened", r.stats.opened,
	)
	if database := r.b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "volume_surge",
			Scanned:      r.stats.scanned,
			SkippedPrice: r.stats.skippedLowVol + r.stats.skippedNoBaseline + r.stats.skippedSurge,
			SkippedHeld:  r.stats.skippedHeld,
			SkippedDead:  r.stats.skippedDead,
			Opened:       r.stats.opened,
		})
	}
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
