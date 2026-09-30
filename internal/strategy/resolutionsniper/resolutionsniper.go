package resolutionsniper

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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("ResolutionSniper tick error", "error", err)
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
	scanned       int
	skippedPrice  int
	skippedVolume int
	skippedExpiry int
	skippedHeld   int
	skippedDead   int
	opened        int
}

// run carries all mutable tick state so the extracted scan steps stay
// low-arity methods instead of long parameter lists.
type run struct {
	ctx    context.Context
	b      *broker.Broker
	s      *ResolutionSniperStrategy
	config cfg.ResolutionSniperConfig
	risk   cfg.RiskConfig
	held   map[string]struct{}
	now    int64
	cutoff time.Time
	stats  tickStats
}

func (s *ResolutionSniperStrategy) tick(ctx context.Context, b *broker.Broker) error {
	r := &run{
		ctx:    ctx,
		b:      b,
		s:      s,
		config: b.GetConfig().Strategies.ResolutionSniper,
		risk:   b.GetConfig().Risk,
		now:    time.Now().Unix(),
		cutoff: time.Now().Add(time.Duration(b.GetConfig().Strategies.ResolutionSniper.MaxDaysToEnd) * 24 * time.Hour),
	}

	gammaClient := b.GetGamma()
	if gammaClient == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gammaClient.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	positions, err := b.GetPositionsByStrategy("resolution_sniper")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	r.held = make(map[string]struct{}, len(positions))
	for _, p := range positions {
		r.held[p.TokenID] = struct{}{}
	}

	for _, m := range markets {
		if r.scanMarket(m) {
			return nil
		}
	}

	r.report()
	return nil
}

// scanMarket applies the market-level filters and scans its outcomes. It
// returns true when the whole tick must stop (insufficient balance).
func (r *run) scanMarket(m gamma.Market) bool {
	if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
		return false
	}

	if m.Volume < r.config.MinVolume {
		r.stats.skippedVolume++
		return false
	}

	endDate := parseEndDate(m.EndDate)
	if endDateOutsideWindow(endDate, r.cutoff) {
		r.stats.skippedExpiry++
		return false
	}

	r.stats.scanned++

	for i, outcome := range m.Outcomes {
		if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
			break
		}
		if r.tryOutcome(m, i, outcome, endDate) == stopTick {
			return true
		}
	}
	return false
}

// endDateOutsideWindow reports whether the market end date fails the expiry
// filters: unparseable/missing, beyond the cutoff, or already past.
func endDateOutsideWindow(endDate, cutoff time.Time) bool {
	return endDate.IsZero() || endDate.After(cutoff) || endDate.Before(time.Now())
}

// parseEndDate parses the RFC3339 market end date, returning the zero time on
// empty or malformed input (same as the original inline parse-and-ignore).
func parseEndDate(endDate string) time.Time {
	if endDate == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339, endDate)
	return t
}

// outcomeDecision replays the original continue/return-nil control flow.
type outcomeDecision int

const (
	skipOutcome outcomeDecision = iota
	stopTick
)

// tryOutcome applies the outcome-level filters and, when the candidate
// passes, places the buy order.
func (r *run) tryOutcome(m gamma.Market, i int, outcome string, endDate time.Time) outcomeDecision {
	tokenID := m.TokenIDs[i]
	price := m.OutcomePrices[i]

	if price < r.config.MinPrice || price > r.config.MaxPrice {
		r.stats.skippedPrice++
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

	size := kelly.OptimalSize(kelly.EdgeEstimate{
		WinProb:    price,
		Payout:     1 - price,
		LossAmount: price,
	}, r.risk.MaxPositionUSDC*10, r.config.MaxPosition, r.config.KellyFraction)

	if size <= 0 {
		return skipOutcome
	}

	tickSize, negRisk := r.orderBookParams(m.ConditionID)

	result, buyErr := r.b.Buy(r.ctx, broker.BuyRequest{
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
		return r.handleBuyError(tokenID, buyErr)
	}

	r.announceOpen(m.Question, outcome, price, size, result.FilledSize, endDate)
	r.stats.opened++
	r.held[tokenID] = struct{}{}
	return skipOutcome
}

// orderBookParams reads tick size and neg-risk flags from a single market
// data fetch, mirroring the original inline lookup.
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
		slog.Warn("ResolutionSniper: insufficient balance")
		return stopTick
	}
	slog.Error("ResolutionSniper buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
	return skipOutcome
}

// announceOpen logs and notifies a successful open, keeping the original
// field names, ordering and message formats.
func (r *run) announceOpen(question, outcome string, price, size, filled float64, endDate time.Time) {
	if len(question) > 60 {
		question = question[:60]
	}
	paperTag := ""
	if r.b.IsPaper() {
		paperTag = " [PAPER]"
	}
	daysLeft := int(endDate.Sub(time.Now()).Hours() / 24)
	slog.Info("ResolutionSniper opened",
		"question", question,
		"outcome", outcome,
		"price", price,
		"size", size,
		"filled", filled,
		"days_left", daysLeft,
		"paper", paperTag,
	)

	if r.b.GetNotifier() != nil {
		mode := "LIVE"
		if r.b.IsPaper() {
			mode = "PAPER"
		}
		msg := fmt.Sprintf("%s [ResolutionSniper] %s\n-> %s @ %.3f | $%.2f | resolves in %dd",
			mode, question, outcome, price, size, daysLeft)
		r.b.Notify(msg)
	}
}

// report emits the tick-end summary log and diagnostic record.
func (r *run) report() {
	slog.Info("ResolutionSniper tick end",
		"scanned", r.stats.scanned,
		"skipped_volume", r.stats.skippedVolume,
		"skipped_expiry", r.stats.skippedExpiry,
		"skipped_price", r.stats.skippedPrice,
		"held", r.stats.skippedHeld,
		"dead", r.stats.skippedDead,
		"opened", r.stats.opened,
	)
	if database := r.b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "resolution_sniper",
			Scanned:      r.stats.scanned,
			SkippedPrice: r.stats.skippedPrice + r.stats.skippedVolume + r.stats.skippedExpiry,
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
