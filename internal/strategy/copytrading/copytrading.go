package copytrading

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/dataapi"
)

const deadTokenTTL = 3600

type CopyTradingStrategy struct {
	broker     *broker.Broker
	deadTokens map[string]int64
}

func New(b *broker.Broker) *CopyTradingStrategy {
	return &CopyTradingStrategy{
		broker:     b,
		deadTokens: make(map[string]int64),
	}
}

func (s *CopyTradingStrategy) Name() string {
	return "copy_trading"
}

func (s *CopyTradingStrategy) Run(ctx context.Context, b *broker.Broker) error {
	copyCfg := b.GetConfig().Strategies.CopyTrading
	if !copyCfg.Enabled {
		slog.Info("CopyTrading disabled")
		return nil
	}
	slog.Info("CopyTrading started", "poll", copyCfg.PollIntervalSeconds, "min_pnl", copyCfg.MinPnL)

	for ctx.Err() == nil {
		if err := s.tick(b); err != nil {
			slog.Error("CopyTrading tick error", "error", err)
		}
		if err := waitOrDone(ctx, time.Duration(copyCfg.PollIntervalSeconds)*time.Second); err != nil {
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

type sourceTrader struct {
	Address string  `json:"address"`
	Name    string  `json:"name"`
	Outcome string  `json:"outcome"`
	Size    float64 `json:"size"`
	Price   float64 `json:"price"`
}

type candidatePos struct {
	pos    dataapi.Position
	trader dataapi.LeaderboardEntry
}

// decision is the sentinel a per-candidate evaluation hands back to the
// candidate loop so it can replay the original continue/break/return flow.
type decision int

const (
	decisionSkip   decision = iota // original `continue`
	decisionStop                   // original mid-loop `return nil` (insufficient balance)
	decisionTraded                 // original fall-through after a successful buy
)

// skipCode names the tick-summary counter a filter increments when it skips
// a candidate.
type skipCode int

const (
	skipNone skipCode = iota
	skipDead
	skipHeld
	skipNoMarket
	skipTooFar
	skipNoPrice
	skipPrice
	skipSport
	skipBuy
)

// tickStats mirrors the original per-tick skip counters 1:1.
type tickStats struct {
	totalPositionsSeen, skippedHeld, skippedDead int
	skippedPrice, skippedTooFar, skippedNoPrice  int
	skippedNoMarket, skippedSport, skippedBuy    int
	openedThisTick                               int
}

// count increments the counter matching a filter's skip code.
func (st *tickStats) count(sk skipCode) {
	switch sk {
	case skipDead:
		st.skippedDead++
	case skipHeld:
		st.skippedHeld++
	case skipNoMarket:
		st.skippedNoMarket++
	case skipTooFar:
		st.skippedTooFar++
	case skipNoPrice:
		st.skippedNoPrice++
	case skipPrice:
		st.skippedPrice++
	case skipSport:
		st.skippedSport++
	case skipBuy:
		st.skippedBuy++
	}
}

// copyRun bundles the mutable per-tick state (broker handle, configs, held
// map, trader source map, per-trader copy budget, open-position count, and
// the skip counters) so the extracted helpers keep short parameter lists
// instead of threading ten loose arguments.
type copyRun struct {
	s            *CopyTradingStrategy
	b            *broker.Broker
	stratCfg     cfg.CopyTradingConfig
	risk         cfg.RiskConfig
	myPositions  map[string]struct{}
	tokenSources map[string][]sourceTrader
	traderCopied map[string]int
	openCount    int
	stats        tickStats
}

// entryQuote is the resolved buy ticket for one candidate token.
type entryQuote struct {
	price    float64
	outcome  string
	tickSize string
	negRisk  bool
	slug     string
	question string
}

// logTickBalance reproduces the original paper/live balance-check logging.
func logTickBalance(b *broker.Broker) {
	if b.IsPaper() {
		balance, err := b.GetStrategyBalance("copy_trading")
		if err != nil {
			slog.Warn("Could not fetch paper balance", "error", err)
		} else {
			slog.Info("Paper balance check", "balance", balance)
		}
	} else {
		balance, err := b.GetBalance()
		if err != nil {
			slog.Warn("Could not fetch balance", "error", err)
		} else {
			slog.Info("Balance check", "balance", balance)
		}
	}
}

// winRateAcceptable applies the trader win-rate gate. It keeps the original
// Debug log when the trader is rejected.
func (r *copyRun) winRateAcceptable(trader dataapi.LeaderboardEntry, traderPositions []dataapi.Position) bool {
	if r.stratCfg.MinTraderWinRate <= 0 || len(traderPositions) == 0 {
		return true
	}
	profitable := 0
	for _, tp := range traderPositions {
		if tp.CashPnl > 0 {
			profitable++
		}
	}
	winRate := float64(profitable) / float64(len(traderPositions))
	if winRate < r.stratCfg.MinTraderWinRate {
		slog.Debug("Skipping trader — low win rate", "trader", trader.Address[:min(8, len(trader.Address))], "winRate", fmt.Sprintf("%.0f%%", winRate*100))
		return false
	}
	return true
}

// collectCandidates gathers every open position of the leaderboard traders
// into the token->sources map plus the deduped candidate list, exactly like
// the original trader loop.
func (r *copyRun) collectCandidates(topTraders []dataapi.LeaderboardEntry) []candidatePos {
	tokenSources := r.tokenSources
	var candidates []candidatePos
	seenTokens := make(map[string]struct{})

	for _, trader := range topTraders {
		traderPositions, err := r.b.GetDataAPI().GetPositions(trader.Address)
		if err != nil {
			continue
		}
		if !r.winRateAcceptable(trader, traderPositions) {
			continue
		}
		for _, pos := range traderPositions {
			if pos.TokenID == "" {
				continue
			}
			tokenSources[pos.TokenID] = append(tokenSources[pos.TokenID], sourceTrader{
				Address: trader.Address,
				Name:    trader.Name,
				Outcome: pos.Outcome,
				Size:    pos.Size,
				Price:   pos.AvgPrice,
			})
			if _, seen := seenTokens[pos.TokenID]; !seen {
				seenTokens[pos.TokenID] = struct{}{}
				candidates = append(candidates, candidatePos{pos, trader})
			}
		}
	}
	return candidates
}

// resolvesTooFar applies the MaxDaysToResolution horizon gate.
func (r *copyRun) resolvesTooFar(mktData map[string]interface{}) bool {
	if r.stratCfg.MaxDaysToResolution <= 0 {
		return false
	}
	endDateStr, _ := mktData["end_date_iso"].(string)
	if endDateStr == "" {
		return false
	}
	endDt, err := time.Parse(time.RFC3339, endDateStr)
	if err != nil {
		return false
	}
	daysLeft := time.Until(endDt).Hours() / 24
	return daysLeft > r.stratCfg.MaxDaysToResolution
}

// priceFromMarket pulls the entry price/outcome for the candidate token from
// the cached market payload's token list.
func priceFromMarket(mktData map[string]interface{}, tokenID string) (float64, string) {
	buyPrice := 0.0
	buyOutcome := ""
	if tokens, ok := mktData["tokens"].([]map[string]interface{}); ok {
		for _, t := range tokens {
			tid, _ := t["token_id"].(string)
			if tid == tokenID {
				if p, ok := t["price"].(float64); ok {
					buyPrice = p
				}
				if outcome, _ := t["outcome"].(string); outcome != "" {
					buyOutcome = outcome
				}
				break
			}
		}
	}
	return buyPrice, buyOutcome
}

// priceFromOrderbook is the fallback best-quote lookup when the market cache
// carries no usable price for the token.
func priceFromOrderbook(b *broker.Broker, tokenID string, fallback float64) float64 {
	ob, err := b.GetOrderbook(tokenID)
	if err != nil {
		return fallback
	}
	if len(ob.Asks) > 0 {
		return ob.Asks[0].Price
	}
	if len(ob.Bids) > 0 {
		return ob.Bids[0].Price
	}
	return fallback
}

// entryQuote gates resolve and price-check the candidate, returning the skip
// code of the first price gate that rejects it (skipNone when it passes).
func (r *copyRun) entryQuote(pos dataapi.Position, mktData map[string]interface{}) (entryQuote, skipCode) {
	buyPrice, marketOutcome := priceFromMarket(mktData, pos.TokenID)
	buyOutcome := pos.Outcome
	if marketOutcome != "" {
		buyOutcome = marketOutcome
	}

	if buyPrice <= 0 {
		buyPrice = priceFromOrderbook(r.b, pos.TokenID, buyPrice)
	}

	if buyPrice <= 0 {
		return entryQuote{}, skipNoPrice
	}

	if !(r.stratCfg.PriceGuard < buyPrice && buyPrice < (1-r.stratCfg.PriceGuard)) {
		return entryQuote{}, skipPrice
	}
	if r.stratCfg.MinEntryPrice > 0 && buyPrice < r.stratCfg.MinEntryPrice {
		return entryQuote{}, skipPrice
	}
	if r.stratCfg.MaxEntryPrice > 0 && buyPrice > r.stratCfg.MaxEntryPrice {
		return entryQuote{}, skipPrice
	}

	tickSize, _ := mktData["tick_size"].(string)
	if tickSize == "" {
		tickSize = "0.01"
	}
	negRisk, _ := mktData["neg_risk"].(bool)
	slug, _ := mktData["slug"].(string)
	question, _ := mktData["question"].(string)

	return entryQuote{
		price:    buyPrice,
		outcome:  buyOutcome,
		tickSize: tickSize,
		negRisk:  negRisk,
		slug:     slug,
		question: question,
	}, skipNone
}

// sourceJSONFor renders the contributing top traders for the trade record.
func (r *copyRun) sourceJSONFor(tokenID string) string {
	sourceJSON := "[]"
	if sources, ok := r.tokenSources[tokenID]; ok && len(sources) > 0 {
		if raw, err := json.Marshal(sources); err == nil {
			sourceJSON = string(raw)
		}
	}
	return sourceJSON
}

// executeBuy places the copy order and classifies errors the same way the
// original inline block did, returning the decision for the caller loop.
func (r *copyRun) executeBuy(c candidatePos, q entryQuote, size float64) decision {
	sourceJSON := r.sourceJSONFor(c.pos.TokenID)

	result, buyErr := r.b.Buy(context.Background(), broker.BuyRequest{
		TokenID:    c.pos.TokenID,
		MarketID:   c.pos.MarketID,
		Outcome:    q.outcome,
		Price:      q.price,
		SizeUSDC:   size,
		TickSize:   q.tickSize,
		NegRisk:    q.negRisk,
		SourceJSON: sourceJSON,
		Strategy:   "copy_trading",
	})
	if buyErr != nil {
		if isDeadTokenError(buyErr) {
			r.s.deadTokens[c.pos.TokenID] = time.Now().Unix() + deadTokenTTL
			slog.Debug("Marking dead orderbook", "token", c.pos.TokenID[:min(12, len(c.pos.TokenID))])
		} else if strings.Contains(buyErr.Error(), "insufficient balance") {
			slog.Warn("Insufficient USDC balance — aborting tick")
			return decisionStop
		} else {
			slog.Error("Buy error", "token", c.pos.TokenID[:min(12, len(c.pos.TokenID))], "error", buyErr)
		}
		r.stats.skippedBuy++
		return decisionSkip
	}

	r.announceCopy(c, q, result, size)
	return decisionTraded
}

// announceCopy reproduces the success log and notifier message of the
// original loop body verbatim.
func (r *copyRun) announceCopy(c candidatePos, q entryQuote, result *broker.TradeResult, size float64) {
	b := r.b
	question := c.pos.TokenID[:min(20, len(c.pos.TokenID))]
	if q.question != "" {
		question = q.question
	}

	paperTag := ""
	if b.IsPaper() {
		paperTag = " [PAPER]"
	}
	slog.Info("Copied",
		"trader", c.trader.Address[:min(8, len(c.trader.Address))],
		"token", c.pos.TokenID[:min(8, len(c.pos.TokenID))],
		"outcome", q.outcome,
		"price", result.Price,
		"size", size,
		"filled", result.FilledSize,
		"sources", len(r.tokenSources[c.pos.TokenID]),
		"paper", paperTag,
	)

	if b.GetNotifier() != nil {
		mode := "LIVE"
		if b.IsPaper() {
			mode = "PAPER"
		}
		urlLine := ""
		if q.slug != "" {
			urlLine = fmt.Sprintf("\nhttps://polymarket.com/market/%s", q.slug)
		}
		truncQ := question
		if len(truncQ) > 80 {
			truncQ = truncQ[:80]
		}
		msg := fmt.Sprintf("%s — Position opened\n%s\n-> Outcome: %s\n-> Price: %.3f  |  Size: $%.2f  |  %d top traders%s",
			mode, truncQ, q.outcome, result.Price, size, len(r.tokenSources[c.pos.TokenID]), urlLine)
		b.Notify(msg)
	}
}

// evaluate runs the full per-candidate filter chain and, when everything
// passes, the buy. It increments the exact same skip counters as the
// original loop body and maps its control flow onto decision sentinels.
func (r *copyRun) evaluate(c candidatePos) decision {
	pos := c.pos
	r.stats.totalPositionsSeen++

	now := time.Now().Unix()
	if exp, ok := r.s.deadTokens[pos.TokenID]; ok && exp > now {
		r.stats.skippedDead++
		return decisionSkip
	}

	if _, held := r.myPositions[pos.TokenID]; held {
		r.stats.skippedHeld++
		return decisionSkip
	}

	mktData := r.b.GetMarketData(pos.MarketID)

	if mktData == nil {
		r.stats.skippedNoMarket++
		r.s.deadTokens[pos.TokenID] = now + deadTokenTTL
		return decisionSkip
	}

	if active, _ := mktData["active"].(bool); !active {
		r.stats.skippedNoMarket++
		return decisionSkip
	}

	if r.resolvesTooFar(mktData) {
		r.stats.skippedTooFar++
		return decisionSkip
	}

	q, sk := r.entryQuote(pos, mktData)
	if sk != skipNone {
		r.stats.count(sk)
		return decisionSkip
	}

	sport := detectSport(q.slug, pos.Slug)
	if isBlocked(sport, r.stratCfg.AllowedSports, r.stratCfg.BlockedSports) {
		r.stats.skippedSport++
		return decisionSkip
	}

	size := getPositionSize(q.price, r.stratCfg.SizeTiers, r.risk.MaxPositionUSDC)
	if size <= 0 {
		return decisionSkip
	}

	if size*q.price < 1.0 {
		slog.Debug("Skipping — maker amount below $1 minimum", "token", pos.TokenID[:min(8, len(pos.TokenID))], "makerAmt", size*q.price)
		return decisionSkip
	}

	return r.executeBuy(c, q, size)
}

// runCandidates replays the original candidate loop, including its break on
// the open-position cap and its mid-loop abort on insufficient balance. It
// reports whether the whole tick must stop.
func (r *copyRun) runCandidates(candidates []candidatePos) bool {
	for _, c := range candidates {
		if r.openCount >= r.risk.MaxOpenPositions {
			break
		}
		if r.traderCopied[c.trader.Address] >= r.stratCfg.MaxCopyPerTrader {
			continue
		}
		if stop := r.applyDecision(c); stop {
			return true
		}
	}
	return false
}

// applyDecision acts on one candidate evaluation outcome and reports whether
// the tick must stop (insufficient balance).
func (r *copyRun) applyDecision(c candidatePos) bool {
	switch r.evaluate(c) {
	case decisionStop:
		return true
	case decisionTraded:
		r.traderCopied[c.trader.Address]++
		r.openCount++
		r.stats.openedThisTick++
		r.myPositions[c.pos.TokenID] = struct{}{}
	case decisionSkip:
	}
	return false
}

func (s *CopyTradingStrategy) tick(b *broker.Broker) error {
	copyCfg := b.GetConfig().Strategies.CopyTrading
	risk := b.GetConfig().Risk

	logTickBalance(b)

	topTraders, err := b.GetDataAPI().GetTopTraders(copyCfg.MinPnL, 20)
	if err != nil || len(topTraders) == 0 {
		slog.Info("No qualifying traders found")
		return nil
	}

	positions, err := b.GetPositionsByStrategy("copy_trading")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	myPositions := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		myPositions[p.TokenID] = struct{}{}
	}
	openCount := len(myPositions)

	slog.Info("Tick start", "traders", len(topTraders), "open", openCount)

	b.ClearMarketCache()

	r := &copyRun{
		s:            s,
		b:            b,
		stratCfg:     copyCfg,
		risk:         risk,
		myPositions:  myPositions,
		tokenSources: make(map[string][]sourceTrader),
		traderCopied: make(map[string]int),
		openCount:    openCount,
	}

	candidates := r.collectCandidates(topTraders)

	// stop mirrors the original mid-loop `return nil` on insufficient
	// balance: the tick-summary log must NOT run in that case.
	if r.runCandidates(candidates) {
		return nil
	}

	slog.Info("Tick end",
		"opened", r.stats.openedThisTick,
		"total_seen", r.stats.totalPositionsSeen,
		"held", r.stats.skippedHeld,
		"dead", r.stats.skippedDead,
		"no_market", r.stats.skippedNoMarket,
		"no_price", r.stats.skippedNoPrice,
		"price", r.stats.skippedPrice,
		"too_far", r.stats.skippedTooFar,
		"sport", r.stats.skippedSport,
		"buy_err", r.stats.skippedBuy,
	)
	return nil
}

func isDeadTokenError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "dead orderbook") || strings.Contains(msg, "does not exist")
}

var sportPrefixes = map[string]string{
	"mlb":        "mlb",
	"nba":        "nba",
	"nhl":        "nhl",
	"nfl":        "nfl",
	"atp":        "tennis",
	"wta":        "tennis",
	"wnba":       "wnba",
	"cs2":        "esports",
	"dota2":      "esports",
	"epl":        "soccer",
	"laliga":     "soccer",
	"seriea":     "soccer",
	"bundesliga": "soccer",
	"ufc":        "mma",
	"mma":        "mma",
	"mls":        "soccer",
	"ncaab":      "ncaab",
	"ncaaf":      "ncaaf",
	"pga":        "golf",
	"tennis":     "tennis",
}

func detectSport(marketSlug string, positionSlug string) string {
	for _, s := range []string{marketSlug, positionSlug} {
		if s == "" {
			continue
		}
		lower := strings.ToLower(s)
		for prefix, sport := range sportPrefixes {
			if strings.HasPrefix(lower, prefix+"-") || strings.Contains(lower, "-"+prefix+"-") {
				return sport
			}
		}
	}
	return ""
}

func isBlocked(sport string, allowed []string, blocked []string) bool {
	if sport == "" {
		return false
	}
	if len(allowed) > 0 {
		found := false
		for _, a := range allowed {
			if strings.EqualFold(a, sport) {
				found = true
				break
			}
		}
		return !found
	}
	for _, b := range blocked {
		if strings.EqualFold(b, sport) {
			return true
		}
	}
	return false
}

func getPositionSize(price float64, tiers []cfg.SizeTier, maxSize float64) float64 {
	for _, t := range tiers {
		if price >= t.MinPrice && price < t.MaxPrice && t.SizeUSDC > 0 {
			return math.Min(t.SizeUSDC, maxSize)
		}
	}
	// P2.2: no tier matched. Previously we defaulted to maxSize, which
	// silently placed the largest possible bet on prices that fell
	// outside every configured band. Return 0 (skip) instead and log so
	// the gap in the tier config is visible.
	slog.Warn("CopyTrading no size tier matched price, skipping", "price", price)
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
