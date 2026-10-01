package momentumrider

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

type snapshotEntry struct {
	Price  float64
	Volume float64
}

type MomentumRiderStrategy struct {
	deadTokens     map[string]int64
	snapshot       map[string]snapshotEntry
	snapshotTime   time.Time
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("MomentumRider tick error", "error", err)
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
	scanned          int
	skippedNoSnap    int
	skippedPriceMove int
	skippedVolume    int
	skippedHeld      int
	skippedDead      int
	opened           int
}

// run carries all mutable tick state so the extracted scan steps stay
// low-arity methods instead of long parameter lists.
type run struct {
	ctx    context.Context
	b      *broker.Broker
	s      *MomentumRiderStrategy
	config cfg.MomentumRiderConfig
	risk   cfg.RiskConfig
	held   map[string]struct{}
	now    int64
	stats  tickStats
}

// snapshotDecision reports whether tick must return early after the
// snapshot bookkeeping step.
type snapshotDecision int

const (
	continueTick snapshotDecision = iota
	endTick
)

func (s *MomentumRiderStrategy) tick(ctx context.Context, b *broker.Broker) error {
	r := &run{
		ctx:    ctx,
		b:      b,
		s:      s,
		config: b.GetConfig().Strategies.MomentumRider,
		risk:   b.GetConfig().Risk,
		now:    time.Now().Unix(),
	}

	gammaClient := b.GetGamma()
	if gammaClient == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gammaClient.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	currentSnap := buildSnapshot(markets)

	positions, err := b.GetPositionsByStrategy("momentum_rider")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	r.held = make(map[string]struct{}, len(positions))
	for _, p := range positions {
		r.held[p.TokenID] = struct{}{}
	}

	if r.refreshSnapshot(currentSnap) == endTick {
		return nil
	}

	for _, m := range markets {
		if r.scanMarket(m) == stopTick {
			return nil
		}
	}

	r.report()
	return nil
}

// scanMarket applies the market-level shape filter and scans its outcomes.
func (r *run) scanMarket(m gamma.Market) outcomeDecision {
	if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
		return skipOutcome
	}

	r.stats.scanned++

	for i, outcome := range m.Outcomes {
		if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
			break
		}

		if r.tryOutcome(m, i, outcome) == stopTick {
			return stopTick
		}
	}
	return skipOutcome
}

// buildSnapshot captures the current price/volume of every token.
func buildSnapshot(markets []gamma.Market) map[string]snapshotEntry {
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
	return currentSnap
}

// refreshSnapshot installs the initial snapshot or refreshes a stale one,
// returning endTick when the scan must not run this tick.
func (r *run) refreshSnapshot(currentSnap map[string]snapshotEntry) snapshotDecision {
	if len(r.s.snapshot) == 0 {
		r.s.snapshot = currentSnap
		r.s.snapshotTime = time.Now()
		slog.Info("MomentumRider: initial snapshot saved", "tokens", len(currentSnap))
		return endTick
	}

	lookbackDuration := time.Duration(r.config.LookbackMinutes) * time.Minute
	if time.Since(r.s.snapshotTime) > lookbackDuration {
		r.s.snapshot = currentSnap
		r.s.snapshotTime = time.Now()
		slog.Info("MomentumRider: snapshot refreshed")
		return endTick
	}
	return continueTick
}

// outcomeDecision replays the original continue/return-nil control flow.
type outcomeDecision int

const (
	skipOutcome outcomeDecision = iota
	stopTick
)

// tryOutcome applies the outcome-level momentum filters and, when the
// candidate passes, places the buy order.
func (r *run) tryOutcome(m gamma.Market, i int, outcome string) outcomeDecision {
	tokenID := m.TokenIDs[i]
	currentPrice := m.OutcomePrices[i]
	currentVol := m.Volume

	if currentPrice <= 0.05 || currentPrice >= 0.95 {
		return skipOutcome
	}

	if _, h := r.held[tokenID]; h {
		r.stats.skippedHeld++
		return skipOutcome
	}

	if exp, ok := r.s.deadTokens[tokenID]; ok && exp > r.now {
		r.stats.skippedDead++
		return skipOutcome
	}

	prev, hadPrev := r.s.snapshot[tokenID]
	if !hadPrev || prev.Price <= 0 {
		r.stats.skippedNoSnap++
		return skipOutcome
	}

	priceChange := (currentPrice - prev.Price) / prev.Price

	if priceChange < r.config.PriceChangeThreshold {
		r.stats.skippedPriceMove++
		return skipOutcome
	}

	volDelta := currentVol - prev.Volume
	avgVol := prev.Volume
	if avgVol > 0 {
		ratio := volDelta / avgVol
		if ratio < r.config.VolumeSpikeMultiplier {
			r.stats.skippedVolume++
			return skipOutcome
		}
	}

	size := r.kellySize(currentPrice)
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
		Price:    currentPrice,
		SizeUSDC: size,
		TickSize: tickSize,
		NegRisk:  negRisk,
		Strategy: "momentum_rider",
	})
	if buyErr != nil {
		return r.handleBuyError(tokenID, buyErr)
	}

	r.announceOpen(openInfo{Question: question, Outcome: outcome, PriceChange: priceChange,
		VolDelta: volDelta, AvgVol: avgVol, Price: currentPrice, Size: size, Filled: result.FilledSize})
	r.stats.opened++
	r.held[tokenID] = struct{}{}
	return skipOutcome
}

// kellySize computes the momentum-adjusted stake for a price.
func (r *run) kellySize(currentPrice float64) float64 {
	winProb := kelly.EstimateWinProb(currentPrice, "", "", 0)
	winProb += 0.05
	if winProb > 0.95 {
		winProb = 0.95
	}
	return kelly.OptimalSize(kelly.EdgeEstimate{
		WinProb:    winProb,
		Payout:     1 - currentPrice,
		LossAmount: currentPrice,
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
	if isDeadErr(buyErr) {
		r.s.deadTokens[tokenID] = r.now + 3600
		return skipOutcome
	}
	if strings.Contains(buyErr.Error(), "insufficient balance") {
		slog.Warn("MomentumRider: insufficient balance")
		return stopTick
	}
	slog.Error("MomentumRider buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
	return skipOutcome
}

// openInfo bundles the fill details logged/notified after a successful open.
type openInfo struct {
	Question    string
	Outcome     string
	PriceChange float64
	VolDelta    float64
	AvgVol      float64
	Price       float64
	Size        float64
	Filled      float64
}

// announceOpen logs and notifies a successful open, keeping the original
// field names, ordering and message formats.
func (r *run) announceOpen(o openInfo) {
	paperTag := ""
	if r.b.IsPaper() {
		paperTag = " [PAPER]"
	}
	volPctStr := "+0%"
	if o.AvgVol > 0 {
		volPctStr = fmt.Sprintf("%+.0f%%", o.VolDelta/o.AvgVol*100)
	}
	slog.Info("MomentumRider opened",
		"question", o.Question,
		"outcome", o.Outcome,
		"change", fmt.Sprintf("%+.1f%%", o.PriceChange*100),
		"vol_spike", volPctStr,
		"price", o.Price,
		"size", o.Size,
		"filled", o.Filled,
		"paper", paperTag,
	)

	if r.b.GetNotifier() != nil {
		mode := "LIVE"
		if r.b.IsPaper() {
			mode = "PAPER"
		}
		msg := fmt.Sprintf("%s [MomentumRider] %s\n-> %s moved %+.1f%% | vol %s | buying @ %.3f | $%.2f",
			mode, o.Question, o.Outcome, o.PriceChange*100, volPctStr, o.Price, o.Size)
		r.b.Notify(msg)
	}
}

// report emits the tick-end summary log and diagnostic record.
func (r *run) report() {
	slog.Info("MomentumRider tick end",
		"scanned", r.stats.scanned,
		"no_snap", r.stats.skippedNoSnap,
		"price_move", r.stats.skippedPriceMove,
		"volume", r.stats.skippedVolume,
		"held", r.stats.skippedHeld,
		"dead", r.stats.skippedDead,
		"opened", r.stats.opened,
	)
	if database := r.b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "momentum_rider",
			Scanned:      r.stats.scanned,
			SkippedNotOU: r.stats.skippedNoSnap,
			SkippedPrice: r.stats.skippedPriceMove,
			SkippedSport: r.stats.skippedVolume,
			SkippedHeld:  r.stats.skippedHeld,
			SkippedDead:  r.stats.skippedDead,
			Opened:       r.stats.opened,
		})
	}
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
