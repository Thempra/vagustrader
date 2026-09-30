package underhunter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/gamma"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/strategy/kelly"
)

const clobREST = "https://clob.polymarket.com"

type UnderHunterStrategy struct {
	deadTokens map[string]int64
}

func New() *UnderHunterStrategy {
	return &UnderHunterStrategy{deadTokens: make(map[string]int64)}
}

func (s *UnderHunterStrategy) Name() string { return "under_hunter" }

func (s *UnderHunterStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.UnderHunter
	if !config.Enabled {
		slog.Info("UnderHunter disabled")
		return nil
	}
	slog.Info("UnderHunter started", "poll", config.PollIntervalSeconds, "price_range", fmt.Sprintf("%.2f-%.2f", config.MinPrice, config.MaxPrice))

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("UnderHunter tick error", "error", err)
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
	scanned      int
	skippedNotOU int
	skippedPrice int
	skippedSport int
	skippedHeld  int
	skippedDead  int
	opened       int
}

// run carries all mutable tick state so the extracted scan steps stay
// low-arity methods instead of long parameter lists.
type run struct {
	ctx    context.Context
	b      *broker.Broker
	s      *UnderHunterStrategy
	config cfg.UnderHunterConfig
	risk   cfg.RiskConfig
	held   map[string]struct{}
	now    int64
	stats  tickStats
}

func (s *UnderHunterStrategy) tick(ctx context.Context, b *broker.Broker) error {
	r := &run{
		ctx:    ctx,
		b:      b,
		s:      s,
		config: b.GetConfig().Strategies.UnderHunter,
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

	positions, err := b.GetPositionsByStrategy("under_hunter")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	r.held = make(map[string]struct{}, len(positions))
	for _, p := range positions {
		r.held[p.TokenID] = struct{}{}
	}

	for _, m := range markets {
		if !isOverUnderMarket(m.Outcomes) {
			r.stats.skippedNotOU++
			continue
		}
		r.stats.scanned++

		for i, outcome := range m.Outcomes {
			if r.tryOutcome(m, i, outcome) == stopTick {
				return nil
			}
		}
	}

	r.report()
	return nil
}

// isOverUnderMarket reports whether any outcome is an Over/Under line.
func isOverUnderMarket(outcomes []string) bool {
	for _, o := range outcomes {
		if strings.EqualFold(o, "Under") || strings.EqualFold(o, "Over") {
			return true
		}
	}
	return false
}

// outcomeDecision replays the original continue/return-nil control flow.
type outcomeDecision int

const (
	skipOutcome outcomeDecision = iota
	stopTick
)

// tryOutcome applies the outcome-level filters and, when the candidate
// passes, places the buy order on the Under side.
func (r *run) tryOutcome(m gamma.Market, i int, outcome string) outcomeDecision {
	if !strings.EqualFold(outcome, "Under") {
		return skipOutcome
	}

	if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
		return skipOutcome
	}

	price := m.OutcomePrices[i]
	tokenID := m.TokenIDs[i]

	if price < r.config.MinPrice || price > r.config.MaxPrice {
		r.stats.skippedPrice++
		return skipOutcome
	}

	sportPrefix := detectSportPrefix(m.Slug)
	if !isAllowedSport(sportPrefix, r.config.AllowedSports) {
		r.stats.skippedSport++
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

	winProb := kelly.EstimateWinProb(price, sportPrefix, "under", 0)
	size := r.kellySize(price, winProb)
	if size <= 0 {
		return skipOutcome
	}

	tickSize, negRisk := r.orderBookParams(m.ConditionID)

	result, buyErr := r.b.Buy(r.ctx, broker.BuyRequest{
		TokenID:  tokenID,
		MarketID: m.ConditionID,
		Outcome:  "Under",
		Price:    price,
		SizeUSDC: size,
		TickSize: tickSize,
		NegRisk:  negRisk,
		Strategy: "under_hunter",
	})
	if buyErr != nil {
		return r.handleBuyError(tokenID, buyErr)
	}

	r.announceOpen(m.Question, price, size, result.FilledSize, sportPrefix, winProb)
	r.stats.opened++
	r.held[tokenID] = struct{}{}
	return skipOutcome
}

// kellySize computes the stake for a win probability. P2.1:
// prediction-market payoff. A winning share pays $1.00 regardless of entry
// price, so the per-share gain is (1 - price) and the per-share loss is the
// entry price. Previously we passed Payout=price, LossAmount=price which made
// Kelly degenerate to even-money (b=1) and under-bet every longshot.
func (r *run) kellySize(price, winProb float64) float64 {
	return kelly.OptimalSize(kelly.EdgeEstimate{
		WinProb:    winProb,
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
	if isDeadErr(buyErr) {
		r.s.deadTokens[tokenID] = r.now + 3600
		return skipOutcome
	}
	if strings.Contains(buyErr.Error(), "insufficient balance") {
		slog.Warn("UnderHunter: insufficient balance")
		return stopTick
	}
	slog.Error("UnderHunter buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
	return skipOutcome
}

// announceOpen logs and notifies a successful open, keeping the original
// field names, ordering and message formats.
func (r *run) announceOpen(question string, price, size, filled float64, sportPrefix string, winProb float64) {
	if len(question) > 60 {
		question = question[:60]
	}
	paperTag := ""
	if r.b.IsPaper() {
		paperTag = " [PAPER]"
	}
	slog.Info("UnderHunter opened",
		"question", question,
		"price", price,
		"size", size,
		"filled", filled,
		"sport", sportPrefix,
		"winProb", fmt.Sprintf("%.0f%%", winProb*100),
		"paper", paperTag,
	)

	if r.b.GetNotifier() != nil {
		mode := "LIVE"
		if r.b.IsPaper() {
			mode = "PAPER"
		}
		msg := fmt.Sprintf("%s [UnderHunter] %s\n-> Under @ %.3f | $%.2f | est %.0f%% win",
			mode, question, price, size, winProb*100)
		r.b.Notify(msg)
	}
}

// report emits the tick-end summary log and diagnostic record.
func (r *run) report() {
	slog.Info("UnderHunter tick end",
		"scanned", r.stats.scanned,
		"not_ou", r.stats.skippedNotOU,
		"price", r.stats.skippedPrice,
		"sport", r.stats.skippedSport,
		"held", r.stats.skippedHeld,
		"dead", r.stats.skippedDead,
		"opened", r.stats.opened,
	)
	if database := r.b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "under_hunter",
			Scanned:      r.stats.scanned,
			SkippedNotOU: r.stats.skippedNotOU,
			SkippedPrice: r.stats.skippedPrice,
			SkippedSport: r.stats.skippedSport,
			SkippedHeld:  r.stats.skippedHeld,
			SkippedDead:  r.stats.skippedDead,
			Opened:       r.stats.opened,
		})
	}
}

func detectSportPrefix(slug string) string {
	if slug == "" {
		return ""
	}
	lower := strings.ToLower(slug)
	dashIdx := strings.Index(lower, "-")
	if dashIdx > 0 {
		return lower[:dashIdx]
	}
	return ""
}

func isAllowedSport(sport string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if strings.EqualFold(a, sport) {
			return true
		}
	}
	return false
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

var _ sportChecker = (*UnderHunterStrategy)(nil)

type sportChecker interface{ Name() string }

func init() {
	_ = math.Round
	_ = cfg.SizeTier{}
}
