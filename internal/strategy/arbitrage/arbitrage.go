package arbitrage

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
)

type ArbitrageStrategy struct{}

func New() *ArbitrageStrategy {
	return &ArbitrageStrategy{}
}

func (s *ArbitrageStrategy) Name() string { return "arbitrage" }

func (s *ArbitrageStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.Arbitrage
	if !config.Enabled {
		slog.Info("Arbitrage disabled")
		return nil
	}
	slog.Info("Arbitrage started", "poll", config.PollIntervalSeconds, "min_profit", config.MinProfitUSDC)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(b); err != nil {
			slog.Error("Arbitrage tick error", "error", err)
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

func (s *ArbitrageStrategy) tick(b *broker.Broker) error {
	config := b.GetConfig().Strategies.Arbitrage

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	held, err := loadHeldTokens(b)
	if err != nil {
		return err
	}

	var stats arbStats

	for _, m := range markets {
		d := s.considerMarket(b, m, held, config)
		if d == arbStopTick {
			// insufficient balance: the original inline loop aborted the tick
			// without recording diagnostics.
			return nil
		}
		stats.record(d)
	}

	slog.Info("Arbitrage tick", "scanned", stats.scanned, "under_book", stats.underBook, "profit_filtered", stats.profitFiltered, "arb_found", stats.found)
	recordArbDiagnostic(b, stats.scanned, stats.underBook, stats.profitFiltered, stats.found)
	return nil
}

// arbStats carries the per-tick funnel counters.
type arbStats struct {
	scanned        int
	underBook      int
	profitFiltered int
	found          int
}

// record applies the counter side effects one market evaluation caused. The
// under_book counter is bumped by every market that passes the over-book
// tolerance check, exactly like the original inline increments.
func (st *arbStats) record(d arbDecision) {
	switch d {
	case arbOverBook:
		st.scanned++
		return
	case arbSkipPlain, arbStopTick:
		return
	}
	st.underBook++
	switch d {
	case arbProfitFiltered:
		st.profitFiltered++
	case arbFound:
		st.found++
	}
}

// arbDecision names the outcome of evaluating one market inside the tick loop,
// so the counter bookkeeping stays a flat switch in tick.
type arbDecision int

const (
	arbSkipPlain        arbDecision = iota // malformed market: no counters
	arbStopTick                            // insufficient balance: abort the tick
	arbOverBook                            // totalPrice above tolerance: scanned++
	arbHeldAlready                         // under book, all tokens already held
	arbDiscountFiltered                    // under book but filtered by discount
	arbProfitFiltered                      // under book, filtered by min profit
	arbFound                               // arbed: legs bought, counted, notified
)

// considerMarket runs the per-market arb funnel exactly as the original inline
// loop did and reports which counter bucket the market fell into.
func (s *ArbitrageStrategy) considerMarket(b *broker.Broker, m gamma.Market, held map[string]struct{}, config cfg.ArbitrageConfig) arbDecision {
	totalPrice, ok := sumPrices(m)
	if !ok {
		return arbSkipPlain
	}

	// tolerance: Polymarket's book rarely dips below 1.00 but does oscillate.
	// We accept any market whose outcome-sum is within tolerance of 1.00 —
	// capturing transient inefficiencies *and* the rare pure-arb case.
	const tolerance = 0.02
	if totalPrice > 1.0+tolerance {
		return arbOverBook
	}

	discount := 1.0 - totalPrice
	if discount <= -tolerance {
		return arbDiscountFiltered
	}

	if allTokensHeld(m.TokenIDs, held) {
		return arbHeldAlready
	}

	estimatedProfit := discount * config.MaxPosition
	if estimatedProfit < config.MinProfitUSDC {
		return arbProfitFiltered
	}

	question := truncateQuestion(m.Question)
	slog.Info("Arbitrage found",
		"question", question,
		"total_price", fmt.Sprintf("%.4f", totalPrice),
		"discount", fmt.Sprintf("%.4f", discount),
		"est_profit", fmt.Sprintf("$%.2f", estimatedProfit),
		"outcomes", len(m.OutcomePrices),
	)

	stop, err := buyAllLegs(b, m, held, config.MaxPosition, question)
	if err != nil {
		// buyAllLegs never returns an error today (the original inline loop
		// had no error path); keep the plumbing for future changes.
		slog.Error("Arbitrage legs error", "error", err)
	}
	if stop {
		return arbStopTick
	}

	notifyArbFound(b, question, totalPrice, discount, estimatedProfit)
	return arbFound
}

// loadHeldTokens returns the set of token IDs already open for this strategy.
func loadHeldTokens(b *broker.Broker) (map[string]struct{}, error) {
	positions, err := b.GetPositionsByStrategy("arbitrage")
	if err != nil {
		return nil, fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}
	return held, nil
}

// sumPrices adds up a market's outcome prices. ok is false when the market
// lacks the minimum two priced outcomes needed to consider an arb.
func sumPrices(m gamma.Market) (float64, bool) {
	if len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
		return 0, false
	}
	totalPrice := 0.0
	for _, p := range m.OutcomePrices {
		totalPrice += p
	}
	return totalPrice, true
}

// allTokensHeld reports whether every token of the market is already open.
func allTokensHeld(tokenIDs []string, held map[string]struct{}) bool {
	allHeld := true
	for _, tid := range tokenIDs {
		if _, h := held[tid]; !h {
			allHeld = false
			break
		}
	}
	return allHeld
}

// truncateQuestion trims a market question to the log/notification width.
func truncateQuestion(q string) string {
	if len(q) > 60 {
		return q[:60]
	}
	return q
}

// buyAllLegs buys one leg per unheld outcome of the market. stop is true when
// the broker reported insufficient balance and the whole tick must end
// without error, exactly as the inline loop used to do.
func buyAllLegs(b *broker.Broker, m gamma.Market, held map[string]struct{}, maxPosition float64, question string) (stop bool, err error) {
	perOutcomeSize := maxPosition / float64(len(m.OutcomePrices))

	tickSize, negRisk := orderBookParams(b, m.ConditionID)

	for i, outcome := range m.Outcomes {
		if i >= len(m.TokenIDs) || i >= len(m.OutcomePrices) {
			break
		}

		tokenID := m.TokenIDs[i]
		price := m.OutcomePrices[i]

		if price <= 0 {
			continue
		}

		if _, h := held[tokenID]; h {
			continue
		}

		result, buyErr := b.Buy(context.Background(), broker.BuyRequest{
			TokenID:  tokenID,
			MarketID: m.ConditionID,
			Outcome:  outcome,
			Price:    price,
			SizeUSDC: perOutcomeSize,
			TickSize: tickSize,
			NegRisk:  negRisk,
			Strategy: "arbitrage",
		})
		if buyErr != nil {
			if strings.Contains(buyErr.Error(), "insufficient balance") {
				slog.Warn("Arbitrage: insufficient balance")
				return true, nil
			}
			slog.Error("Arbitrage buy error", "outcome", outcome, "error", buyErr)
			continue
		}

		paperTag := ""
		if b.IsPaper() {
			paperTag = " [PAPER]"
		}
		slog.Info("Arbitrage leg",
			"outcome", outcome,
			"price", price,
			"size", perOutcomeSize,
			"filled", result.FilledSize,
			"paper", paperTag,
		)

		held[tokenID] = struct{}{}
	}
	return false, nil
}

// orderBookParams reads tick_size / neg_risk from cached market data,
// falling back to the historical defaults when unavailable.
func orderBookParams(b *broker.Broker, conditionID string) (tickSize string, negRisk bool) {
	tickSize = "0.01"
	mktData := b.GetMarketData(conditionID)
	if mktData != nil {
		if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
			tickSize = ts
		}
		negRisk, _ = mktData["neg_risk"].(bool)
	}
	return tickSize, negRisk
}

// notifyArbFound sends the arb-found notification when a notifier is wired up.
func notifyArbFound(b *broker.Broker, question string, totalPrice, discount, estimatedProfit float64) {
	if b.GetNotifier() == nil {
		return
	}
	mode := "LIVE"
	if b.IsPaper() {
		mode = "PAPER"
	}
	msg := fmt.Sprintf("%s [Arbitrage] %s\n-> Sum: %.4f | Discount: %.2f%% | Est profit: $%.2f",
		mode, question, totalPrice, discount*100, estimatedProfit)
	b.Notify(msg)
}

// recordArbDiagnostic stores the per-tick funnel counters, best-effort.
func recordArbDiagnostic(b *broker.Broker, scanned, underBook, profitFiltered, arbFound int) {
	if database := b.GetDatabase(); database != nil {
		_ = database.RecordDiagnostic(&db.Diagnostic{
			Strategy:     "arbitrage",
			Scanned:      scanned,
			SkippedPrice: underBook,
			SkippedDead:  profitFiltered,
			Opened:       arbFound,
		})
	}
}
