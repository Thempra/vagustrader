package whaleconsensus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
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
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(b); err != nil {
			slog.Error("WhaleConsensus tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
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

	now := time.Now().Unix()
	var opened int

	for tokenID, sources := range tokenSources {
		if len(sources) < config.MinTraders {
			continue
		}

		if _, h := held[tokenID]; h {
			continue
		}

		if exp, ok := s.deadTokens[tokenID]; ok && exp > now {
			continue
		}

		marketID := tokenMarkets[tokenID]
		if marketID == "" {
			continue
		}

		mktData := b.GetMarketData(marketID)
		if mktData == nil {
			s.deadTokens[tokenID] = now + 3600
			continue
		}
		if active, _ := mktData["active"].(bool); !active {
			continue
		}

		buyPrice := 0.0
		outcome := tokenOutcomes[tokenID]
		tickSize := "0.01"
		if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
			tickSize = ts
		}

		if tokens, ok := mktData["tokens"].([]map[string]interface{}); ok {
			for _, t := range tokens {
				if tid, _ := t["token_id"].(string); tid == tokenID {
					if p, ok := t["price"].(float64); ok {
						buyPrice = p
					}
					if o, ok := t["outcome"].(string); ok && o != "" {
						outcome = o
					}
					break
				}
			}
		}

		if buyPrice <= 0 {
			ob, err := b.GetOrderbook(tokenID)
			if err == nil && len(ob.Asks) > 0 {
				buyPrice = ob.Asks[0].Price
			}
		}

		if buyPrice <= 0 || buyPrice >= 0.95 || buyPrice <= 0.05 {
			continue
		}

		winProb := kelly.EstimateWinProb(buyPrice, "", "", len(sources))
		size := kelly.OptimalSize(kelly.EdgeEstimate{
			WinProb:    winProb,
			Payout:     buyPrice,
			LossAmount: buyPrice,
		}, risk.MaxPositionUSDC*10, config.MaxPosition, config.KellyFraction)

		if size <= 0 {
			continue
		}

		negRisk := false
		if mktData != nil {
			negRisk, _ = mktData["neg_risk"].(bool)
		}

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
			Outcome:    outcome,
			Price:      buyPrice,
			SizeUSDC:   size,
			TickSize:   tickSize,
			NegRisk:    negRisk,
			SourceJSON: sourceJSON,
			Strategy:   "whale_consensus",
		})
		if buyErr != nil {
			if isDeadErr(buyErr) {
				s.deadTokens[tokenID] = now + 3600
			} else if strings.Contains(buyErr.Error(), "insufficient balance") {
				slog.Warn("WhaleConsensus: insufficient balance")
				return nil
			} else {
				slog.Error("WhaleConsensus buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
			}
			continue
		}

		paperTag := ""
		if b.IsPaper() {
			paperTag = " [PAPER]"
		}
		slog.Info("WhaleConsensus opened",
			"question", question,
			"traders", len(sources),
			"price", buyPrice,
			"size", size,
			"filled", result.FilledSize,
			"paper", paperTag,
		)

		if b.GetNotifier() != nil {
			mode := "LIVE"
			if b.IsPaper() {
				mode = "PAPER"
			}
			msg := fmt.Sprintf("%s [WhaleConsensus] %s\n-> %d traders agree on %s @ %.3f | $%.2f",
				mode, question, len(sources), outcome, buyPrice, size)
			b.Notify(msg)
		}

		opened++
		held[tokenID] = struct{}{}
	}

	if opened > 0 {
		slog.Info("WhaleConsensus tick", "opened", opened)
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
