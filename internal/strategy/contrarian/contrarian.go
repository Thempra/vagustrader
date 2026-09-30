package contrarian

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/client/gamma"
	"github.com/vagustrader/vagustrader/internal/strategy/kelly"
)

type ContrarianStrategy struct {
	deadTokens    map[string]int64
	priceSnapshot map[string]float64
	snapshotTime  time.Time
	// tradingPausedUntil stops the strategy from re-submitting buys after
	// the exchange reports insufficient balance. Without it the strategy
	// retried every poll forever (2k+ retries observed) since collateral
	// only frees when positions settle.
	tradingPausedUntil time.Time
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

	for ctx.Err() == nil {
		if err := s.tick(b); err != nil {
			slog.Error("Contrarian tick error", "error", err)
		}
		if err := waitOrDone(ctx, time.Duration(config.PollIntervalSeconds)*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// waitOrDone sleeps for d, returning ctx.Err() when cancelled first.
func waitOrDone(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// buildPriceIndex flattens every market outcome into a token -> price map.
func buildPriceIndex(markets []gamma.Market) map[string]float64 {
	currentPrices := make(map[string]float64)
	for _, m := range markets {
		for i, tokenID := range m.TokenIDs {
			if i < len(m.OutcomePrices) {
				currentPrices[tokenID] = m.OutcomePrices[i]
			}
		}
	}
	return currentPrices
}

// resnapshotIfStale reseeds the price snapshot when it is empty or older than
// the configured lookback. It mirrors the original early-return semantics:
// callers must end the tick when this reports true.
func (s *ContrarianStrategy) resnapshotIfStale(currentPrices map[string]float64, lookbackMinutes int) bool {
	if len(s.priceSnapshot) == 0 {
		s.priceSnapshot = currentPrices
		s.snapshotTime = time.Now()
		slog.Info("Contrarian: initial price snapshot saved", "tokens", len(currentPrices))
		return true
	}
	lookbackDuration := time.Duration(lookbackMinutes) * time.Minute
	if time.Since(s.snapshotTime) > lookbackDuration {
		s.priceSnapshot = currentPrices
		s.snapshotTime = time.Now()
		slog.Info("Contrarian: price snapshot refreshed")
		return true
	}
	return false
}

// contrarianOrder carries everything needed to place (and announce) the
// opposite-side buy after the move-side check passes.
type contrarianOrder struct {
	conditionID  string
	tokenID      string
	outcome      string
	price        float64
	size         float64
	tickSize     string
	negRisk      bool
	question     string
	movedOutcome string
	priceChange  float64
}

// scanner bundles the broker, per-tick state, and sizing config so the
// outcome-evaluation helpers keep short parameter lists. The held map is
// shared and mutated as positions open, exactly like the original loop-local
// map, and the dead map aliases s.deadTokens like the original closure.
type scanner struct {
	b             *broker.Broker
	s             *ContrarianStrategy
	held          map[string]struct{}
	snapshot      map[string]float64
	now           int64
	budget        float64
	maxPosition   float64
	kellyFraction float64
	threshold     float64
}

// isEligibleCandidate reports whether the moved outcome may trigger a
// contrarian buy: sane price, not already held, not dead-tokened, and with a
// usable previous snapshot price (returned as previousPrice).
func (sc scanner) isEligibleCandidate(tokenID string, currentPrice float64) (float64, bool) {
	if currentPrice <= 0.05 || currentPrice >= 0.95 {
		return 0, false
	}
	if _, h := sc.held[tokenID]; h {
		return 0, false
	}
	if exp, ok := sc.s.deadTokens[tokenID]; ok && exp > sc.now {
		return 0, false
	}
	previousPrice, hadPrevious := sc.snapshot[tokenID]
	if !hadPrevious || previousPrice <= 0 {
		return 0, false
	}
	return previousPrice, true
}

// buildOppositeOrder evaluates the price spike on outcome i and, when the
// opposite leg passes every filter, returns the order to place. ok=false
// means "skip this outcome" (the original `continue` paths).
func (sc scanner) buildOppositeOrder(m gamma.Market, i int, outcome string, priceChange float64) (contrarianOrder, bool) {
	oppositeIdx := 0
	if i == 0 {
		oppositeIdx = 1
	}
	if oppositeIdx >= len(m.TokenIDs) || oppositeIdx >= len(m.OutcomePrices) {
		return contrarianOrder{}, false
	}

	oppositeTokenID := m.TokenIDs[oppositeIdx]
	oppositePrice := m.OutcomePrices[oppositeIdx]
	oppositeOutcome := m.Outcomes[oppositeIdx]

	if _, h := sc.held[oppositeTokenID]; h {
		return contrarianOrder{}, false
	}
	if oppositePrice <= 0.05 || oppositePrice >= 0.95 {
		return contrarianOrder{}, false
	}

	winProb := kelly.EstimateWinProb(oppositePrice, "", "", 0)
	if winProb < 0.35 {
		return contrarianOrder{}, false
	}

	size := kelly.OptimalSize(kelly.EdgeEstimate{
		WinProb:    winProb,
		Payout:     1 - oppositePrice,
		LossAmount: oppositePrice,
	}, sc.budget, sc.maxPosition, sc.kellyFraction)

	if size <= 0 {
		return contrarianOrder{}, false
	}

	tickSize, negRisk := orderTicket(sc.b.GetMarketData(m.ConditionID))

	question := m.Question
	if len(question) > 60 {
		question = question[:60]
	}

	return contrarianOrder{
		conditionID:  m.ConditionID,
		tokenID:      oppositeTokenID,
		outcome:      oppositeOutcome,
		price:        oppositePrice,
		size:         size,
		tickSize:     tickSize,
		negRisk:      negRisk,
		question:     question,
		movedOutcome: outcome,
		priceChange:  priceChange,
	}, true
}

// orderTicket extracts the tick size and neg-risk flag from cached market
// data, applying the original "0.01" default.
func orderTicket(mktData map[string]interface{}) (string, bool) {
	tickSize := "0.01"
	negRisk := false
	if mktData != nil {
		if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
			tickSize = ts
		}
		negRisk, _ = mktData["neg_risk"].(bool)
	}
	return tickSize, negRisk
}

// executeBuy places the order; error handling stays with the caller so the
// original state mutations and log ordering are preserved.
func (sc scanner) executeBuy(order contrarianOrder) (*broker.TradeResult, error) {
	return sc.b.Buy(context.Background(), broker.BuyRequest{
		TokenID:  order.tokenID,
		MarketID: order.conditionID,
		Outcome:  order.outcome,
		Price:    order.price,
		SizeUSDC: order.size,
		TickSize: order.tickSize,
		NegRisk:  order.negRisk,
		Strategy: "contrarian",
	})
}

// handleBuyError reproduces the original three-way error classification
// verbatim (dead token, insufficient balance, generic) and reports whether
// the whole tick must stop (the original mid-loop `return nil`).
func (sc scanner) handleBuyError(tokenID string, buyErr error) bool {
	if isDeadErr(buyErr) {
		sc.s.deadTokens[tokenID] = sc.now + 3600
		return false
	}
	if strings.Contains(buyErr.Error(), "insufficient balance") {
		sc.s.tradingPausedUntil = time.Now().Add(30 * time.Minute)
		slog.Warn("Contrarian: insufficient balance, pausing new buys for 30m",
			"resume_after", sc.s.tradingPausedUntil.Format(time.RFC3339))
		return true
	}
	slog.Error("Contrarian buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
	return false
}

// logOpenAndNotify reproduces the success logging and notifier message of the
// original loop body verbatim.
func (sc scanner) logOpenAndNotify(order contrarianOrder, filledSize float64) {
	b := sc.b
	paperTag := ""
	if b.IsPaper() {
		paperTag = " [PAPER]"
	}
	slog.Info("Contrarian opened",
		"question", order.question,
		"moved", order.movedOutcome,
		"change", fmt.Sprintf("%+.1f%%", order.priceChange*100),
		"buying", order.outcome,
		"price", order.price,
		"size", order.size,
		"filled", filledSize,
		"paper", paperTag,
	)

	if b.GetNotifier() != nil {
		mode := "LIVE"
		if b.IsPaper() {
			mode = "PAPER"
		}
		msg := fmt.Sprintf("%s [Contrarian] %s\n-> %s moved %+.1f%%, buying %s @ %.3f | $%.2f",
			mode, order.question, order.movedOutcome, order.priceChange*100, order.outcome, order.price, order.size)
		b.Notify(msg)
	}
}

// processOutcome evaluates one outcome of a market and, when eligible,
// executes the opposite-side buy. It returns how many positions opened
// (0 or 1) and whether the whole tick must stop (insufficient balance).
func (sc scanner) processOutcome(m gamma.Market, i int, outcome string) (int, bool) {
	tokenID := m.TokenIDs[i]
	currentPrice := m.OutcomePrices[i]

	previousPrice, ok := sc.isEligibleCandidate(tokenID, currentPrice)
	if !ok {
		return 0, false
	}

	priceChange := (currentPrice - previousPrice) / previousPrice
	if priceChange <= sc.threshold {
		return 0, false
	}

	order, ok := sc.buildOppositeOrder(m, i, outcome, priceChange)
	if !ok {
		return 0, false
	}

	result, buyErr := sc.executeBuy(order)
	if buyErr != nil {
		if stop := sc.handleBuyError(order.tokenID, buyErr); stop {
			return 0, true
		}
		return 0, false
	}

	sc.logOpenAndNotify(order, result.FilledSize)
	sc.held[order.tokenID] = struct{}{}
	return 1, false
}

// processMarket scans one market's outcomes and returns the number of
// positions opened plus the tick-stop sentinel from processOutcome.
func (sc scanner) processMarket(m gamma.Market) (int, bool) {
	if len(m.Outcomes) < 2 || len(m.OutcomePrices) < 2 || len(m.TokenIDs) < 2 {
		return 0, false
	}

	var opened int
	for i, outcome := range m.Outcomes {
		if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
			break
		}
		n, stop := sc.processOutcome(m, i, outcome)
		opened += n
		if stop {
			return opened, true
		}
	}
	return opened, false
}

func (s *ContrarianStrategy) tick(b *broker.Broker) error {
	config := b.GetConfig().Strategies.Contrarian
	risk := b.GetConfig().Risk

	if !b.IsPaper() && time.Now().Before(s.tradingPausedUntil) {
		slog.Info("Contrarian: trading paused after insufficient balance",
			"resume_after", s.tradingPausedUntil.Format(time.RFC3339))
		return nil
	}

	gammaClient := b.GetGamma()
	if gammaClient == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gammaClient.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	currentPrices := buildPriceIndex(markets)

	positions, err := b.GetPositionsByStrategy("contrarian")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	if s.resnapshotIfStale(currentPrices, config.LookbackMinutes) {
		return nil
	}

	sc := scanner{
		b:             b,
		s:             s,
		held:          held,
		snapshot:      s.priceSnapshot,
		now:           time.Now().Unix(),
		budget:        risk.MaxPositionUSDC * 10,
		maxPosition:   config.MaxPosition,
		kellyFraction: config.KellyFraction,
		threshold:     config.PriceChangeThreshold,
	}

	// stop mirrors the original mid-loop `return nil` on insufficient
	// balance: the tick summary log must NOT run in that case.
	var opened int
	for _, m := range markets {
		n, stop := sc.processMarket(m)
		opened += n
		if stop {
			return nil
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
