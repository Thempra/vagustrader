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
	broker      *broker.Broker
	deadTokens  map[string]int64
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
	cfg_ := b.GetConfig().Strategies.CopyTrading
	if !cfg_.Enabled {
		slog.Info("CopyTrading disabled")
		return nil
	}
	slog.Info("CopyTrading started", "poll", cfg_.PollIntervalSeconds, "min_pnl", cfg_.MinPnL)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(b); err != nil {
			slog.Error("CopyTrading tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(cfg_.PollIntervalSeconds) * time.Second):
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

func (s *CopyTradingStrategy) tick(b *broker.Broker) error {
	cfg_ := b.GetConfig().Strategies.CopyTrading
	risk := b.GetConfig().Risk

	if !b.IsPaper() {
		balance, err := b.GetBalance()
		if err != nil {
			slog.Warn("Could not fetch balance", "error", err)
		} else {
			slog.Info("Balance check", "balance", balance)
		}
	}

	topTraders, err := b.GetDataAPI().GetTopTraders(cfg_.MinPnL, 20)
	if err != nil || len(topTraders) == 0 {
		slog.Info("No qualifying traders found")
		return nil
	}

	positions, err := b.GetPositions()
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

	tokenSources := make(map[string][]sourceTrader)
	var candidates []candidatePos
	seenTokens := make(map[string]struct{})

	for _, trader := range topTraders {
		traderPositions, err := b.GetDataAPI().GetPositions(trader.Address)
		if err != nil {
			continue
		}
		if cfg_.MinTraderWinRate > 0 && len(traderPositions) > 0 {
			profitable := 0
			for _, tp := range traderPositions {
				if tp.CashPnl > 0 {
					profitable++
				}
			}
			winRate := float64(profitable) / float64(len(traderPositions))
			if winRate < cfg_.MinTraderWinRate {
				slog.Debug("Skipping trader — low win rate", "trader", trader.Address[:min(8, len(trader.Address))], "winRate", fmt.Sprintf("%.0f%%", winRate*100))
				continue
			}
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

	var totalPositionsSeen, skippedHeld, skippedDead int
	var skippedPrice, skippedTooFar, skippedNoPrice, skippedNoMarket, skippedSport, skippedBuy int
	var openedThisTick int
	traderCopied := make(map[string]int)

	for _, c := range candidates {
		if openCount >= risk.MaxOpenPositions {
			break
		}
		if traderCopied[c.trader.Address] >= cfg_.MaxCopyPerTrader {
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

		mktData := b.GetMarketData(pos.MarketID)

		if mktData == nil {
			skippedNoMarket++
			s.deadTokens[pos.TokenID] = now + deadTokenTTL
			continue
		}

		if active, _ := mktData["active"].(bool); !active {
			skippedNoMarket++
			continue
		}

		if cfg_.MaxDaysToResolution > 0 {
			if endDateStr, _ := mktData["end_date_iso"].(string); endDateStr != "" {
				endDt, err := time.Parse(time.RFC3339, endDateStr)
				if err == nil {
					daysLeft := time.Until(endDt).Hours() / 24
					if daysLeft > cfg_.MaxDaysToResolution {
						skippedTooFar++
						continue
					}
				}
			}
		}

		buyPrice := 0.0
		buyOutcome := pos.Outcome
		tickSize, _ := mktData["tick_size"].(string)
		if tickSize == "" {
			tickSize = "0.01"
		}
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
			ob, err := b.GetOrderbook(pos.TokenID)
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

		if !(cfg_.PriceGuard < buyPrice && buyPrice < (1-cfg_.PriceGuard)) {
			skippedPrice++
			continue
		}

		if cfg_.MinEntryPrice > 0 && buyPrice < cfg_.MinEntryPrice {
			skippedPrice++
			continue
		}
		if cfg_.MaxEntryPrice > 0 && buyPrice > cfg_.MaxEntryPrice {
			skippedPrice++
			continue
		}

		slug, _ := mktData["slug"].(string)
		sport := detectSport(slug, pos.Slug)
		if isBlocked(sport, cfg_.AllowedSports, cfg_.BlockedSports) {
			skippedSport++
			continue
		}

		size := getPositionSize(buyPrice, cfg_.SizeTiers, risk.MaxPositionUSDC)
		if size <= 0 {
			continue
		}

		if size*buyPrice < 1.0 {
			slog.Debug("Skipping — maker amount below $1 minimum", "token", pos.TokenID[:min(8, len(pos.TokenID))], "makerAmt", size*buyPrice)
			continue
		}

		negRisk, _ := mktData["neg_risk"].(bool)

		sourceJSON := "[]"
		if sources, ok := tokenSources[pos.TokenID]; ok && len(sources) > 0 {
			if raw, err := json.Marshal(sources); err == nil {
				sourceJSON = string(raw)
			}
		}

		result, buyErr := b.Buy(context.Background(), broker.BuyRequest{
			TokenID:    pos.TokenID,
			MarketID:   pos.MarketID,
			Outcome:    buyOutcome,
			Price:      buyPrice,
			SizeUSDC:   size,
			TickSize:   tickSize,
			NegRisk:    negRisk,
			SourceJSON: sourceJSON,
		})
		if buyErr != nil {
			if isDeadTokenError(buyErr) {
				s.deadTokens[pos.TokenID] = time.Now().Unix() + deadTokenTTL
				slog.Debug("Marking dead orderbook", "token", pos.TokenID[:min(12, len(pos.TokenID))])
			} else if strings.Contains(buyErr.Error(), "insufficient balance") {
				slog.Warn("Insufficient USDC balance — aborting tick")
				return nil
			} else {
				slog.Error("Buy error", "token", pos.TokenID[:min(12, len(pos.TokenID))], "error", buyErr)
			}
			skippedBuy++
			continue
		}

		question := pos.TokenID[:min(20, len(pos.TokenID))]
		if q, _ := mktData["question"].(string); q != "" {
			question = q
		}

		paperTag := ""
		if b.IsPaper() {
			paperTag = " [PAPER]"
		}
		slog.Info("Copied",
			"trader", c.trader.Address[:min(8, len(c.trader.Address))],
			"token", pos.TokenID[:min(8, len(pos.TokenID))],
			"outcome", buyOutcome,
			"price", result.Price,
			"size", size,
			"filled", result.FilledSize,
			"sources", len(tokenSources[pos.TokenID]),
			"paper", paperTag,
		)

		if b.GetNotifier() != nil {
			mode := "LIVE"
			if b.IsPaper() {
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
				mode, truncQ, buyOutcome, result.Price, size, len(tokenSources[pos.TokenID]), urlLine)
			b.Notify(msg)
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
		"sport", skippedSport,
		"buy_err", skippedBuy,
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
	return maxSize
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
