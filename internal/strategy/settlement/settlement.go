package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/db"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type SettlementStrategy struct {
	broker *broker.Broker
}

func New(b *broker.Broker) *SettlementStrategy {
	return &SettlementStrategy{broker: b}
}

func (s *SettlementStrategy) Name() string { return "settlement" }

func (s *SettlementStrategy) Run(ctx context.Context, b *broker.Broker) error {
	intervalSec := b.GetConfig().Strategies.Settlement.IntervalSeconds
	if intervalSec <= 0 {
		intervalSec = 300
	}
	interval := time.Duration(intervalSec) * time.Second

	slog.Info("Settlement checker started", "interval", intervalSec)
	for {
		if err := s.tick(b); err != nil {
			slog.Error("Settlement tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (s *SettlementStrategy) tick(b *broker.Broker) error {
	positions, err := b.GetPositions()
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	for _, pos := range positions {
		if err := s.checkPosition(b, &pos); err != nil {
			tid := pos.TokenID
			if len(tid) > 12 {
				tid = tid[:12]
			}
			slog.Error("Settlement error", "token", tid, "error", err)
		}
	}
	return nil
}

func (s *SettlementStrategy) checkPosition(b *broker.Broker, pos *db.Position) error {
	tokenID := pos.TokenID
	marketID := pos.MarketID
	avgPrice := pos.AvgPrice
	shares := pos.Size
	outcome := pos.Outcome
	strategy := pos.Strategy
	if strategy == "" {
		strategy = "copy_trading"
	}

	clobData := b.GetMarketData(marketID)
	if clobData == nil {
		return nil
	}
	if closed, _ := clobData["closed"].(bool); !closed {
		return nil
	}

	finalPrice := tokenPrice(clobData, tokenID)
	if finalPrice < 0 {
		return nil
	}

	pnl := (finalPrice - avgPrice) * shares
	recoveredAmount := shares * finalPrice

	question, slug, err := b.GetMarketMeta(marketID)
	if err != nil {
		return fmt.Errorf("get market meta: %w", err)
	}
	if question == "" {
		if q, ok := clobData["question"].(string); ok && q != "" {
			question = q
			slug, _ = clobData["slug"].(string)
			if err := b.UpsertMarketMeta(&db.MarketMeta{
				ConditionID: marketID,
				Question:    question,
				Slug:        slug,
			}); err != nil {
				return fmt.Errorf("upsert market meta: %w", err)
			}
		}
	}
	if question == "" {
		tid := tokenID
		if len(tid) > 20 {
			tid = tid[:20]
		}
		question = tid + "..."
	}

	if !b.IsPaper() {
		negRisk, _ := clobData["neg_risk"].(bool)
		slog.Info("Market resolved — funds stay on Polymarket as Available to trade", "market", marketID[:min(12, len(marketID))], "negRisk", negRisk, "shares", shares)
	}

	paperTag := ""
	if b.IsPaper() {
		paperTag = " [PAPER]"
	}
	slog.Info("Settled",
		"strategy", strategy,
		"question", trunc(question, 50),
		"outcome", outcome,
		"final", fmt.Sprintf("%.0f", finalPrice),
		"avg", fmt.Sprintf("%.3f", avgPrice),
		"pnl", fmt.Sprintf("$%.2f", pnl),
		"paper", paperTag,
	)

	if err := b.SettleTrades(tokenID, finalPrice); err != nil {
		return fmt.Errorf("settle trades: %w", err)
	}
	if b.IsPaper() {
		if err := b.DeletePositionStrategy(tokenID, strategy); err != nil {
			return fmt.Errorf("delete position: %w", err)
		}
		if err := b.CreditWallet(strategy, recoveredAmount); err != nil {
			slog.Error("credit wallet", "strategy", strategy, "error", err)
		}
	} else {
		if err := b.DeletePosition(tokenID); err != nil {
			return fmt.Errorf("delete position: %w", err)
		}
	}
	if err := updateSnapshot(b, pnl, strategy); err != nil {
		return fmt.Errorf("update snapshot: %w", err)
	}

	if b.GetNotifier() != nil {
		sendNotification(b.GetNotifier(), question, slug, outcome, finalPrice, avgPrice, shares, pnl, b.IsPaper(), strategy)
	}

	return nil
}

func tokenPrice(marketData map[string]interface{}, tokenID string) float64 {
	tokens, ok := marketData["tokens"].([]map[string]interface{})
	if !ok {
		return -1
	}
	for _, m := range tokens {
		if tid, _ := m["token_id"].(string); tid == tokenID {
			p, _ := m["price"].(float64)
			return p
		}
	}
	return -1
}

func updateSnapshot(b *broker.Broker, pnl float64, strategy string) error {
	today := time.Now().UTC().Format("2006-01-02")

	database := b.GetDatabase()
	snapList, err := database.GetPNLSnapshotsByStrategy(strategy, 1000)
	if err != nil {
		return fmt.Errorf("get pnl snapshots: %w", err)
	}

	var existingRealized float64
	var existingTrades int
	for _, s := range snapList {
		if s.Date == today {
			existingRealized = s.Realized
			existingTrades = s.TotalTrades
			break
		}
	}

	return database.SavePNLSnapshot(&db.PNLSnapshot{
		Date:        today,
		Strategy:    strategy,
		Realized:    existingRealized + pnl,
		Unrealized:  0.0,
		TotalTrades: existingTrades + 1,
	})
}

func sendNotification(n interface{ Send(string) }, question, slug, outcome string, finalPrice, avgPrice, shares, pnl float64, paper bool, strategy string) {
	result := "✅ WIN"
	if pnl < 0 {
		result = "❌ LOSS"
	}

	mode := "💰 LIVE"
	if paper {
		mode = "📄 PAPER"
	}

	wonLost := "Lost ✗"
	if finalPrice == 1.0 {
		wonLost = "Won ✓"
	}

	var urlLine string
	if slug != "" {
		urlLine = fmt.Sprintf(
			"\n🔗 https://polymarket.com/market/%s",
			slug,
		)
	}

	msg := fmt.Sprintf(
		"%s — Position settled %s\n📊 %s\n📋 Strategy: %s\n-> Held: %s → %s\n-> Entry: %.3f  |  Shares: %.2f  |  P&L: $%.2f%s",
		mode, result, trunc(question, 80), strategy, outcome, wonLost, avgPrice, shares, pnl, urlLine,
	)
	n.Send(msg)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var _ interface{ Name() string } = (*SettlementStrategy)(nil)
