package broker

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/client/dataapi"
	"github.com/vagustrader/vagustrader/internal/client/gamma"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/notify"
)

const clobREST = "https://clob.polymarket.com"

// marketCacheTTL bounds how long a cached market document is considered fresh.
// 60s matches Polymarket's typical price-update cadence and prevents the 5
// concurrent strategy goroutines from hammering the CLOB endpoint on every
// tick. Also defends against the stale-cache trap where one strategy observes
// a market as active and another, moments later, misses the close flag.
const marketCacheTTL = 60 * time.Second

type marketCacheEntry struct {
	data      map[string]interface{}
	fetchedAt time.Time
}

type Broker struct {
	clob        clob.Client
	dataAPI     *dataapi.Client
	gamma       *gamma.Client
	database    *db.DB
	config      *cfg.AppConfig
	paper       bool
	notifier    *notify.Notifier

	marketMu    sync.RWMutex
	marketCache map[string]marketCacheEntry
}

func New(
	clobClient clob.Client,
	dataAPI *dataapi.Client,
	gammaClient *gamma.Client,
	database *db.DB,
	config *cfg.AppConfig,
	paper bool,
	notifier *notify.Notifier,
) *Broker {
	return &Broker{
		clob:        clobClient,
		dataAPI:     dataAPI,
		gamma:       gammaClient,
		database:    database,
		config:      config,
		paper:       paper,
		notifier:    notifier,
		marketCache: make(map[string]marketCacheEntry),
	}
}

func (b *Broker) GetDataAPI() *dataapi.Client   { return b.dataAPI }
func (b *Broker) GetGamma() *gamma.Client        { return b.gamma }
func (b *Broker) GetConfig() *cfg.AppConfig      { return b.config }
func (b *Broker) IsPaper() bool                  { return b.paper }
func (b *Broker) GetNotifier() *notify.Notifier   { return b.notifier }
func (b *Broker) GetDatabase() *db.DB             { return b.database }

func (b *Broker) GetPositions() ([]db.Position, error) {
	return b.database.GetPositions()
}

func (b *Broker) GetPositionsByStrategy(strategy string) ([]db.Position, error) {
	return b.database.GetPositionsByStrategy(strategy)
}

func (b *Broker) IsHeld(tokenID string) bool {
	pos, err := b.database.GetPositions()
	if err != nil {
		return false
	}
	for _, p := range pos {
		if p.TokenID == tokenID {
			return true
		}
	}
	return false
}

func (b *Broker) IsHeldByStrategy(tokenID, strategy string) bool {
	pos, err := b.database.GetPositionsByStrategy(strategy)
	if err != nil {
		return false
	}
	for _, p := range pos {
		if p.TokenID == tokenID {
			return true
		}
	}
	return false
}

func (b *Broker) GetBalance() (float64, error) {
	return b.clob.GetBalance()
}

func (b *Broker) GetStrategyBalance(strategy string) (float64, error) {
	wallet, err := b.database.GetWallet(strategy)
	if err != nil {
		return 0, err
	}
	return wallet.Balance, nil
}

func (b *Broker) DeletePositionStrategy(tokenID, strategy string) error {
	return b.database.DeletePositionStrategy(tokenID, strategy)
}

func (b *Broker) CreditWallet(strategy string, amount float64) error {
	return b.database.CreditWallet(strategy, amount)
}

// ClearMarketCache is retained for backwards compatibility with strategies
// that explicitly invalidate the cache at tick boundaries. With TTL-based
// caching (P1.1) this is effectively a no-op of historical interest; callers
// no longer need to invoke it.
func (b *Broker) ClearMarketCache() {
	b.marketMu.Lock()
	b.marketCache = make(map[string]marketCacheEntry)
	b.marketMu.Unlock()
}

func (b *Broker) GetMarketData(conditionID string) map[string]interface{} {
	// Fast path: read-lock check for a fresh entry.
	b.marketMu.RLock()
	if entry, ok := b.marketCache[conditionID]; ok && time.Since(entry.fetchedAt) < marketCacheTTL {
		b.marketMu.RUnlock()
		return entry.data
	}
	b.marketMu.RUnlock()

	// Slow path: fetch outside the lock so concurrent callers don't serialize
	// on HTTP I/O. The write below is guarded; last writer wins, which is fine
	// because the upstream CLOB response is deterministic for a given
	// conditionID at a given instant.
	result := fetchClobMarket(conditionID)
	if result != nil {
		b.marketMu.Lock()
		b.marketCache[conditionID] = marketCacheEntry{data: result, fetchedAt: time.Now()}
		b.marketMu.Unlock()
	}
	return result
}

func (b *Broker) GetOrderbook(tokenID string) (*clob.Orderbook, error) {
	return b.clob.GetOrderbook(tokenID)
}

func (b *Broker) Notify(msg string) {
	if b.notifier != nil {
		b.notifier.Send(msg)
	}
}

func (b *Broker) SettleTrades(tokenID string, finalPrice float64) error {
	return b.database.SettleTrades(tokenID, finalPrice)
}

// SettleTradesByStrategy settles only trades belonging to (tokenID, strategy).
// Use this in multi-strategy settlement to avoid double-counting P&L when two
// strategies hold the same token. See P1.4.
func (b *Broker) SettleTradesByStrategy(tokenID, strategy string, finalPrice float64) error {
	return b.database.SettleTradesByStrategy(tokenID, strategy, finalPrice)
}

func (b *Broker) DeletePosition(tokenID string) error {
	return b.database.DeletePosition(tokenID)
}

func (b *Broker) GetMarketMeta(conditionID string) (string, string, error) {
	return b.database.GetMarketMeta(conditionID)
}

func (b *Broker) UpsertMarketMeta(m *db.MarketMeta) error {
	return b.database.UpsertMarketMeta(m)
}

func fetchClobMarket(conditionID string) map[string]interface{} {
	u := clobREST + "/markets/" + conditionID
	req, err := http.NewRequest(http.MethodGet, u, nil)
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
	tickSize, _ := raw["tick_size"].(string)

	var parsedTokens []map[string]interface{}
	if rawTokens, ok := raw["tokens"].([]interface{}); ok {
		for _, t := range rawTokens {
			tm, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			parsedTokens = append(parsedTokens, map[string]interface{}{
				"token_id": stringVal(tm, "token_id"),
				"price":    floatVal(tm, "price"),
				"outcome":  stringVal(tm, "outcome"),
			})
		}
	}

	negRisk, _ := raw["neg_risk"].(bool)

	return map[string]interface{}{
		"question":     question,
		"slug":         slug,
		"active":       active && !closed,
		"closed":       closed,
		"end_date_iso": endDate,
		"tick_size":    tickSize,
		"neg_risk":     negRisk,
		"tokens":       parsedTokens,
	}
}

func stringVal(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func floatVal(m map[string]interface{}, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		var f float64
		fmt.Sscanf(v, "%f", &f)
		return f
	default:
		return 0
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func snapToTick(price float64, tickSize string) float64 {
	var tick float64
	fmt.Sscanf(tickSize, "%f", &tick)
	if tick <= 0 {
		tick = 0.01
	}
	steps := math.Round(price / tick)
	snapped := steps * tick
	return math.Round(snapped*1e8) / 1e8
}

func isNoBalanceError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not enough balance") || strings.Contains(msg, "balance is not enough")
}
