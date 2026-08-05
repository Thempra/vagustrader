package settlement

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/notify"
	"github.com/vagustrader/vagustrader/internal/strategy"
)

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

const (
	interval = 300 * time.Second
	clobRest = "https://clob.polymarket.com"
)

type SettlementStrategy struct{}

func (s *SettlementStrategy) Name() string { return "settlement" }

func (s *SettlementStrategy) Run(ctx context.Context, sctx *strategy.Context) error {
	slog.Info("Settlement checker started")
	for {
		if err := s.tick(sctx); err != nil {
			slog.Error("Settlement tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (s *SettlementStrategy) tick(sctx *strategy.Context) error {
	positions, err := sctx.DB.GetPositions()
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	for _, pos := range positions {
		if err := s.checkPosition(sctx, &pos); err != nil {
			tid := pos.TokenID
			if len(tid) > 12 {
				tid = tid[:12]
			}
			slog.Debug("Settlement error", "token", tid, "error", err)
		}
	}
	return nil
}

func (s *SettlementStrategy) checkPosition(sctx *strategy.Context, pos *db.Position) error {
	tokenID := pos.TokenID
	marketID := pos.MarketID
	avgPrice := pos.AvgPrice
	shares := pos.Size
	outcome := pos.Outcome

	clobData := fetchClobMarketData(marketID)
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

	question, slug, err := sctx.DB.GetMarketMeta(marketID)
	if err != nil {
		return fmt.Errorf("get market meta: %w", err)
	}
	if question == "" {
		if q, ok := clobData["question"].(string); ok && q != "" {
			question = q
			slug, _ = clobData["slug"].(string)
			if err := sctx.DB.UpsertMarketMeta(&db.MarketMeta{
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

	if !sctx.Paper && sctx.RPCURL != "" {
		negRisk, _ := clobData["neg_risk"].(bool)
		txHash, err := sctx.Clob.RedeemPosition(sctx.RPCURL, marketID, negRisk, shares)
		if err != nil {
			slog.Warn("Redeem failed (tokens may be in deposit wallet, not EOA)", "market", marketID[:min(12, len(marketID))], "error", err)
		} else {
			slog.Info("Redeemed position", "market", marketID[:min(12, len(marketID))], "txHash", txHash, "negRisk", negRisk, "shares", shares)
			if sctx.Notifier != nil {
				sctx.Notifier.Send(fmt.Sprintf("💰 Redeemed — %s\n→ Shares: %.2f  |  TX: %s", trunc(question, 60), shares, txHash))
			}
		}
	}

	paperTag := ""
	if sctx.Paper {
		paperTag = " [PAPER]"
	}
	slog.Info("Settled",
		"question", trunc(question, 50),
		"outcome", outcome,
		"final", fmt.Sprintf("%.0f", finalPrice),
		"avg", fmt.Sprintf("%.3f", avgPrice),
		"pnl", fmt.Sprintf("$%.2f", pnl),
		"paper", paperTag,
	)

	if err := sctx.DB.SettleTrades(tokenID, finalPrice); err != nil {
		return fmt.Errorf("settle trades: %w", err)
	}
	if err := sctx.DB.DeletePosition(tokenID); err != nil {
		return fmt.Errorf("delete position: %w", err)
	}
	if err := updateSnapshot(sctx.DB, pnl); err != nil {
		return fmt.Errorf("update snapshot: %w", err)
	}

	if sctx.Notifier != nil {
		sendNotification(sctx.Notifier, question, slug, outcome, finalPrice, avgPrice, shares, pnl, sctx.Paper)
	}

	return nil
}

func fetchClobMarketData(conditionID string) map[string]interface{} {
	url := clobRest + "/markets/" + conditionID
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		slog.Debug("CLOB REST request error", "condition", trunc(conditionID, 12), "error", err)
		return nil
	}
	req.Header.Set("User-Agent", "python-httpx/0.27.0")

	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("CLOB REST check failed", "condition", trunc(conditionID, 12), "error", err)
		return nil
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Debug("CLOB REST decode failed", "condition", trunc(conditionID, 12), "error", err)
		return nil
	}
	return result
}

func tokenPrice(marketData map[string]interface{}, tokenID string) float64 {
	tokens, ok := marketData["tokens"].([]interface{})
	if !ok {
		return -1
	}
	for _, t := range tokens {
		m, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		if tid, _ := m["token_id"].(string); tid == tokenID {
			p, _ := m["price"].(float64)
			return p
		}
	}
	return -1
}

func updateSnapshot(database *db.DB, pnl float64) error {
	today := time.Now().UTC().Format("2006-01-02")

	snapshots, err := database.GetPNLSnapshots(1000)
	if err != nil {
		return fmt.Errorf("get pnl snapshots: %w", err)
	}

	var existingRealized float64
	var existingTrades int
	for _, s := range snapshots {
		if s.Date == today {
			existingRealized = s.Realized
			existingTrades = s.TotalTrades
			break
		}
	}

	return database.SavePNLSnapshot(&db.PNLSnapshot{
		Date:        today,
		Realized:    existingRealized + pnl,
		Unrealized:  0.0,
		TotalTrades: existingTrades + 1,
	})
}

func sendNotification(n *notify.Notifier, question, slug, outcome string, finalPrice, avgPrice, shares, pnl float64, paper bool) {
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
			`\n🔗 <a href="https://polymarket.com/market/%s">%s</a>`,
			slug, trunc(question, 60),
		)
	}

	msg := fmt.Sprintf(
		"%s — Position settled %s\n📊 <b>%s</b>\n→ Held: <b>%s</b> → %s\n→ Entry: %.3f  |  Shares: %.2f  |  P&amp;L: <b>$%.2f</b>%s",
		mode, result, trunc(question, 80), outcome, wonLost, avgPrice, shares, pnl, urlLine,
	)
	n.Send(msg)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Ensure SettlementStrategy implements strategy.Strategy at compile time.
var _ strategy.Strategy = (*SettlementStrategy)(nil)
