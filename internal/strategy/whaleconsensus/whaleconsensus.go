package whaleconsensus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/strategy/kelly"
)

type WhaleConsensusStrategy struct {
	deadTokens map[string]int64
}

func New() *WhaleConsensusStrategy {
	return &WhaleConsensusStrategy{deadTokens: make(map[string]int64)}
}

func (s *WhaleConsensusStrategy) Name() string { return "whale_consensus" }

func (s *WhaleConsensusStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.WhaleConsensus
	if !config.Enabled {
		slog.Info("WhaleConsensus disabled")
		return nil
	}
	slog.Info("WhaleConsensus started", "poll", config.PollIntervalSeconds, "min_traders", config.MinTraders)

	for {
		if err := s.tick(b); err != nil {
			slog.Error("WhaleConsensus tick error", "error", err)
		}
		if err := waitOrDone(ctx, config.PollIntervalSeconds); err != nil {
			return err
		}
	}
}

// waitOrDone sleeps for pollSeconds seconds, returning ctx.Err() early when
// ctx is cancelled — identical semantics to the original select loop.
func waitOrDone(ctx context.Context, pollSeconds int) error {
	t := time.NewTicker(time.Duration(pollSeconds) * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
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

func (s *WhaleConsensusStrategy) tick(b *broker.Broker) error {
	config := b.GetConfig().Strategies.WhaleConsensus
	risk := b.GetConfig().Risk

	topTraders, err := b.GetDataAPI().GetTopTraders(config.MinPnL, 20)
	if err != nil || len(topTraders) < config.MinTraders {
		return nil
	}

	positions, err := b.GetPositionsByStrategy("whale_consensus")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	tokenSources := make(map[string][]sourceTrader)
	tokenMarkets := make(map[string]string)
	tokenOutcomes := make(map[string]string)
	seen := make(map[string]struct{})

	for _, trader := range topTraders {
		traderPositions, err := b.GetDataAPI().GetPositions(trader.Address)
		if err != nil {
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
			if _, ok := seen[pos.TokenID]; !ok {
				seen[pos.TokenID] = struct{}{}
				tokenMarkets[pos.TokenID] = pos.MarketID
				tokenOutcomes[pos.TokenID] = pos.Outcome
			}
		}
	}

	env := whaleCtx{b: b, tokenMarkets: tokenMarkets, tokenOutcomes: tokenOutcomes,
		held: held, config: config, risk: risk, now: time.Now().Unix()}
	var opened int

	for tokenID, sources := range tokenSources {
		stop, openedNow := s.openCandidate(tokenID, sources, env)
		if stop {
			return nil
		}
		if openedNow {
			opened++
		}
	}

	if opened > 0 {
		slog.Info("WhaleConsensus tick", "opened", opened)
	}
	return nil
}

// whaleCtx bundles the per-tick environment shared by the candidate helpers.
type whaleCtx struct {
	b             *broker.Broker
	tokenMarkets  map[string]string
	tokenOutcomes map[string]string
	held          map[string]struct{}
	config        cfg.WhaleConsensusConfig
	risk          cfg.RiskConfig
	now           int64
}

// whaleQuote carries the market-derived buy parameters for one candidate token.
type whaleQuote struct {
	price    float64
	outcome  string
	tickSize string
	negRisk  bool
}

// openCandidate replays the original per-token guard chain and, when every
// filter passes, sizes and places the buy. stop mirrors the original
// "insufficient balance" early return from tick; openedNow reports a fill.
func (s *WhaleConsensusStrategy) openCandidate(tokenID string, sources []sourceTrader, env whaleCtx) (stop, openedNow bool) {
	marketID, ok := s.candidateGate(tokenID, sources, env)
	if !ok {
		return false, false
	}
	return s.placeCandidateBuy(tokenID, sources, env, marketID)
}

// candidateGate replays the original pre-trade filters in order, returning the
// market to trade in when every guard passes.
func (s *WhaleConsensusStrategy) candidateGate(tokenID string, sources []sourceTrader, env whaleCtx) (string, bool) {
	if len(sources) < env.config.MinTraders {
		return "", false
	}
	if _, h := env.held[tokenID]; h {
		return "", false
	}
	if exp, ok := s.deadTokens[tokenID]; ok && exp > env.now {
		return "", false
	}
	marketID := env.tokenMarkets[tokenID]
	if marketID == "" {
		return "", false
	}
	return marketID, true
}

// placeCandidateBuy quotes, sizes and buys one consensus token, replaying the
// original post-filter flow verbatim.
func (s *WhaleConsensusStrategy) placeCandidateBuy(tokenID string, sources []sourceTrader, env whaleCtx, marketID string) (stop, openedNow bool) {
	b, now := env.b, env.now

	mktData := b.GetMarketData(marketID)
	if mktData == nil {
		s.deadTokens[tokenID] = now + 3600
		return false, false
	}
	if active, _ := mktData["active"].(bool); !active {
		return false, false
	}

	q := whaleQuote{outcome: env.tokenOutcomes[tokenID], tickSize: "0.01"}
	if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
		q.tickSize = ts
	}
	fillQuote(mktData, tokenID, &q)
	if q.price <= 0 {
		ob, err := b.GetOrderbook(tokenID)
		if err == nil && len(ob.Asks) > 0 {
			q.price = ob.Asks[0].Price
		}
	}
	if q.price <= 0 || q.price >= 0.95 || q.price <= 0.05 {
		return false, false
	}

	winProb := kelly.EstimateWinProb(q.price, "", "", len(sources))
	size := kelly.OptimalSize(kelly.EdgeEstimate{
		WinProb:    winProb,
		Payout:     1 - q.price,
		LossAmount: q.price,
	}, env.risk.MaxPositionUSDC*10, env.config.MaxPosition, env.config.KellyFraction)
	if size <= 0 {
		return false, false
	}
	q.negRisk, _ = mktData["neg_risk"].(bool)

	sourceJSON := "[]"
	if raw, err := json.Marshal(sources); err == nil {
		sourceJSON = string(raw)
	}

	question, _ := mktData["question"].(string)
	if len(question) > 60 {
		question = question[:60]
	}

	result, buyErr := b.Buy(context.Background(), broker.BuyRequest{
		TokenID:    tokenID,
		MarketID:   marketID,
		Outcome:    q.outcome,
		Price:      q.price,
		SizeUSDC:   size,
		TickSize:   q.tickSize,
		NegRisk:    q.negRisk,
		SourceJSON: sourceJSON,
		Strategy:   "whale_consensus",
	})
	if buyErr != nil {
		return s.handleBuyError(tokenID, buyErr, now), false
	}

	announceWhaleOpen(b, question, len(sources), q.outcome, q.price, size, result.FilledSize)
	return false, true
}

// fillQuote copies price/outcome for tokenID out of the gamma market payload,
// exactly as the original inline token scan did.
func fillQuote(mktData map[string]interface{}, tokenID string, q *whaleQuote) {
	if tokens, ok := mktData["tokens"].([]map[string]interface{}); ok {
		for _, t := range tokens {
			if tid, _ := t["token_id"].(string); tid == tokenID {
				if p, ok := t["price"].(float64); ok {
					q.price = p
				}
				if o, ok := t["outcome"].(string); ok && o != "" {
					q.outcome = o
				}
				break
			}
		}
	}
}

// handleBuyError replays the original error branches; returns stop=true only
// for insufficient balance (which ended the whole tick).
func (s *WhaleConsensusStrategy) handleBuyError(tokenID string, buyErr error, now int64) (stop bool) {
	if isDeadErr(buyErr) {
		s.deadTokens[tokenID] = now + 3600
	} else if strings.Contains(buyErr.Error(), "insufficient balance") {
		slog.Warn("WhaleConsensus: insufficient balance")
		return true
	} else {
		slog.Error("WhaleConsensus buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
	}
	return false
}

// announceWhaleOpen logs and notifies a successful open, same fields/order.
func announceWhaleOpen(b *broker.Broker, question string, traders int, outcome string, price, size, filled float64) {
	paperTag := ""
	if b.IsPaper() {
		paperTag = " [PAPER]"
	}
	slog.Info("WhaleConsensus opened",
		"question", question,
		"traders", traders,
		"price", price,
		"size", size,
		"filled", filled,
		"paper", paperTag,
	)
	if b.GetNotifier() != nil {
		mode := "LIVE"
		if b.IsPaper() {
			mode = "PAPER"
		}
		msg := fmt.Sprintf("%s [WhaleConsensus] %s\n-> %d traders agree on %s @ %.3f | $%.2f",
			mode, question, traders, outcome, price, size)
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

func minL(a, b int) int {
	if a < b {
		return a
	}
	return b
}
