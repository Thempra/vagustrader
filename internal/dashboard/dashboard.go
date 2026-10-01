package dashboard

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	_ "github.com/mattn/go-sqlite3"
	"github.com/vagustrader/vagustrader/internal/cfg"
	bdb "github.com/vagustrader/vagustrader/internal/db"
)

//go:embed static/*
var staticFS embed.FS

// CSS class names and HTTP header names repeated across the HTML builders.
const (
	classValPos       = "val-pos"
	classValNeg       = "val-neg"
	classBadgeYes     = "badge-yes"
	classBadgeNo      = "badge-no"
	headerContentType = "Content-Type"
	ellipsis          = "…"
)

type DashboardData struct {
	Positions         []PositionView
	RecentTrades      []TradeView
	TodayTrades       []TradeView
	Snapshots         []SnapshotView
	SnapshotsReversed []SnapshotView
	CurrentValues     map[string]float64

	ActiveStrategy string
	Strategies     []StrategyTab
	Wallet         *bdb.PaperWallet

	PositionCount  int
	TradeCount     int
	TradeDisplayed int
	TotalCost      float64
	RealizedPnL    float64
	UnrealizedPnL  float64
	TotalPnL       float64
	LiveToday      int
	PaperToday     int
	InvestedToday  float64
	Today          string
	Now            string
	CSS            string
	JS             string

	PositionRows template.HTML
	BalanceRows  template.HTML
	SnapCells    template.HTML

	StatsOverview     template.HTML
	TraderStatsRows   template.HTML
	TraderDetailRows  template.HTML
	PriceStatsRows    template.HTML
	DowStatsRows      template.HTML
	HourStatsRows     template.HTML
	MarketStatsRows   template.HTML
	PaperLiveStatsRow template.HTML
	StrategyStatsRows template.HTML

	PnLTimelineLabels template.JS
	PnLTimelineData   template.JS
	TraderChartLabels template.JS
	TraderChartPnL    template.JS
	TraderChartWins   template.JS
	PriceChartLabels  template.JS
	PriceChartPnL     template.JS
	DowChartLabels    template.JS
	DowChartPnL       template.JS
	HourChartLabels   template.JS
	HourChartPnL      template.JS
	WinLossData       template.JS
	PerfAvgWin        float64
	PerfAvgLoss       float64
	PerfProfitFactor  float64
	PerfStreakWins    int
	PerfStreakLosses  int

	ConfigHTML  template.HTML
	DBPath      string
	LastUpdated string
}

type PositionView struct {
	TokenID      string
	MarketID     string
	Outcome      string
	Size         float64
	AvgPrice     float64
	UpdatedTs    int64
	Question     string
	Slug         string
	Cost         float64
	CurrentValue float64
	PnL          float64
	HasPrice     bool
	SourceJSON   string
}

type TradeView struct {
	ID         int64
	Ts         int64
	MarketID   string
	TokenID    string
	Side       string
	Outcome    string
	Price      float64
	SizeUSDC   float64
	Paper      bool
	Result     sql.NullFloat64
	ResultStr  string
	Question   string
	Slug       string
	DateStr    string
	HasResult  bool
	Recovered  float64
	Gain       float64
	SourceJSON string
	Strategy   string
}

type SnapshotView struct {
	Date        string
	DateShort   string
	Realized    float64
	Unrealized  float64
	TotalTrades int
}

type StrategyTab struct {
	Name        string
	DisplayName string
	Balance     float64
}

var gammaBase = "https://gamma-api.polymarket.com"
var clobBase = "https://clob.polymarket.com"
var priceClient = &http.Client{Timeout: 4 * time.Second}
var metaClient = &http.Client{Timeout: 6 * time.Second}

type StrategyCache struct {
	html    []byte
	csvData []byte
	data    *DashboardData
}

var (
	cacheMu    sync.RWMutex
	stratCache = make(map[string]*StrategyCache)
)

func fetchMetaByToken(tokenID string) (conditionID, question, slug string) {
	url := fmt.Sprintf("%s/markets?clob_token_ids=%s", gammaBase, tokenID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", "", ""
	}
	req.Header.Set("User-Agent", "vagustrader/1.0")
	resp, err := metaClient.Do(req)
	if err != nil {
		return "", "", ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", ""
	}

	var raw struct {
		Data []json.RawMessage `json:"data"`
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		if err2 := json.Unmarshal(body, &items); err2 != nil {
			return "", "", ""
		}
	} else {
		items = raw.Data
	}

	if len(items) == 0 {
		return "", "", ""
	}

	var obj map[string]interface{}
	if err := json.Unmarshal(items[0], &obj); err != nil {
		return "", "", ""
	}

	getStr := func(key string) string {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
		return ""
	}

	conditionID = getStr("conditionId")
	question = getStr("question")
	slug = getStr("slug")
	return conditionID, question, slug
}

func fetchTokenPrice(tokenID, conditionID string) float64 {
	url := fmt.Sprintf("%s/markets/%s", clobBase, conditionID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return -1
	}
	req.Header.Set("User-Agent", "vagustrader/1.0")
	resp, err := priceClient.Do(req)
	if err != nil {
		return -1
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return -1
	}

	var data struct {
		Tokens []struct {
			TokenID string `json:"token_id"`
			Price   string `json:"price"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return -1
	}

	for _, t := range data.Tokens {
		if t.TokenID == tokenID {
			p, err := strconv.ParseFloat(t.Price, 64)
			if err != nil {
				return -1
			}
			return p
		}
	}
	return -1
}

// loadOpenPositions reads open positions for the strategy and stamps live
// market prices onto them.
func loadOpenPositions(db *sql.DB, data *DashboardData, strategy string) {
	posRows, err := db.Query(`
		SELECT p.token_id, p.market_id, p.outcome, p.size, p.avg_price, p.updated_ts,
		       COALESCE(m.question, ''), COALESCE(m.slug, ''),
		       COALESCE((
		           SELECT t.source_json FROM trades t
		           WHERE t.token_id = p.token_id AND t.source_json IS NOT NULL AND t.source_json != ''
		           ORDER BY t.ts DESC LIMIT 1
		       ), '')
		FROM positions p
		LEFT JOIN market_meta m ON p.market_id = m.condition_id
		WHERE p.strategy = ?
		ORDER BY p.updated_ts DESC
	`, strategy)
	if err == nil {
		defer posRows.Close()
		scanPositions(posRows, data)
	}
	data.PositionCount = len(data.Positions)

	applyLivePrices(data, fetchTokenPricesParallel(data.Positions))
}

// loadDayTrades folds the trades placed since UTC midnight into the counters.
func loadDayTrades(db *sql.DB, data *DashboardData, strategy string, startTs, endTs int64) {
	todayRows, err := db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, ''), COALESCE(strategy, '') FROM trades WHERE strategy = ? AND ts >= ? AND ts < ?", strategy, startTs, endTs)
	if err != nil {
		return
	}
	defer todayRows.Close()
	scanTodayTrades(todayRows, data)
}

// loadSettledTrades reads every settled trade joined with its market meta.
func loadSettledTrades(db *sql.DB, data *DashboardData, strategy string) {
	tradeRows, err := db.Query(`
		SELECT t.id, t.ts, t.market_id, t.token_id, t.side, COALESCE(t.outcome, ''),
		       t.price, t.size_usdc,
		       COALESCE(t.order_id, ''), t.paper, t.result,
		       COALESCE(t.source_json, ''),
		       COALESCE(m.question, ''), COALESCE(m.slug, ''),
		       COALESCE(t.strategy, '')
		FROM trades t
		LEFT JOIN market_meta m ON t.market_id = m.condition_id
		WHERE t.result IS NOT NULL AND t.strategy = ?
		ORDER BY t.ts
	`, strategy)
	if err != nil {
		return
	}
	defer tradeRows.Close()
	scanSettledTrades(tradeRows, data)
}

// scanPositions folds every open-position row into the dashboard payload.
func scanPositions(posRows *sql.Rows, data *DashboardData) {
	for posRows.Next() {
		var pv PositionView
		if err := posRows.Scan(&pv.TokenID, &pv.MarketID, &pv.Outcome, &pv.Size, &pv.AvgPrice, &pv.UpdatedTs, &pv.Question, &pv.Slug, &pv.SourceJSON); err != nil {
			continue
		}
		pv.Cost = pv.Size * pv.AvgPrice
		data.TotalCost += pv.Cost
		data.Positions = append(data.Positions, pv)
	}
}

// applyLivePrices stamps the latest market price onto each open position.
func applyLivePrices(data *DashboardData, prices map[string]float64) {
	for i := range data.Positions {
		p := &data.Positions[i]
		if price, ok := prices[p.TokenID]; ok {
			p.CurrentValue = p.Size * price
			p.PnL = p.CurrentValue - p.Cost
			p.HasPrice = true
			data.CurrentValues[p.TokenID] = price
			data.UnrealizedPnL += p.PnL
		}
	}
}

// settleTradeView fills in the derived display fields of a settled trade.
func settleTradeView(tv *TradeView, paper int) {
	tv.Paper = paper == 1
	tv.DateStr = time.Unix(tv.Ts, 0).UTC().Format("2006-01-02 15:04")
	if !tv.Result.Valid {
		tv.ResultStr = "OPEN"
		return
	}
	tv.HasResult = true
	shares := tv.SizeUSDC / tv.Price
	recovered := shares * tv.Result.Float64
	tv.Recovered = recovered
	tv.Gain = recovered - tv.SizeUSDC
	if tv.Result.Float64 >= 0.99 {
		tv.ResultStr = "WIN"
	} else {
		tv.ResultStr = "LOSS"
	}
}

// scanTodayTrades folds the day's trades into the today counters.
func scanTodayTrades(todayRows *sql.Rows, data *DashboardData) {
	for todayRows.Next() {
		var tv TradeView
		var paper int
		if err := todayRows.Scan(&tv.ID, &tv.Ts, &tv.MarketID, &tv.TokenID, &tv.Side, &tv.Outcome, &tv.Price, &tv.SizeUSDC, new(string), &paper, &tv.Result, &tv.SourceJSON, &tv.Strategy); err != nil {
			continue
		}
		tv.Paper = paper == 1
		data.InvestedToday += tv.SizeUSDC
		if tv.Paper {
			data.PaperToday++
		} else {
			data.LiveToday++
		}
	}
}

// scanSettledTrades folds every settled trade (joined with market meta).
func scanSettledTrades(tradeRows *sql.Rows, data *DashboardData) {
	for tradeRows.Next() {
		var tv TradeView
		var paper int
		if err := tradeRows.Scan(&tv.ID, &tv.Ts, &tv.MarketID, &tv.TokenID, &tv.Side, &tv.Outcome, &tv.Price, &tv.SizeUSDC, new(string), &paper, &tv.Result, &tv.SourceJSON, &tv.Question, &tv.Slug, &tv.Strategy); err != nil {
			continue
		}
		settleTradeView(&tv, paper)
		data.RecentTrades = append(data.RecentTrades, tv)
	}
}

func fetchTokenPricesParallel(positions []PositionView) map[string]float64 {
	result := make(map[string]float64)
	var mu sync.Mutex
	var wg sync.WaitGroup

	sem := make(chan struct{}, 12)

	for _, p := range positions {
		if p.MarketID == "" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(tokenID, marketID string) {
			defer wg.Done()
			defer func() { <-sem }()
			price := fetchTokenPrice(tokenID, marketID)
			if price >= 0 {
				mu.Lock()
				result[tokenID] = price
				mu.Unlock()
			}
		}(p.TokenID, p.MarketID)
	}
	wg.Wait()
	return result
}

func backfillMeta(db *sql.DB) {
	rows, err := db.Query(`
		SELECT p.token_id, p.market_id FROM positions p
		LEFT JOIN market_meta m ON p.market_id = m.condition_id
		WHERE m.condition_id IS NULL OR m.slug = ''
		UNION
		SELECT t.token_id, t.market_id FROM trades t
		LEFT JOIN market_meta m ON t.market_id = m.condition_id
		WHERE m.condition_id IS NULL OR m.slug = ''
	`)
	if err != nil {
		return
	}
	defer rows.Close()

	type pair struct {
		tokenID     string
		conditionID string
	}
	var missing []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.tokenID, &p.conditionID); err != nil {
			continue
		}
		missing = append(missing, p)
	}

	limit := 4
	if len(missing) < limit {
		limit = len(missing)
	}
	var wg sync.WaitGroup
	for i := 0; i < limit; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cid, question, slug := fetchMetaByToken(missing[idx].tokenID)
			if question != "" && cid != "" {
				db.Exec(
					"INSERT INTO market_meta (condition_id, question, slug) VALUES (?, ?, ?) ON CONFLICT(condition_id) DO UPDATE SET question=excluded.question, slug=excluded.slug",
					cid, question, slug,
				)
			}
		}(i)
	}
	wg.Wait()
}

var configPath string

type strategyDef struct {
	Key         string
	DisplayName string
}

func enabledStrategies() []strategyDef {
	ac := cfg.LoadAppConfig(configPath)
	var out []strategyDef
	if ac.Strategies.CopyTrading.Enabled {
		out = append(out, strategyDef{"copy_trading", "Copy Trading"})
	}
	if ac.Strategies.UnderHunter.Enabled {
		out = append(out, strategyDef{"under_hunter", "Under Hunter"})
	}
	if ac.Strategies.WhaleConsensus.Enabled {
		out = append(out, strategyDef{"whale_consensus", "Whale Consensus"})
	}
	if ac.Strategies.Arbitrage.Enabled {
		out = append(out, strategyDef{"arbitrage", "Arbitrage"})
	}
	if ac.Strategies.Contrarian.Enabled {
		out = append(out, strategyDef{"contrarian", "Contrarian"})
	}
	if ac.Strategies.ResolutionSniper.Enabled {
		out = append(out, strategyDef{"resolution_sniper", "Resolution Sniper"})
	}
	if ac.Strategies.MomentumRider.Enabled {
		out = append(out, strategyDef{"momentum_rider", "Momentum Rider"})
	}
	if ac.Strategies.DeepValue.Enabled {
		out = append(out, strategyDef{"deep_value", "Deep Value"})
	}
	if ac.Strategies.VolumeSurge.Enabled {
		out = append(out, strategyDef{"volume_surge", "Volume Surge"})
	}
	if len(out) == 0 {
		out = []strategyDef{{"copy_trading", "Copy Trading"}}
	}
	return out
}

func resolveStrategy(reqStrategy string) string {
	defs := enabledStrategies()
	for _, d := range defs {
		if d.Key == reqStrategy {
			return reqStrategy
		}
	}
	if len(defs) > 0 {
		return defs[0].Key
	}
	return "copy_trading"
}

func buildStrategyTabs(database *bdb.DB) []StrategyTab {
	defs := enabledStrategies()
	wallets, _ := database.GetAllWallets()
	bal := make(map[string]float64)
	for _, w := range wallets {
		bal[w.Strategy] = w.Balance
	}
	tabs := make([]StrategyTab, 0, len(defs))
	for _, d := range defs {
		tabs = append(tabs, StrategyTab{Name: d.Key, DisplayName: d.DisplayName, Balance: bal[d.Key]})
	}
	return tabs
}

func query(dbPath, strategy string) *DashboardData {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Printf("dashboard: open db: %v", err)
		return &DashboardData{ActiveStrategy: strategy}
	}
	defer db.Close()

	database := &bdb.DB{DB: db}

	data := &DashboardData{
		CurrentValues:  make(map[string]float64),
		ActiveStrategy: strategy,
		Today:          time.Now().UTC().Format("2006-01-02"),
		Now:            time.Now().UTC().Format("15:04:05") + " UTC",
	}

	loadOpenPositions(db, data, strategy)

	today := time.Now().UTC()
	startOfDay := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	startTs := startOfDay.Unix()
	endTs := startOfDay.Add(24 * time.Hour).Unix()

	loadDayTrades(db, data, strategy, startTs, endTs)
	loadSettledTrades(db, data, strategy)
	data.TradeCount = len(data.RecentTrades)
	data.TradeDisplayed = len(data.RecentTrades)
	if data.TradeDisplayed > 500 {
		data.TradeDisplayed = 500
	}

	if snaps, err := database.GetPNLSnapshotsByStrategy(strategy, 100000); err == nil {
		for _, s := range snaps {
			sv := SnapshotView{Date: s.Date, Realized: s.Realized, Unrealized: s.Unrealized, TotalTrades: s.TotalTrades}
			if len(sv.Date) >= 5 {
				sv.DateShort = sv.Date[5:]
			}
			data.Snapshots = append(data.Snapshots, sv)
		}
	}
	if rp, err := database.GetRealizedPnLByStrategy(strategy); err == nil {
		data.RealizedPnL = rp
	}

	snapDisplay := data.Snapshots
	if len(snapDisplay) > 7 {
		snapDisplay = snapDisplay[:7]
	}
	for i := len(snapDisplay) - 1; i >= 0; i-- {
		data.SnapshotsReversed = append(data.SnapshotsReversed, snapDisplay[i])
	}

	data.TotalPnL = data.RealizedPnL + data.UnrealizedPnL

	displayTrades := data.RecentTrades
	if len(displayTrades) > 500 {
		displayTrades = displayTrades[len(displayTrades)-500:]
	}
	for i, j := 0, len(displayTrades)-1; i < j; i, j = i+1, j-1 {
		displayTrades[i], displayTrades[j] = displayTrades[j], displayTrades[i]
	}
	data.PositionRows = renderPositionRows(data.Positions)
	data.BalanceRows = renderBalanceRows(displayTrades)
	data.SnapCells = renderSnapCells(data.SnapshotsReversed)

	computeAndRenderStats(data)

	data.Wallet, _ = database.GetWallet(strategy)
	data.Strategies = buildStrategyTabs(database)

	data.DBPath = dbPath
	data.LastUpdated = time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
	data.ConfigHTML = readConfig(configPath)

	return data
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + ellipsis
}

func pnlClass(val interface{}) string {
	switch v := val.(type) {
	case float64:
		if v > 0 {
			return "green"
		}
		if v < 0 {
			return "red"
		}
	case int:
		if v > 0 {
			return "green"
		}
		if v < 0 {
			return "red"
		}
	}
	return "blue"
}

func outcomeBadge(outcome string) string {
	cls := classBadgeYes
	outcomeLower := strings.ToLower(outcome)
	if outcomeLower == "no" || outcomeLower == "false" {
		cls = classBadgeNo
	}
	return fmt.Sprintf(`<span class="badge-outcome %s">%s</span>`, cls, outcome)
}

func marketLink(question, slug string) template.HTML {
	if question != "" && slug != "" {
		label := truncate(question, 48)
		return template.HTML(fmt.Sprintf(`<a href="https://polymarket.com/market/%s" target="_blank" class="mkt-link">%s</a>`, slug, label))
	}
	if question != "" {
		return template.HTML(truncate(question, 48))
	}
	return template.HTML(`<span class="text-secondary">&mdash;</span>`)
}

func resultBadge(result string) template.HTML {
	switch result {
	case "WIN":
		return template.HTML(`<span class="result-win">WIN</span>`)
	case "LOSS":
		return template.HTML(`<span class="result-loss">LOSS</span>`)
	default:
		return template.HTML(`<span class="result-open">&mdash;</span>`)
	}
}

func snapPnlClass(val float64) string {
	if val >= 0 {
		return "snap-pos"
	}
	return "snap-neg"
}

func buildTemplate() *template.Template {
	tmplBytes, err := staticFS.ReadFile("static/template.html")
	if err != nil {
		log.Fatalf("dashboard: read template: %v", err)
	}

	funcMap := template.FuncMap{
		"pnlClass":     pnlClass,
		"outcomeBadge": outcomeBadge,
		"marketLink":   marketLink,
		"resultBadge":  resultBadge,
		"snapPnlClass": snapPnlClass,
		"printf":       fmt.Sprintf,
		"add":          func(a, b int) int { return a + b },
		"sub":          func(a, b int) int { return a - b },
		"mul":          func(a, b int) int { return a * b },
		"div":          func(a, b int) int { return a / b },
		"gt":           func(a, b int) bool { return a > b },
		"lt":           func(a, b int) bool { return a < b },
		"eq":           func(a, b interface{}) bool { return a == b },
		"ne":           func(a, b interface{}) bool { return a != b },
		"formatPrice":  func(p float64) string { return fmt.Sprintf("%.3f", p) },
		"formatUsd":    func(p float64) string { return fmt.Sprintf("$%.2f", p) },
		"formatPnL":    func(p float64) string { return fmt.Sprintf("%+.2f", p) },
	}

	tmpl, err := template.New("dashboard").Funcs(funcMap).Parse(string(tmplBytes))
	if err != nil {
		log.Fatalf("dashboard: parse template: %v", err)
	}
	return tmpl
}

func renderPositionRows(positions []PositionView) template.HTML {
	if len(positions) == 0 {
		return `<tr><td colspan="5" class="empty">No open positions</td></tr>`
	}
	var b strings.Builder
	for _, p := range positions {
		cost := p.Size * p.AvgPrice
		badge := classBadgeNo
		if strings.EqualFold(p.Outcome, "yes") || strings.EqualFold(p.Outcome, "true") || strings.EqualFold(p.Outcome, "over") || strings.EqualFold(p.Outcome, "buy") {
			badge = classBadgeYes
		}
		sourceDataAttr := ""
		if p.SourceJSON != "" && p.SourceJSON != "[]" {
			sourceDataAttr = fmt.Sprintf(` data-sources='%s'`, p.SourceJSON)
		}
		marketLink := p.Question
		if p.Slug != "" {
			marketLink = fmt.Sprintf(`<a href="https://polymarket.com/market/%s" target="_blank" class="mkt-link">%s</a>`, p.Slug, truncate(p.Question, 48))
		} else if p.Question == "" {
			marketLink = `<span class="text-secondary">—</span>`
		}
		b.WriteString(fmt.Sprintf(`<tr><td class="mkt-cell">%s</td><td class="col-side side-source"><span class="badge-outcome %s"%s>%s</span></td><td class="num">%.2f</td><td class="num">%.3f</td><td class="num usd">$%.2f</td></tr>`,
			marketLink, badge, sourceDataAttr, p.Outcome, p.Size, p.AvgPrice, cost))
	}
	return template.HTML(b.String())
}

func renderBalanceRows(trades []TradeView) template.HTML {
	if len(trades) == 0 {
		return template.HTML(`<tr><td colspan="9" class="empty">No trades yet</td></tr>`)
	}
	var b strings.Builder
	for _, t := range trades {
		b.WriteString(renderBalanceRow(t))
	}
	return template.HTML(b.String())
}

// renderBalanceRow formats one trade row of the balances table.
func renderBalanceRow(t TradeView) string {
	modeBadge := `<span class="mode-live">L</span>`
	if t.Paper {
		modeBadge = `<span class="mode-paper">P</span>`
	}
	recovered, gainCell := recoveredGainCells(t)
	return fmt.Sprintf(`<tr><td class="time-full">%s</td><td>%s</td><td class="mkt-cell">%s</td><td class="col-side side-source">%s</td><td class="num">%.3f</td><td class="num usd">$%.2f</td>%s%s<td class="result-cell">%s</td><td>%s</td></tr>`,
		t.DateStr, modeBadge, marketLinkCell(t), sideCell(t), t.Price, t.SizeUSDC, recovered, gainCell, resultCell(t), strategyCell(t))
}

// recoveredGainCells renders the recovered/gain pair for a settled trade, or
// the em-dash placeholders while it is still open.
func recoveredGainCells(t TradeView) (string, string) {
	if !t.HasResult {
		return `<td class="num dim">—</td>`, `<td class="num dim">—</td>`
	}
	return fmt.Sprintf(`<td class="num usd">$%.2f</td>`, t.Recovered),
		fmt.Sprintf(`<td class="num %s">$%+ .2f</td>`, pnlClassOf(t.Gain), t.Gain)
}

// sideCell renders the outcome badge with its optional source-trader payload.
func sideCell(t TradeView) string {
	displayOutcome := t.Outcome
	if displayOutcome == "" {
		displayOutcome = t.Side
	}
	badge := classBadgeNo
	switch strings.ToLower(displayOutcome) {
	case "yes", "true", "over", "buy":
		badge = classBadgeYes
	}
	sourceDataAttr := ""
	if t.SourceJSON != "" && t.SourceJSON != "[]" {
		sourceDataAttr = fmt.Sprintf(` data-sources='%s'`, t.SourceJSON)
	}
	return fmt.Sprintf(`<span class="badge-outcome %s"%s>%s</span>`, badge, sourceDataAttr, displayOutcome)
}

func strategyCell(t TradeView) string {
	if t.Strategy == "" {
		return ""
	}
	return fmt.Sprintf(`<span class="strategy-badge">%s</span>`, t.Strategy)
}

func resultCell(t TradeView) string {
	if !t.HasResult {
		return `<span class="result-open">—</span>`
	}
	if t.Result.Float64 >= 0.99 {
		return `<span class="result-win">WIN</span>`
	}
	return `<span class="result-loss">LOSS</span>`
}

func marketLinkCell(t TradeView) string {
	if t.Slug != "" {
		return fmt.Sprintf(`<a href="https://polymarket.com/market/%s" target="_blank" class="mkt-link">%s</a>`, t.Slug, truncate(t.Question, 36))
	}
	if t.Question == "" {
		return `<span class="text-secondary">—</span>`
	}
	return t.Question
}

func renderSnapCells(snapshots []SnapshotView) template.HTML {
	if len(snapshots) == 0 {
		return template.HTML(`<div class="snap-cell text-secondary">No history</div>`)
	}
	var b strings.Builder
	for _, s := range snapshots {
		color := "snap-pos"
		if s.Realized < 0 {
			color = "snap-neg"
		}
		b.WriteString(fmt.Sprintf(`<div class="snap-cell"><div class="snap-date">%s</div><div class="snap-pnl %s">$%+ .0f</div><div class="snap-trades">%dt</div></div>`,
			s.DateShort, color, s.Realized, s.TotalTrades))
	}
	return template.HTML(b.String())
}

type traderStat struct {
	Address  string
	Name     string
	Trades   int
	Wins     int
	PnL      float64
	Invested float64
	Best     float64
	Worst    float64
}

type outcomeStat struct {
	Outcome string
	Trades  int
	Wins    int
	PnL     float64
}

type bracketStat struct {
	Bracket string
	Trades  int
	Wins    int
	PnL     float64
}

type dowStat struct {
	Day    string
	Trades int
	Wins   int
	PnL    float64
}

type hourStat struct {
	Hour   string
	Trades int
	Wins   int
	PnL    float64
}

type marketStat struct {
	Question string
	Slug     string
	Trades   int
	Wins     int
	PnL      float64
}

type strategyStat struct {
	Strategy string
	Trades   int
	Wins     int
	PnL      float64
	Invested float64
}

func gainFromTrade(t TradeView) float64 {
	if !t.HasResult {
		return 0
	}
	shares := t.SizeUSDC / t.Price
	recovered := shares * t.Result.Float64
	return recovered - t.SizeUSDC
}

// totals carries the cross-trade aggregates used by the overview cards.
type totals struct {
	invested  float64
	recovered float64
	wins      int
	losses    int
	best      float64
	worst     float64
	bestInit  bool
	worstInit bool
	sumWin    float64
	sumLoss   float64
}

// perfMetrics carries win/loss streaks and averages.
type perfMetrics struct {
	avgWin        float64
	avgLoss       float64
	profitFactor  float64
	maxWinStreak  int
	maxLossStreak int
}

// statsAgg groups every per-dimension accumulator map plus paper/live counters.
type statsAgg struct {
	traderMap   map[string]*traderStat
	bracketMap  map[string]*bracketStat
	dowMap      map[string]*dowStat
	hourMap     map[string]*hourStat
	marketMap   map[string]*marketStat
	strategyMap map[string]*strategyStat
	paperTrades int
	paperWins   int
	paperPnL    float64
	liveTrades  int
	liveWins    int
	livePnL     float64
}

func newStatsAgg() *statsAgg {
	return &statsAgg{
		traderMap:   make(map[string]*traderStat),
		bracketMap:  make(map[string]*bracketStat),
		dowMap:      make(map[string]*dowStat),
		hourMap:     make(map[string]*hourStat),
		marketMap:   make(map[string]*marketStat),
		strategyMap: make(map[string]*strategyStat),
	}
}

// winRatePct is the guarded percentage used across the stats tables.
func winRatePct(wins, trades int) float64 {
	if trades > 0 {
		return float64(wins) / float64(trades) * 100
	}
	return 0
}

// pnlClassOf picks the pos/neg CSS class for a PnL figure.
func pnlClassOf(v float64) string {
	if v < 0 {
		return classValNeg
	}
	return classValPos
}

// truncateAddress renders "0x1234…cdef" for unnamed source wallets.
func truncateAddress(addr string) string {
	name := addr
	if len(name) > 14 {
		name = name[:6] + ellipsis + name[len(name)-4:]
	}
	return name
}

// displayTraderName prefers the trader label, falling back to the address.
func displayTraderName(tc *traderStat) string {
	if tc.Name != "" {
		return tc.Name
	}
	return truncateAddress(tc.Address)
}

// byPnL desc sorts and truncates a stat slice (interface over the concrete
// stat types via a PnL accessor supplied by the caller).
func topByPnL[T any](list []T, pnl func(T) float64, limit int) []T {
	sort.Slice(list, func(i, j int) bool { return pnl(list[i]) > pnl(list[j]) })
	if len(list) > limit {
		list = list[:limit]
	}
	return list
}

func computeAndRenderStats(data *DashboardData) {
	trades := data.RecentTrades
	if len(trades) == 0 {
		data.StatsOverview = template.HTML(`<div class="empty-state">No settled trades yet</div>`)
		return
	}
	tt := &totals{}
	agg := newStatsAgg()
	ingestStats(trades, tt, agg)
	c := computePerfMetrics(trades, tt)

	data.PerfAvgWin = c.avgWin
	data.PerfAvgLoss = c.avgLoss
	data.PerfProfitFactor = c.profitFactor
	data.PerfStreakWins = c.maxWinStreak
	data.PerfStreakLosses = c.maxLossStreak

	traderList := statsByPnL(agg.traderMap, func(t *traderStat) float64 { return t.PnL }, 20)
	data.TraderStatsRows, data.TraderDetailRows = renderTraderTables(traderList)

	bracketOrder := []string{"0.00-0.20", "0.20-0.40", "0.40-0.60", "0.60-0.80", "0.80-1.00"}
	dowOrder := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	hourOrder := []string{"00-03", "04-07", "08-11", "12-15", "16-19", "20-23"}
	data.PriceStatsRows = renderOrderedRows(agg.bracketMap, bracketOrder, bracketRow)
	data.DowStatsRows = renderOrderedRows(agg.dowMap, dowOrder, dowRow)
	data.HourStatsRows = renderOrderedRows(agg.hourMap, hourOrder, hourRow)

	marketList := topByPnL(statsValues(agg.marketMap), func(m *marketStat) float64 { return m.PnL }, 30)
	data.MarketStatsRows = renderMarketTable(marketList)
	data.StrategyStatsRows = renderStrategyTable(topByPnL(statsValues(agg.strategyMap), func(s *strategyStat) float64 { return s.PnL }, 1<<30))
	data.PaperLiveStatsRow = renderPaperLiveRow(agg)

	// ── Chart.js JSON data ──
	data.PnLTimelineLabels, data.PnLTimelineData = renderSnapshotTimeline(data.SnapshotsReversed)
	data.TraderChartLabels, data.TraderChartPnL, data.TraderChartWins = renderTraderChart(traderList)
	data.PriceChartLabels, data.PriceChartPnL = renderOrderedChartSeries(agg.bracketMap, bracketOrder, bracketChartLabel)
	data.DowChartLabels, data.DowChartPnL = renderOrderedChartSeries(agg.dowMap, dowOrder, dowChartLabel)
	data.HourChartLabels, data.HourChartPnL = renderOrderedChartSeries(agg.hourMap, hourOrder, hourChartLabel)

	// Win/Loss doughnut
	data.WinLossData = template.JS(fmt.Sprintf("[%d,%d]", tt.wins, tt.losses))

	data.StatsOverview = renderOverview(tt, c)
}

// ingestStats replays the original single pass over settled trades, feeding
// every accumulator exactly once per trade and in the same order.
func ingestStats(trades []TradeView, tt *totals, agg *statsAgg) {
	for _, t := range trades {
		gain := gainFromTrade(t)
		shares := t.SizeUSDC / t.Price
		recovered := shares * t.Result.Float64
		won := gain >= 0

		tt.invested += t.SizeUSDC
		tt.recovered += recovered
		if won {
			tt.wins++
			tt.sumWin += gain
		} else {
			tt.losses++
			tt.sumLoss += gain
		}
		tt.observe(gain)
		agg.observeMode(t.Paper, won, gain)

		mergeStat(agg.bracketMap, priceBracket(t.Price), func() *bracketStat {
			return &bracketStat{Bracket: priceBracket(t.Price)}
		}, won, gain)

		ts := time.Unix(t.Ts, 0).UTC()
		mergeStat(agg.dowMap, ts.Weekday().String(), func() *dowStat {
			return &dowStat{Day: ts.Weekday().String()}
		}, won, gain)
		mergeStat(agg.hourMap, hourBracketStr(ts.Hour()), func() *hourStat {
			return &hourStat{Hour: hourBracketStr(ts.Hour())}
		}, won, gain)
		mergeStat(agg.marketMap, t.MarketID, func() *marketStat {
			return &marketStat{Question: t.Question, Slug: t.Slug}
		}, won, gain)

		stratKey := t.Strategy
		if stratKey == "" {
			stratKey = "copy_trading"
		}
		sc := agg.strategyMap[stratKey]
		if sc == nil {
			sc = &strategyStat{Strategy: stratKey}
			agg.strategyMap[stratKey] = sc
		}
		sc.Trades++
		sc.Invested += t.SizeUSDC
		if won {
			sc.Wins++
		}
		sc.PnL += gain

		ingestTradeSources(agg, t, gain)
	}
}

// bucket is the shared Trades/Wins/PnL shape every keyed stat accumulator
// embeds by implementing count/pnl accessors through mergeStat's generics.
type bucket interface {
	countInc()
	countWins()
	addPnL(float64)
}

func (b *bracketStat) countInc()        { b.Trades++ }
func (b *bracketStat) countWins()       { b.Wins++ }
func (b *bracketStat) addPnL(v float64) { b.PnL += v }
func (d *dowStat) countInc()            { d.Trades++ }
func (d *dowStat) countWins()           { d.Wins++ }
func (d *dowStat) addPnL(v float64)     { d.PnL += v }
func (h *hourStat) countInc()           { h.Trades++ }
func (h *hourStat) countWins()          { h.Wins++ }
func (h *hourStat) addPnL(v float64)    { h.PnL += v }
func (m *marketStat) countInc()         { m.Trades++ }
func (m *marketStat) countWins()        { m.Wins++ }
func (m *marketStat) addPnL(v float64)  { m.PnL += v }

// mergeStat folds one trade into the keyed accumulator, creating the bucket on
// first sight exactly like the original inline blocks.
func mergeStat[V bucket](m map[string]V, key string, fresh func() V, won bool, gain float64) {
	v, ok := m[key]
	if !ok {
		v = fresh()
		m[key] = v
	}
	v.countInc()
	if won {
		v.countWins()
	}
	v.addPnL(gain)
}

// ingestTradeSources splits one copy-traded position's gain evenly across the
// source wallets recorded in its SourceJSON payload.
// tradeSource is one entry of a copy-trade's SourceJSON payload.
type tradeSource struct {
	Address string  `json:"address"`
	Name    string  `json:"name"`
	Outcome string  `json:"outcome"`
	Size    float64 `json:"size"`
	Price   float64 `json:"price"`
}

// parseTradeSources decodes SourceJSON, tolerating malformed payloads.
func parseTradeSources(src string) []tradeSource {
	if src == "" || src == "[]" {
		return nil
	}
	var sources []tradeSource
	if err := json.Unmarshal([]byte(src), &sources); err != nil {
		return nil
	}
	return sources
}

func ingestTradeSources(agg *statsAgg, t TradeView, gain float64) {
	sources := parseTradeSources(t.SourceJSON)
	if len(sources) == 0 {
		return
	}
	splitPnL := gain / float64(len(sources))
	splitInv := t.SizeUSDC / float64(len(sources))
	won := gain >= 0
	for _, s := range sources {
		tc := agg.traderMap[s.Address]
		if tc == nil {
			tc = &traderStat{Address: s.Address, Name: s.Name}
			agg.traderMap[s.Address] = tc
		}
		if tc.Name == "" && s.Name != "" {
			tc.Name = s.Name
		}
		tc.Trades++
		tc.Invested += splitInv
		if won {
			tc.Wins++
		}
		tc.PnL += splitPnL
		foldBestWorst(tc, splitPnL)
	}
}

// foldBestWorst keeps the per-trader best/worst split figures up to date.
func foldBestWorst(tc *traderStat, splitPnL float64) {
	if tc.Best == 0 || splitPnL > tc.Best {
		tc.Best = splitPnL
	}
	if tc.Worst == 0 || splitPnL < tc.Worst {
		tc.Worst = splitPnL
	}
}

// observe tracks the running best/worst trade gains.
func (tt *totals) observe(gain float64) {
	if !tt.bestInit || gain > tt.best {
		tt.best = gain
		tt.bestInit = true
	}
	if !tt.worstInit || gain < tt.worst {
		tt.worst = gain
		tt.worstInit = true
	}
}

// observeMode folds one trade into the paper or live counter set.
func (agg *statsAgg) observeMode(paper, won bool, gain float64) {
	trades, wins, pnl := &agg.liveTrades, &agg.liveWins, &agg.livePnL
	if paper {
		trades, wins, pnl = &agg.paperTrades, &agg.paperWins, &agg.paperPnL
	}
	*trades++
	if won {
		*wins++
	}
	*pnl += gain
}

// computePerfMetrics derives averages, profit factor and win/loss streaks.
func computePerfMetrics(trades []TradeView, tt *totals) perfMetrics {
	var c perfMetrics
	if tt.wins > 0 {
		c.avgWin = tt.sumWin / float64(tt.wins)
	}
	if tt.losses > 0 {
		c.avgLoss = tt.sumLoss / float64(tt.losses)
	}
	if tt.sumLoss < 0 {
		c.profitFactor = tt.sumWin / -tt.sumLoss
	}
	curWin, curLoss := 0, 0
	for _, t := range trades {
		if gainFromTrade(t) >= 0 {
			curWin++
			curLoss = 0
			if curWin > c.maxWinStreak {
				c.maxWinStreak = curWin
			}
		} else {
			curLoss++
			curWin = 0
			if curLoss > c.maxLossStreak {
				c.maxLossStreak = curLoss
			}
		}
	}
	return c
}

// renderOverview builds the eight stat cards exactly as the old inline block.
func renderOverview(tt *totals, c perfMetrics) template.HTML {
	_ = c
	totalSettled := tt.wins + tt.losses
	winRate := 0.0
	if totalSettled > 0 {
		winRate = float64(tt.wins) / float64(totalSettled) * 100
	}
	avgGain := 0.0
	if totalSettled > 0 {
		avgGain = (tt.recovered - tt.invested) / float64(totalSettled)
	}
	return template.HTML(fmt.Sprintf(`
<div class="stats-grid">
  <div class="stat-card"><div class="stat-label">Total Invested</div><div class="stat-val usd">$%.2f</div></div>
  <div class="stat-card"><div class="stat-label">Total Recovered</div><div class="stat-val usd">$%.2f</div></div>
  <div class="stat-card"><div class="stat-label">Realized P&amp;L</div><div class="stat-val %s">$%+.2f</div></div>
  <div class="stat-card"><div class="stat-label">Win Rate</div><div class="stat-val %s">%d/%d (%.1f%%)</div></div>
  <div class="stat-card"><div class="stat-label">Best Trade</div><div class="stat-val val-pos">$%+.2f</div></div>
  <div class="stat-card"><div class="stat-label">Worst Trade</div><div class="stat-val val-neg">$%+.2f</div></div>
  <div class="stat-card"><div class="stat-label">Avg Gain/Trade</div><div class="stat-val %s">$%+.2f</div></div>
  <div class="stat-card"><div class="stat-label">Total Settled</div><div class="stat-val blue">%d</div></div>
</div>`,
		tt.invested, tt.recovered,
		pnlClass(tt.recovered-tt.invested), tt.recovered-tt.invested,
		pnlClass(float64(tt.wins-tt.losses)), tt.wins, totalSettled, winRate,
		tt.best, tt.worst,
		pnlClass(avgGain), avgGain, totalSettled))
}

// statsValues snapshots a stat map into a slice.
func statsValues[V any](m map[string]V) []V {
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// statsByPnL sorts a map's values by PnL desc and truncates to topN.
func statsByPnL[V any](m map[string]V, pnl func(V) float64, topN int) []V {
	return topByPnL(statsValues(m), pnl, topN)
}

// renderTraderTables renders the main trader rows and the detail sub-table.
func renderTraderTables(traderList []*traderStat) (template.HTML, template.HTML) {
	var traderSB strings.Builder
	if len(traderList) == 0 {
		traderSB.WriteString(`<tr><td colspan="7" class="empty">No source trader data</td></tr>`)
	} else {
		for _, tc := range traderList {
			roi := 0.0
			if tc.Invested > 0 {
				roi = tc.PnL / tc.Invested * 100
			}
			traderSB.WriteString(fmt.Sprintf(`<tr><td class="src-name">%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num usd">$%.2f</td><td class="num %s">$%+.2f</td><td class="num">%.1f%%</td></tr>`,
				displayTraderName(tc), tc.Trades, tc.Wins, winRatePct(tc.Wins, tc.Trades), tc.Invested, pnlClassOf(tc.PnL), tc.PnL, roi))
		}
	}
	var detailSB strings.Builder
	if len(traderList) == 0 {
		detailSB.WriteString(`<tr><td colspan="5" class="empty">No data</td></tr>`)
	} else {
		for _, tc := range traderList {
			detailSB.WriteString(fmt.Sprintf(`<tr><td class="src-name">%s</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td><td class="num val-pos">$%+.2f</td></tr>`,
				displayTraderName(tc), tc.Trades, winRatePct(tc.Wins, tc.Trades), pnlClassOf(tc.PnL), tc.PnL, tc.Best))
		}
	}
	return template.HTML(traderSB.String()), template.HTML(detailSB.String())
}

// orderedRow formats one stats-table row from its precomputed cells.
func orderedRow(label string, trades, wins int, pnl float64) string {
	return fmt.Sprintf(`<tr><td>%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
		label, trades, wins, winRatePct(wins, trades), pnlClassOf(pnl), pnl)
}

// statView adapts each concrete stat type to the shared ordered-row shape.
type statView struct {
	label  string
	trades int
	wins   int
	pnl    float64
}

type statPoint interface {
	view() statView
}

func (b *bracketStat) view() statView { return statView{b.Bracket, b.Trades, b.Wins, b.PnL} }
func (d *dowStat) view() statView     { return statView{d.Day[:3], d.Trades, d.Wins, d.PnL} }
func (h *hourStat) view() statView    { return statView{h.Hour, h.Trades, h.Wins, h.PnL} }

func bracketRow(b *bracketStat) string { return orderedRow(b.Bracket, b.Trades, b.Wins, b.PnL) }
func dowRow(d *dowStat) string         { return orderedRow(d.Day[:3], d.Trades, d.Wins, d.PnL) }
func hourRow(h *hourStat) string       { return orderedRow(h.Hour, h.Trades, h.Wins, h.PnL) }

func bracketChartLabel(b *bracketStat) string { return b.Bracket }
func dowChartLabel(d *dowStat) string         { return d.Day[:3] }
func hourChartLabel(h *hourStat) string       { return h.Hour }

// renderOrderedRows walks the fixed key order, skipping absent buckets, and
// renders each present bucket through rowFmt.
func renderOrderedRows[V statPoint](m map[string]V, order []string, rowFmt func(V) string) template.HTML {
	var sb strings.Builder
	for _, k := range order {
		v, ok := m[k]
		if !ok {
			continue
		}
		sb.WriteString(rowFmt(v))
	}
	return template.HTML(sb.String())
}

// renderOrderedChartSeries mirrors renderOrderedRows for Chart.js arrays.
func renderOrderedChartSeries[V statPoint](m map[string]V, order []string, label func(V) string) (template.JS, template.JS) {
	var labels, pnls []string
	for _, k := range order {
		v, ok := m[k]
		if !ok {
			continue
		}
		labels = append(labels, fmt.Sprintf("%q", label(v)))
		pnls = append(pnls, fmt.Sprintf("%.2f", v.view().pnl))
	}
	return jsArray(labels), jsArray(pnls)
}

// jsArray serialises a string slice as a JS array literal ("[]" when empty).
func jsArray(items []string) template.JS {
	return template.JS("[" + strings.Join(items, ",") + "]")
}

// renderMarketTable renders the top-30 markets table rows.
func renderMarketTable(marketList []*marketStat) template.HTML {
	var sb strings.Builder
	for _, mc := range marketList {
		label := mc.Question
		if len(label) > 50 {
			label = label[:50] + ellipsis
		}
		sb.WriteString(fmt.Sprintf(`<tr><td class="mkt-cell">%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
			label, mc.Trades, mc.Wins, winRatePct(mc.Wins, mc.Trades), pnlClassOf(mc.PnL), mc.PnL))
	}
	return template.HTML(sb.String())
}

// renderStrategyTable renders every strategy row, best PnL first.
func renderStrategyTable(strategyList []*strategyStat) template.HTML {
	var sb strings.Builder
	if len(strategyList) == 0 {
		sb.WriteString(`<tr><td colspan="5" class="empty">No strategy data</td></tr>`)
	} else {
		for _, sc := range strategyList {
			displayName := sc.Strategy
			if displayName == "" {
				displayName = "unknown"
			}
			sb.WriteString(fmt.Sprintf(`<tr><td class="src-name">%s</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num usd">$%.2f</td><td class="num %s">$%+.2f</td></tr>`,
				displayName, sc.Trades, winRatePct(sc.Wins, sc.Trades), sc.Invested, pnlClassOf(sc.PnL), sc.PnL))
		}
	}
	return template.HTML(sb.String())
}

// renderPaperLiveRow renders the two paper/live comparison rows.
func renderPaperLiveRow(agg *statsAgg) template.HTML {
	return template.HTML(fmt.Sprintf(`
<tr><td><span class="mode-paper">PAPER</span></td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>
<tr><td><span class="mode-live">LIVE</span></td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
		agg.paperTrades, agg.paperWins, winRatePct(agg.paperWins, agg.paperTrades), pnlClassOf(agg.paperPnL), agg.paperPnL,
		agg.liveTrades, agg.liveWins, winRatePct(agg.liveWins, agg.liveTrades), pnlClassOf(agg.livePnL), agg.livePnL))
}

// renderSnapshotTimeline builds the realized-PnL timeline chart arrays.
func renderSnapshotTimeline(snapshots []SnapshotView) (template.JS, template.JS) {
	if len(snapshots) == 0 {
		return template.JS("[]"), template.JS("[]")
	}
	var labels, values []string
	for _, s := range snapshots {
		labels = append(labels, s.DateShort)
		values = append(values, fmt.Sprintf("%.2f", s.Realized))
	}
	return template.JS("[" + strings.Join(mapQuoted(labels), ",") + "]"),
		template.JS("[" + strings.Join(values, ",") + "]")
}

// renderTraderChart builds the trader PnL/wins chart arrays.
func renderTraderChart(traderList []*traderStat) (template.JS, template.JS, template.JS) {
	if len(traderList) == 0 {
		return template.JS("[]"), template.JS("[]"), template.JS("[]")
	}
	var names, pnls, wins []string
	for _, tc := range traderList {
		n := tc.Name
		if n == "" {
			n = "0x" + tc.Address[:min(6, len(tc.Address))]
		}
		names = append(names, fmt.Sprintf("%q", n))
		pnls = append(pnls, fmt.Sprintf("%.2f", tc.PnL))
		wins = append(wins, fmt.Sprintf("%d", tc.Wins))
	}
	return template.JS("[" + strings.Join(names, ",") + "]"),
		template.JS("[" + strings.Join(pnls, ",") + "]"),
		template.JS("[" + strings.Join(wins, ",") + "]")
}

func mapQuoted(s []string) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = fmt.Sprintf("%q", v)
	}
	return out
}

func priceBracket(price float64) string {
	switch {
	case price < 0.20:
		return "0.00-0.20"
	case price < 0.40:
		return "0.20-0.40"
	case price < 0.60:
		return "0.40-0.60"
	case price < 0.80:
		return "0.60-0.80"
	default:
		return "0.80-1.00"
	}
}

func hourBracketStr(h int) string {
	switch {
	case h < 4:
		return "00-03"
	case h < 8:
		return "04-07"
	case h < 12:
		return "08-11"
	case h < 16:
		return "12-15"
	case h < 20:
		return "16-19"
	default:
		return "20-23"
	}
}

func readConfig(path string) template.HTML {
	data, err := os.ReadFile(path)
	if err != nil {
		return template.HTML(fmt.Sprintf(`<div class="config-err">Cannot read config: %v</div>`, err))
	}

	var appCfg cfg.AppConfig
	if err := yaml.Unmarshal(data, &appCfg); err != nil {
		appCfg = cfg.AppConfig{
			Risk:       cfg.RiskConfig{MaxPositionUSDC: 50, MaxOpenPositions: 10, MaxOpenPositionsGlobal: 100, DailyLossLimitUSDC: 200},
			Markets:    cfg.MarketsConfig{MinLiquidityUSDC: 5000},
			Strategies: cfg.StrategiesConfig{CopyTrading: cfg.CopyTradingConfig{Enabled: true, PollIntervalSeconds: 300, MinPnL: 10000, MinVolumeUSD: 10000, PriceGuard: 0.05, MaxCopyPerTrader: 3, MaxDaysToResolution: 0}},
		}
	}

	var b strings.Builder
	b.WriteString(`<div class="config-sections">`)

	b.WriteString(`<div class="config-group"><div class="config-group-title"><span class="cfg-icon">&#x26A0;</span> Risk</div>`)
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Max Position</span><span class="cfg-val">$%.0f</span></div>`, appCfg.Risk.MaxPositionUSDC))
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Max Open Positions</span><span class="cfg-val">%d</span></div>`, appCfg.Risk.MaxOpenPositions))
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Daily Loss Limit</span><span class="cfg-val">$%.0f</span></div>`, appCfg.Risk.DailyLossLimitUSDC))
	b.WriteString(`</div>`)

	b.WriteString(`<div class="config-group"><div class="config-group-title"><span class="cfg-icon">&#x1F4CA;</span> Markets</div>`)
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Min Liquidity</span><span class="cfg-val">$%.0f</span></div>`, appCfg.Markets.MinLiquidityUSDC))
	b.WriteString(`</div>`)

	b.WriteString(`<div class="config-group"><div class="config-group-title"><span class="cfg-icon">&#x1F504;</span> Copy Trading</div>`)
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Enabled</span><span class="cfg-val">%t</span></div>`, appCfg.Strategies.CopyTrading.Enabled))
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Poll Interval</span><span class="cfg-val">%ds</span></div>`, appCfg.Strategies.CopyTrading.PollIntervalSeconds))
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Min P&amp;L</span><span class="cfg-val">$%.0f</span></div>`, appCfg.Strategies.CopyTrading.MinPnL))
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Min Volume</span><span class="cfg-val">$%.0f</span></div>`, appCfg.Strategies.CopyTrading.MinVolumeUSD))
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Price Guard</span><span class="cfg-val">%.2f</span></div>`, appCfg.Strategies.CopyTrading.PriceGuard))
	b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Max Copy/Trader</span><span class="cfg-val">%d</span></div>`, appCfg.Strategies.CopyTrading.MaxCopyPerTrader))
	if appCfg.Strategies.CopyTrading.MaxDaysToResolution > 0 {
		b.WriteString(fmt.Sprintf(`<div class="config-row"><span class="cfg-label">Max Days to Res.</span><span class="cfg-val">%d</span></div>`, int(appCfg.Strategies.CopyTrading.MaxDaysToResolution)))
	}
	b.WriteString(`</div>`)

	b.WriteString(`</div>`)
	return template.HTML(b.String())
}

func buildCSV(data *DashboardData) []byte {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.Write([]string{"Date", "Mode", "Market", "Side", "Price", "Invested", "Recovered", "Gain", "Result"})
	for _, t := range data.RecentTrades {
		mode := "LIVE"
		if t.Paper {
			mode = "PAPER"
		}
		recovered := fmt.Sprintf("%.2f", t.Recovered)
		gain := fmt.Sprintf("%+.2f", t.Gain)
		if !t.HasResult {
			recovered = "0.00"
			gain = "+0.00"
		}
		cw.Write([]string{
			t.DateStr,
			mode,
			t.Question,
			t.Side,
			fmt.Sprintf("%.3f", t.Price),
			fmt.Sprintf("%.2f", t.SizeUSDC),
			recovered,
			gain,
			t.ResultStr,
		})
	}
	cw.Flush()
	return buf.Bytes()
}

func refreshCache(dbPath, strategy string, tmpl *template.Template) {
	start := time.Now()
	data := query(dbPath, strategy)

	var htmlBuf bytes.Buffer
	if err := tmpl.Execute(&htmlBuf, data); err != nil {
		log.Printf("dashboard: cache render: %v", err)
		return
	}
	csvBytes := buildCSV(data)

	cacheMu.Lock()
	stratCache[strategy] = &StrategyCache{html: htmlBuf.Bytes(), csvData: csvBytes, data: data}
	cacheMu.Unlock()

	log.Printf("dashboard: cache refreshed (%s) in %v (%d positions, %d trades)", strategy, time.Since(start).Round(time.Millisecond), data.PositionCount, data.TradeCount)
}

func backgroundRefresh(dbPath string, tmpl *template.Template) {
	for _, s := range enabledStrategies() {
		refreshCache(dbPath, s.Key, tmpl)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err == nil {
		backfillMeta(db)
		db.Close()
		for _, s := range enabledStrategies() {
			refreshCache(dbPath, s.Key, tmpl)
		}
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		for _, s := range enabledStrategies() {
			refreshCache(dbPath, s.Key, tmpl)
		}

		go func() {
			db, err := sql.Open("sqlite3", dbPath)
			if err == nil {
				backfillMeta(db)
				db.Close()
				for _, s := range enabledStrategies() {
					refreshCache(dbPath, s.Key, tmpl)
				}
			}
		}()
	}
}

func Serve(port int, dbPath string, cfgPath string) {
	configPath = cfgPath

	// Diagnóstico: al arrancar el dashboard, registrar qué estrategias
	// habilita el config que realmente se lee. Si el archivo no se
	// encuentra, LoadAppConfig devuelve defaults (solo copy_trading en
	// true) y la barra de tabs mostraría una sola pestaña — este log
	// hace visible esa discrepancia.
	defs := enabledStrategies()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.DisplayName)
	}
	slog.Info("Dashboard enabled strategies", "config", cfgPath, "count", len(defs), "strategies", strings.Join(names, ", "))

	tmpl := buildTemplate()

	go backgroundRefresh(dbPath, tmpl)

	time.Sleep(2 * time.Second)

	fs := http.FileServer(http.FS(staticFS))
	http.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		fs.ServeHTTP(w, r)
	}))
	http.HandleFunc("/", makeIndexHandler(dbPath, tmpl))
	http.HandleFunc("/csv", makeCSVHandler(dbPath))

	http.HandleFunc("/api/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		strategy := r.URL.Query().Get("strategy")
		if strategy == "" {
			strategy = "under_hunter"
		}
		sqliteDB, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer sqliteDB.Close()
		database := &bdb.DB{DB: sqliteDB}
		diags, err := database.GetLatestDiagnostics(strategy, 20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set(headerContentType, "application/json")
		json.NewEncoder(w).Encode(diags)
	})

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	log.Printf("Dashboard → http://localhost:%d", port)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("dashboard: serve: %v", err)
	}
}

// makeIndexHandler serves the dashboard page from the pre-warmed cache,
// falling back to a fresh query+render, exactly as the inline handler did.
func makeIndexHandler(dbPath string, tmpl *template.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		strategy := resolveStrategy(r.URL.Query().Get("strategy"))

		cacheMu.RLock()
		c := stratCache[strategy]
		cacheMu.RUnlock()

		var html []byte
		if c != nil {
			html = c.html
		}
		if len(html) == 0 {
			data := query(dbPath, strategy)
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				http.Error(w, "render error", http.StatusInternalServerError)
				return
			}
			html = buf.Bytes()
		}
		w.Header().Set(headerContentType, "text/html; charset=utf-8")
		w.Write(html)
	}
}

// makeCSVHandler serves the trades CSV, cache-first like the page handler.
func makeCSVHandler(dbPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		strategy := resolveStrategy(r.URL.Query().Get("strategy"))

		cacheMu.RLock()
		c := stratCache[strategy]
		cacheMu.RUnlock()

		var csv []byte
		if c != nil {
			csv = c.csvData
		}
		if len(csv) == 0 {
			data := query(dbPath, strategy)
			csv = buildCSV(data)
		}
		w.Header().Set(headerContentType, "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=vagustrader_trades.csv")
		w.Write(csv)
	}
}
