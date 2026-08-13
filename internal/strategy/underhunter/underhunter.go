package underhunter

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/strategy/kelly"
)

const clobREST = "https://clob.polymarket.com"

type UnderHunterStrategy struct {
	deadTokens map[string]int64
}

func New() *UnderHunterStrategy {
	return &UnderHunterStrategy{deadTokens: make(map[string]int64)}
}

func (s *UnderHunterStrategy) Name() string { return "under_hunter" }

func (s *UnderHunterStrategy) Run(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.UnderHunter
	if !config.Enabled {
		slog.Info("UnderHunter disabled")
		return nil
	}
	slog.Info("UnderHunter started", "poll", config.PollIntervalSeconds, "price_range", fmt.Sprintf("%.2f-%.2f", config.MinPrice, config.MaxPrice))

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.tick(ctx, b); err != nil {
			slog.Error("UnderHunter tick error", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(config.PollIntervalSeconds) * time.Second):
		}
	}
}

func (s *UnderHunterStrategy) tick(ctx context.Context, b *broker.Broker) error {
	config := b.GetConfig().Strategies.UnderHunter
	risk := b.GetConfig().Risk

	gamma := b.GetGamma()
	if gamma == nil {
		return fmt.Errorf("gamma client not available")
	}

	markets, err := gamma.GetMarkets(500, 0, true, true)
	if err != nil {
		return fmt.Errorf("fetch markets: %w", err)
	}

	positions, err := b.GetPositionsByStrategy("under_hunter")
	if err != nil {
		return fmt.Errorf("get positions: %w", err)
	}
	held := make(map[string]struct{}, len(positions))
	for _, p := range positions {
		held[p.TokenID] = struct{}{}
	}

	var scanned, skippedNotOU, skippedPrice, skippedSport, skippedHeld, skippedDead, opened int
	now := time.Now().Unix()

	for _, m := range markets {
		isOU := false
		for _, o := range m.Outcomes {
			if strings.EqualFold(o, "Under") || strings.EqualFold(o, "Over") {
				isOU = true
				break
			}
		}
		if !isOU {
			skippedNotOU++
			continue
		}
		scanned++

		for i, outcome := range m.Outcomes {
			if !strings.EqualFold(outcome, "Under") {
				continue
			}

			if i >= len(m.OutcomePrices) || i >= len(m.TokenIDs) {
				continue
			}

			price := m.OutcomePrices[i]
			tokenID := m.TokenIDs[i]

			if price < config.MinPrice || price > config.MaxPrice {
				skippedPrice++
				continue
			}

			slug := m.Slug
			sportPrefix := detectSportPrefix(slug)
			if !isAllowedSport(sportPrefix, config.AllowedSports) {
				skippedSport++
				continue
			}

			if _, h := held[tokenID]; h {
				skippedHeld++
				continue
			}

			if exp, ok := s.deadTokens[tokenID]; ok && exp > now {
				skippedDead++
				continue
			}

			winProb := kelly.EstimateWinProb(price, sportPrefix, "under", 0)
			size := kelly.OptimalSize(kelly.EdgeEstimate{
				WinProb:    winProb,
				Payout:     price,
				LossAmount: price,
			}, risk.MaxPositionUSDC*10, config.MaxPosition, config.KellyFraction)

			if size <= 0 {
				continue
			}

			tickSize := "0.01"
			mktData := b.GetMarketData(m.ConditionID)
			if mktData != nil {
				if ts, ok := mktData["tick_size"].(string); ok && ts != "" {
					tickSize = ts
				}
			}

			negRisk := false
			if mktData != nil {
				negRisk, _ = mktData["neg_risk"].(bool)
			}

			result, buyErr := b.Buy(ctx, broker.BuyRequest{
				TokenID:  tokenID,
				MarketID: m.ConditionID,
				Outcome:  "Under",
				Price:    price,
				SizeUSDC: size,
				TickSize: tickSize,
				NegRisk:  negRisk,
				Strategy: "under_hunter",
			})
			if buyErr != nil {
				if isDeadErr(buyErr) {
					s.deadTokens[tokenID] = now + 3600
				} else if strings.Contains(buyErr.Error(), "insufficient balance") {
					slog.Warn("UnderHunter: insufficient balance")
					return nil
				} else {
					slog.Error("UnderHunter buy error", "token", tokenID[:minL(12, len(tokenID))], "error", buyErr)
				}
				continue
			}

			question := m.Question
			if len(question) > 60 {
				question = question[:60]
			}
			paperTag := ""
			if b.IsPaper() {
				paperTag = " [PAPER]"
			}
			slog.Info("UnderHunter opened",
				"question", question,
				"price", price,
				"size", size,
				"filled", result.FilledSize,
				"sport", sportPrefix,
				"winProb", fmt.Sprintf("%.0f%%", winProb*100),
				"paper", paperTag,
			)

			if b.GetNotifier() != nil {
				mode := "LIVE"
				if b.IsPaper() {
					mode = "PAPER"
				}
				msg := fmt.Sprintf("%s [UnderHunter] %s\n-> Under @ %.3f | $%.2f | est %.0f%% win",
					mode, question, price, size, winProb*100)
				b.Notify(msg)
			}

			opened++
			held[tokenID] = struct{}{}
		}
	}

	slog.Info("UnderHunter tick end",
		"scanned", scanned,
		"not_ou", skippedNotOU,
		"price", skippedPrice,
		"sport", skippedSport,
		"held", skippedHeld,
		"dead", skippedDead,
		"opened", opened,
	)
	return nil
}

func detectSportPrefix(slug string) string {
	if slug == "" {
		return ""
	}
	lower := strings.ToLower(slug)
	dashIdx := strings.Index(lower, "-")
	if dashIdx > 0 {
		return lower[:dashIdx]
	}
	return ""
}

func isAllowedSport(sport string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if strings.EqualFold(a, sport) {
			return true
		}
	}
	return false
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

var _ sportChecker = (*UnderHunterStrategy)(nil)

type sportChecker interface{ Name() string }

func init() {
	_ = math.Round
	_ = cfg.SizeTier{}
}
