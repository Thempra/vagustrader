package deepvalue

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
		if err := waitOrDone(ctx, config.PollIntervalSeconds); err != nil {
			return err
		}
	}
}

// waitOrDone sleeps for pollSeconds seconds, returning ctx.Err() early if the
// context is cancelled first.
func waitOrDone(ctx context.Context, pollSeconds int) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Duration(pollSeconds) * time.Second):
		return nil
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

	var stats dvStats
	now := time.Now().Unix()

	for _, m := range markets {
		if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
			continue
		}

		// Filter by minimum volume
		if m.Volume < config.MinVolume {
			stats.skippedVolume++
			continue
		}

		stats.scanned++

		stop, err := s.scanOutcomes(ctx, b, m, held, config, risk, now, &stats)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}

	slog.Info("DeepValue tick end",
		"scanned", stats.scanned,
		"skipped_volume", stats.skippedVolume,
		"skipped_price", stats.skippedPrice,
		"held", stats.skippedHeld,
		"dead", stats.skippedDead,
		"opened", stats.opened,
	)
	recordDVDiagnostic(b, &stats)
	return nil
}

// dvStats carries the per-tick funnel counters.
type dvStats struct {
	scanned       int
	skippedVolume int
	skippedPrice  int
	skippedHeld   int
	skippedDead   int
	opened        int
}

// recordSkip bumps the counter matching why an outcome was filtered out.
func (st *dvStats) recordSkip(kind dvSkipKind) {
	switch kind {
	case dvSkipPrice:
		st.skippedPrice++
	case dvSkipHeld:
		st.skippedHeld++
	case dvSkipDead:
		st.skippedDead++
	}
}

// scanOutcomes evaluates every priced outcome of one market exactly as the
// original inline loop did, updating stats. stop is true when the broker
// reported insufficient balance and the whole tick must end without error.
func (s *DeepValueStrategy) scanOutcomes(ctx context.Context, b *broker.Broker, m gamma.Market, held map[string]struct{}, config cfg.DeepValueConfig, risk cfg.RiskConfig, now int64, stats *dvStats) (stop bool, err error) {
	for i, outcome := range m.Outcomes {
		if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
			break
		}

		tokenID := m.TokenIDs[i]
		price := m.OutcomePrices[i]

		if skip, stat := s.outcomeFilter(held, tokenID, price, config.MaxPrice, now); skip {
			stats.recordSkip(stat)
			continue
		}

		size := kelly.OptimalSize(kelly.EdgeEstimate{
			WinProb:    price,
			Payout:     1 - price,
			LossAmount: price,
		}, risk.MaxPositionUSDC*10, config.MaxPosition, config.KellyFraction)

		if size <= 0 {
			continue
		}

		stop, opened := s.tryBuy(ctx, b, m, outcome, tokenID, price, size, now)
		if stop {
			return true, nil
		}
		if opened {
			stats.opened++
			held[tokenID] = struct{}{}
		}
	}
	return false, nil
}

// tryBuy places one deep-value buy order. stop is true on insufficient
// balance (the tick must end without error); opened is true when the position
// was filled and logged, exactly like the original inline flow.
func (s *DeepValueStrategy) tryBuy(ctx context.Context, b *broker.Broker, m gamma.Market, outcome, tokenID string, price, size float64, now int64) (stop, opened bool) {
	result, buyErr := b.Buy(ctx, broker.BuyRequest{
		TokenID:  tokenID,
		MarketID: m.ConditionID,
		Outcome:  outcome,
		Price:    price,
		SizeUSDC: size,
		TickSize: tickSizeOf(b, m.ConditionID),
		NegRisk:  negRiskOf(b, m.ConditionID),
		Strategy: "deep_value",
	})
	if buyErr != nil {
		if isDeadErr(buyErr) {
			s.deadTokens[tokenID] = now + 3600
		} else if strings.Contains(buyErr.Error(), "insufficient balance") {
			slog.Warn("DeepValue: insufficient balance")
			return true, false
		} else {
			slog.Error("DeepValue buy error", "token", tokenID[:minLen(12, len(tokenID))], "error", buyErr)
		}
		return false, false
	}

	announceOpen(b, m.Question, outcome, price, size, result.FilledSize)
	return false, true
}

// recordDVDiagnostic stores the per-tick funnel counters, best-effort.
func recordDVDiagnostic(b *broker.Broker, stats *dvStats) {
	if database := b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "deep_value",
			Scanned:      stats.scanned,
			SkippedPrice: stats.skippedPrice + stats.skippedVolume,
			SkippedHeld:  stats.skippedHeld,
			SkippedDead:  stats.skippedDead,
			Opened:       stats.opened,
		})
	}
}

// dvSkipKind names why an outcome was filtered out before sizing.
type dvSkipKind int

const (
	dvSkipNone  dvSkipKind = iota // outcome passed the filters
	dvSkipPrice                   // price outside the value-bet range
	dvSkipHeld                    // token already open
	dvSkipDead                    // token temporarily blacklisted
)

// outcomeFilter replays the original guard switch: Only consider very cheap
// tokens (value bets), then held/dead checks, reporting which counter to bump.
func (s *DeepValueStrategy) outcomeFilter(held map[string]struct{}, tokenID string, price, maxPrice float64, now int64) (skip bool, kind dvSkipKind) {
	if price <= 0 || price > maxPrice {
		return true, dvSkipPrice
	}
	if s.isHeldOrDead(held, tokenID, now) {
		if _, h := held[tokenID]; h {
			return true, dvSkipHeld
		}
		return true, dvSkipDead
	}
	return false, dvSkipNone
}

// isHeldOrDead reports whether the token is already held or temporarily
// blacklisted as a dead orderbook.
func (s *DeepValueStrategy) isHeldOrDead(held map[string]struct{}, tokenID string, now int64) bool {
	if _, h := held[tokenID]; h {
		return true
	}
	exp, ok := s.deadTokens[tokenID]
	return ok && exp > now
}

// tickSizeOf reads the market tick size from broker-cached market data,
// defaulting to "0.01" when unavailable.
func tickSizeOf(b *broker.Broker, conditionID string) string {
	mktData := b.GetMarketData(conditionID)
	if mktData != nil {
		if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
			return ts
		}
	}
	return "0.01"
}

func negRiskOf(b *broker.Broker, conditionID string) bool {
	if mktData := b.GetMarketData(conditionID); mktData != nil {
		negRisk, _ := mktData["neg_risk"].(bool)
		return negRisk
	}
	return false
}

// announceOpen logs and notifies a newly opened deep-value position.
func announceOpen(b *broker.Broker, question, outcome string, price, size, filled float64) {
	if len(question) > 60 {
		question = question[:60]
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
		"filled", filled,
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
