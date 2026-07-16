package copytrading

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/client/dataapi"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/strategy"
)

var _ dataapi.Position // ensure dataapi import is used
var _ dataapi.LeaderboardEntry

const deadTokenTTL = 3600

const clobREST = "https://clob.polymarket.com"

type CopyTradingStrategy struct {
	deadTokens map[string]int64
}

func New() *CopyTradingStrategy {
	return &CopyTradingStrategy{
		deadTokens: make(map[string]int64),
	}
}

func (s *CopyTradingStrategy) Name() string {
	return "copy_trading"
}

func (s *CopyTradingStrategy) Run(ctx context.Context, sctx *strategy.Context) error {
	cfg := sctx.Config.Strategies.CopyTrading
	if !cfg.Enabled {
		slog.Info("CopyTrading disabled")
		return nil
	}
	slog.Info("CopyTrading started", "poll", cfg.PollIntervalSeconds, "min_pnl", cfg.MinPnL)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(sctx); err != nil {
			slog.Error("CopyTrading tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(cfg.PollIntervalSeconds) * time.Second):
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

type candidatePos struct {
	pos    dataapi.Position
	trader dataapi.LeaderboardEntry
}

func (s *CopyTradingStrategy) tick(sctx *strategy.Context) error {
	cfg := sctx.Config.Strategies.CopyTrading
	risk := sctx.Config.Risk

	if !sctx.Paper {
		balance, err := sctx.Clob.GetBalance()
		if err != nil {
			slog.Warn("Could not fetch balance", "error", err)
		} else if balance < 1.0 {
			slog.Warn("CopyTrading skipping tick — USDC balance too low", "balance", balance)
			return nil
		}
	}

	topTraders, err := sctx.DataAPI.GetTopTraders(cfg.MinPnL, 20)
	if err != nil || len(topTraders) == 0 {
		slog.Info("No qualifying traders found")
		return nil
	}

	positions, err := sctx.DB.GetPositions()
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	myPositions := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		myPositions[p.TokenID] = struct{}{}
	}
	openCount := len(myPositions)

	slog.Info("Tick start", "traders", len(topTraders), "open", openCount)

	tokenSources := make(map[string][]sourceTrader)
	var candidates []candidatePos
	seenTokens := make(map[string]struct{})

	for _, trader := range topTraders {
		traderPositions, err := sctx.DataAPI.GetPositions(trader.Address)
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
			if _, seen := seenTokens[pos.TokenID]; !seen {
				seenTokens[pos.TokenID] = struct{}{}
				candidates = append(candidates, candidatePos{pos, trader})
			}
		}
	}

	marketCache := make(map[string]map[string]interface{})

	var totalPositionsSeen, skippedHeld, skippedDead int
	var skippedPrice, skippedTooFar, skippedNoPrice, skippedNoMarket int
	var openedThisTick int
	traderCopied := make(map[string]int)

	for _, c := range candidates {
		if openCount >= risk.MaxOpenPositions {
			break
		}
		if traderCopied[c.trader.Address] >= cfg.MaxCopyPerTrader {
			continue
		}

		pos := c.pos
		totalPositionsSeen++

		now := time.Now().Unix()
		if exp, ok := s.deadTokens[pos.TokenID]; ok && exp > now {
			skippedDead++
			continue
		}

		if _, held := myPositions[pos.TokenID]; held {
			skippedHeld++
			continue
		}

		if _, cached := marketCache[pos.MarketID]; !cached {
			marketCache[pos.MarketID] = fetchClobMarket(pos.MarketID)
		}
		mktData := marketCache[pos.MarketID]

		if mktData == nil {
			skippedNoMarket++
			s.deadTokens[pos.TokenID] = now + deadTokenTTL
			continue
		}

		if active, _ := mktData["active"].(bool); !active {
			skippedNoMarket++
			continue
		}

		if cfg.MaxDaysToResolution > 0 {
			if endDateStr, _ := mktData["end_date_iso"].(string); endDateStr != "" {
				endDt, err := time.Parse(time.RFC3339, endDateStr)
				if err == nil {
					daysLeft := time.Until(endDt).Hours() / 24
					if daysLeft > cfg.MaxDaysToResolution {
						skippedTooFar++
						continue
					}
				}
			}
		}

		buyPrice := 0.0
		buyOutcome := pos.Outcome
		if tokens, ok := mktData["tokens"].([]map[string]interface{}); ok {
			for _, t := range tokens {
				tid, _ := t["token_id"].(string)
				if tid == pos.TokenID {
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

		if buyPrice <= 0 {
			ob, err := sctx.Clob.GetOrderbook(pos.TokenID)
			if err == nil {
				if len(ob.Asks) > 0 {
					buyPrice = ob.Asks[0].Price
				} else if len(ob.Bids) > 0 {
					buyPrice = ob.Bids[0].Price
				}
			}
		}

		if buyPrice <= 0 {
			skippedNoPrice++
			continue
		}

		if !(cfg.PriceGuard < buyPrice && buyPrice < (1-cfg.PriceGuard)) {
			skippedPrice++
			continue
		}

		size := min(risk.MaxPositionUSDC, pos.Size*buyPrice)
		if size <= 0 {
			continue
		}

		orderResult, err := sctx.Clob.PlaceLimitOrder(clob.OrderArgs{
			TokenID:  pos.TokenID,
			Side:     "BUY",
			Price:    buyPrice,
			SizeUSDC: size,
		}, pos.MarketID)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "does not exist") || strings.Contains(strings.ToLower(msg), "orderbook") {
				s.deadTokens[pos.TokenID] = time.Now().Unix() + deadTokenTTL
				slog.Debug("Marking dead orderbook", "token", pos.TokenID[:12])
			} else if strings.Contains(msg, "not enough balance") || strings.Contains(msg, "balance is not enough") {
				slog.Warn("Insufficient USDC balance — aborting tick")
				return nil
			} else {
				slog.Error("Order error", "token", pos.TokenID, "error", err)
			}
			continue
		}

		var orderID string
		if orderResult != nil {
			if oid, ok := orderResult["orderID"].(string); ok {
				orderID = oid
			} else if oid, ok := orderResult["id"].(string); ok {
				orderID = oid
			}
		}

		sourceJSON := "[]"
		if sources, ok := tokenSources[pos.TokenID]; ok && len(sources) > 0 {
			if raw, err := json.Marshal(sources); err == nil {
				sourceJSON = string(raw)
			}
		}

		if _, dbErr := sctx.DB.RecordTrade(&db.Trade{
			Ts:         time.Now().Unix(),
			MarketID:   pos.MarketID,
			TokenID:    pos.TokenID,
			Side:       "BUY",
			Outcome:    buyOutcome,
			Price:      buyPrice,
			SizeUSDC:   size,
			OrderID:    orderID,
			Paper:      sctx.Paper,
			SourceJSON: sourceJSON,
		}); dbErr != nil {
			slog.Error("record trade", "error", dbErr)
		}

		if dbErr := sctx.DB.UpsertPosition(&db.Position{
			TokenID:   pos.TokenID,
			MarketID:  pos.MarketID,
			Outcome:   buyOutcome,
			Size:      size / buyPrice,
			AvgPrice:  buyPrice,
			UpdatedTs: time.Now().Unix(),
		}); dbErr != nil {
			slog.Error("upsert position", "error", dbErr)
		}

		question := pos.TokenID[:min(20, len(pos.TokenID))]
		if q, _ := mktData["question"].(string); q != "" {
			question = q
		}
		slug, _ := mktData["slug"].(string)

		if dbErr := sctx.DB.UpsertMarketMeta(&db.MarketMeta{
			ConditionID: pos.MarketID,
			Question:    question,
			Slug:        slug,
		}); dbErr != nil {
			slog.Error("upsert market meta", "error", dbErr)
		}

		paperTag := ""
		if sctx.Paper {
			paperTag = " [PAPER]"
		}
		slog.Info("Copied",
			"trader", c.trader.Address[:min(8, len(c.trader.Address))],
			"token", pos.TokenID[:min(8, len(pos.TokenID))],
			"outcome", buyOutcome,
			"price", buyPrice,
			"size", size,
			"sources", len(tokenSources[pos.TokenID]),
			"paper", paperTag,
		)

		if sctx.Notifier != nil {
			mode := "LIVE"
			if sctx.Paper {
				mode = "PAPER"
			}
			urlLine := ""
			if slug != "" {
				urlLine = fmt.Sprintf("\nhttps://polymarket.com/market/%s", slug)
			}
			truncQ := question
			if len(truncQ) > 80 {
				truncQ = truncQ[:80]
			}
			msg := fmt.Sprintf("%s — Position opened\n%s\n-> Outcome: %s\n-> Price: %.3f  |  Size: $%.2f  |  %d top traders%s",
				mode, truncQ, buyOutcome, buyPrice, size, len(tokenSources[pos.TokenID]), urlLine)
			sctx.Notifier.Send(msg)
		}

		traderCopied[c.trader.Address]++
		openCount++
		openedThisTick++
		myPositions[pos.TokenID] = struct{}{}
	}

	slog.Info("Tick end",
		"opened", openedThisTick,
		"total_seen", totalPositionsSeen,
		"held", skippedHeld,
		"dead", skippedDead,
		"no_market", skippedNoMarket,
		"no_price", skippedNoPrice,
		"price", skippedPrice,
		"too_far", skippedTooFar,
	)
	return nil
}

func fetchClobMarket(conditionID string) map[string]interface{} {
	url := clobREST + "/markets/" + conditionID
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "vagustrader/0.1")
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Debug("CLOB market fetch failed", "condition", conditionID[:min(12, len(conditionID))], "error", err)
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}

	endDate, _ := raw["end_date_iso"].(string)
	if endDate == "" {
		if v, ok := raw["endDate"].(string); ok {
			endDate = v
		}
	}
	question, _ := raw["question"].(string)
	slug, _ := raw["slug"].(string)
	active, _ := raw["active"].(bool)
	closed, _ := raw["closed"].(bool)

	var parsedTokens []map[string]interface{}
	if rawTokens, ok := raw["tokens"].([]interface{}); ok {
		for _, t := range rawTokens {
			tm, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			parsedTokens = append(parsedTokens, map[string]interface{}{
				"token_id": stringFromMap(tm, "token_id"),
				"price":    floatFromMap(tm, "price"),
				"outcome":  stringFromMap(tm, "outcome"),
			})
		}
	}

	return map[string]interface{}{
		"question":     question,
		"slug":         slug,
		"active":       active && !closed,
		"closed":       closed,
		"end_date_iso": endDate,
		"tokens":       parsedTokens,
	}
}

func stringFromMap(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func floatFromMap(m map[string]interface{}, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		f, _ := parseFloat(v)
		return f
	default:
		return 0
	}
}

func parseFloat(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}
