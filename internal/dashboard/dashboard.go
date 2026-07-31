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
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/vagustrader/vagustrader/internal/cfg"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed static/*
var staticFS embed.FS

type DashboardData struct {
	Positions         []PositionView
	RecentTrades      []TradeView
	TodayTrades       []TradeView
	Snapshots         []SnapshotView
	SnapshotsReversed []SnapshotView
	CurrentValues     map[string]float64

	PositionCount int
	TradeCount    int
	TotalCost     float64
	RealizedPnL   float64
	UnrealizedPnL float64
	TotalPnL      float64
	LiveToday     int
	PaperToday    int
	InvestedToday float64
	Today         string
	Now           string
	CSS           string
	JS            string

	PositionRows template.HTML
	BalanceRows  template.HTML
	SnapCells    template.HTML

	StatsOverview      template.HTML
	TraderStatsRows    template.HTML
	TraderDetailRows   template.HTML
	PriceStatsRows     template.HTML
	DowStatsRows       template.HTML
	HourStatsRows      template.HTML
	MarketStatsRows    template.HTML
	PaperLiveStatsRow  template.HTML

	PnLTimelineLabels  template.JS
	PnLTimelineData    template.JS
	TraderChartLabels   template.JS
	TraderChartPnL      template.JS
	TraderChartWins     template.JS
	PriceChartLabels    template.JS
	PriceChartPnL       template.JS
	DowChartLabels      template.JS
	DowChartPnL         template.JS
	HourChartLabels     template.JS
	HourChartPnL        template.JS
	WinLossData         template.JS
	PerfAvgWin          float64
	PerfAvgLoss         float64
	PerfProfitFactor    float64
	PerfStreakWins      int
	PerfStreakLosses    int

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
}

type SnapshotView struct {
	Date        string
	DateShort   string
	Realized    float64
	Unrealized  float64
	TotalTrades int
}

var gammaBase = "https://gamma-api.polymarket.com"
var clobBase = "https://clob.polymarket.com"
var priceClient = &http.Client{Timeout: 4 * time.Second}
var metaClient = &http.Client{Timeout: 6 * time.Second}

type cachedDashboard struct {
	html    []byte
	csvData []byte
	data    *DashboardData
	ts      time.Time
}

var (
	cache     atomic.Pointer[cachedDashboard]
	cacheMu   sync.RWMutex
	cacheHTML []byte
	cacheCSV  []byte
	cacheData *DashboardData
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

func query(dbPath string) *DashboardData {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Printf("dashboard: open db: %v", err)
		return &DashboardData{}
	}
	defer db.Close()

	data := &DashboardData{
		CurrentValues: make(map[string]float64),
		Today:         time.Now().UTC().Format("2006-01-02"),
		Now:           time.Now().UTC().Format("15:04:05") + " UTC",
	}

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
		ORDER BY p.updated_ts DESC
	`)
	if err == nil {
		defer posRows.Close()
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
	data.PositionCount = len(data.Positions)

	prices := fetchTokenPricesParallel(data.Positions)
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

	today := time.Now().UTC()
	startOfDay := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	endOfDay := startOfDay.Add(24 * time.Hour)
	startTs := startOfDay.Unix()
	endTs := endOfDay.Unix()

	todayRows, err := db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, '') FROM trades WHERE ts >= ? AND ts < ?", startTs, endTs)
	if err == nil {
		defer todayRows.Close()
		for todayRows.Next() {
			var tv TradeView
			var paper int
			if err := todayRows.Scan(&tv.ID, &tv.Ts, &tv.MarketID, &tv.TokenID, &tv.Side, &tv.Outcome, &tv.Price, &tv.SizeUSDC, new(string), &paper, &tv.Result, &tv.SourceJSON); err != nil {
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

	tradeRows, err := db.Query(`
		SELECT t.id, t.ts, t.market_id, t.token_id, t.side, COALESCE(t.outcome, ''),
		       t.price, t.size_usdc,
		       COALESCE(t.order_id, ''), t.paper, t.result,
		       COALESCE(t.source_json, ''),
		       COALESCE(m.question, ''), COALESCE(m.slug, '')
		FROM trades t
		LEFT JOIN market_meta m ON t.market_id = m.condition_id
		WHERE t.result IS NOT NULL
		ORDER BY t.ts DESC LIMIT 500
	`)
	if err == nil {
		defer tradeRows.Close()
		for tradeRows.Next() {
			var tv TradeView
			var paper int
			if err := tradeRows.Scan(&tv.ID, &tv.Ts, &tv.MarketID, &tv.TokenID, &tv.Side, &tv.Outcome, &tv.Price, &tv.SizeUSDC, new(string), &paper, &tv.Result, &tv.SourceJSON, &tv.Question, &tv.Slug); err != nil {
				continue
			}
			tv.Paper = paper == 1
			tv.DateStr = time.Unix(tv.Ts, 0).UTC().Format("2006-01-02 15:04")
			if tv.Result.Valid {
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
			} else {
				tv.ResultStr = "OPEN"
			}
			data.RecentTrades = append(data.RecentTrades, tv)
		}
	}
	data.TradeCount = len(data.RecentTrades)

	snapRows, err := db.Query("SELECT date, realized, unrealized, total_trades FROM pnl_snapshots ORDER BY date DESC")
	if err == nil {
		defer snapRows.Close()
		for snapRows.Next() {
			var sv SnapshotView
			if err := snapRows.Scan(&sv.Date, &sv.Realized, &sv.Unrealized, &sv.TotalTrades); err != nil {
				continue
			}
			if len(sv.Date) >= 5 {
				sv.DateShort = sv.Date[5:]
			}
			data.Snapshots = append(data.Snapshots, sv)
			data.RealizedPnL += sv.Realized
		}
	}

	snapDisplay := data.Snapshots
	if len(snapDisplay) > 7 {
		snapDisplay = snapDisplay[:7]
	}
	for i := len(snapDisplay) - 1; i >= 0; i-- {
		data.SnapshotsReversed = append(data.SnapshotsReversed, snapDisplay[i])
	}

	data.TotalPnL = data.RealizedPnL + data.UnrealizedPnL

	data.PositionRows = renderPositionRows(data.Positions)
	data.BalanceRows = renderBalanceRows(data.RecentTrades)
	data.SnapCells = renderSnapCells(data.SnapshotsReversed)

	computeAndRenderStats(data)

	data.DBPath = dbPath
	data.LastUpdated = time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
	data.ConfigHTML = readConfig(configPath)

	return data
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\u2026"
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
	cls := "badge-yes"
	outcomeLower := strings.ToLower(outcome)
	if outcomeLower == "no" || outcomeLower == "false" {
		cls = "badge-no"
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
		badge := "badge-no"
		if strings.EqualFold(p.Outcome, "yes") || strings.EqualFold(p.Outcome, "true") || strings.EqualFold(p.Outcome, "over") || strings.EqualFold(p.Outcome, "buy") {
			badge = "badge-yes"
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
		modeBadge := `<span class="mode-live">L</span>`
		if t.Paper {
			modeBadge = `<span class="mode-paper">P</span>`
		}
		var recovered, gainCell string
		if t.HasResult {
			recovered = fmt.Sprintf(`<td class="num usd">$%.2f</td>`, t.Recovered)
			gainClass := "val-pos"
			if t.Gain < 0 {
				gainClass = "val-neg"
			}
			gainCell = fmt.Sprintf(`<td class="num %s">$%+ .2f</td>`, gainClass, t.Gain)
		} else {
			recovered = `<td class="num dim">—</td>`
			gainCell = `<td class="num dim">—</td>`
		}
		displayOutcome := t.Outcome
		if displayOutcome == "" {
			displayOutcome = t.Side
		}
		badge := "badge-no"
		outcomeLower := strings.ToLower(displayOutcome)
		if outcomeLower == "yes" || outcomeLower == "true" || outcomeLower == "over" || outcomeLower == "buy" {
			badge = "badge-yes"
		}
		sourceDataAttr := ""
		if t.SourceJSON != "" && t.SourceJSON != "[]" {
			sourceDataAttr = fmt.Sprintf(` data-sources='%s'`, t.SourceJSON)
		}
		sideCell := fmt.Sprintf(`<span class="badge-outcome %s"%s>%s</span>`, badge, sourceDataAttr, displayOutcome)
		resultBadge := `<span class="result-open">—</span>`
		if t.HasResult {
			if t.Result.Float64 >= 0.99 {
				resultBadge = `<span class="result-win">WIN</span>`
			} else {
				resultBadge = `<span class="result-loss">LOSS</span>`
			}
		}
		marketLink := t.Question
		if t.Slug != "" {
			marketLink = fmt.Sprintf(`<a href="https://polymarket.com/market/%s" target="_blank" class="mkt-link">%s</a>`, t.Slug, truncate(t.Question, 36))
		} else if t.Question == "" {
			marketLink = `<span class="text-secondary">—</span>`
		}
		b.WriteString(fmt.Sprintf(`<tr><td class="time-full">%s</td><td>%s</td><td class="mkt-cell">%s</td><td class="col-side side-source">%s</td><td class="num">%.3f</td><td class="num usd">$%.2f</td>%s%s<td class="result-cell">%s</td></tr>`,
			t.DateStr, modeBadge, marketLink, sideCell, t.Price, t.SizeUSDC, recovered, gainCell, resultBadge))
	}
	return template.HTML(b.String())
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

func gainFromTrade(t TradeView) float64 {
	if !t.HasResult {
		return 0
	}
	shares := t.SizeUSDC / t.Price
	recovered := shares * t.Result.Float64
	return recovered - t.SizeUSDC
}

func computeAndRenderStats(data *DashboardData) {
	trades := data.RecentTrades
	if len(trades) == 0 {
		data.StatsOverview = template.HTML(`<div class="empty-state">No settled trades yet</div>`)
		return
	}

	var totalInvested, totalRecovered float64
	var winCount, lossCount int
	var bestTrade, worstTrade float64
	bestInit, worstInit := false, false
	var sumWinGain, sumLossGain float64

	traderMap := make(map[string]*traderStat)
	bracketMap := make(map[string]*bracketStat)
	dowMap := make(map[string]*dowStat)
	hourMap := make(map[string]*hourStat)
	marketMap := make(map[string]*marketStat)
	var paperTrades, liveTrades, paperWins, liveWins int
	var paperPnL, livePnL float64

	for _, t := range trades {
		gain := gainFromTrade(t)
		shares := t.SizeUSDC / t.Price
		recovered := shares * t.Result.Float64

		totalInvested += t.SizeUSDC
		totalRecovered += recovered

		if gain >= 0 {
			winCount++
			sumWinGain += gain
		} else {
			lossCount++
			sumLossGain += gain
		}

		if !bestInit || gain > bestTrade {
			bestTrade = gain
			bestInit = true
		}
		if !worstInit || gain < worstTrade {
			worstTrade = gain
			worstInit = true
		}

		if t.Paper {
			paperTrades++
			if gain >= 0 {
				paperWins++
			}
			paperPnL += gain
		} else {
			liveTrades++
			if gain >= 0 {
				liveWins++
			}
			livePnL += gain
		}

		bracket := priceBracket(t.Price)
		bc := bracketMap[bracket]
		if bc == nil {
			bc = &bracketStat{Bracket: bracket}
			bracketMap[bracket] = bc
		}
		bc.Trades++
		if gain >= 0 {
			bc.Wins++
		}
		bc.PnL += gain

		ts := time.Unix(t.Ts, 0).UTC()
		dowName := ts.Weekday().String()
		dc := dowMap[dowName]
		if dc == nil {
			dc = &dowStat{Day: dowName}
			dowMap[dowName] = dc
		}
		dc.Trades++
		if gain >= 0 {
			dc.Wins++
		}
		dc.PnL += gain

		hourBracket := hourBracketStr(ts.Hour())
		hc := hourMap[hourBracket]
		if hc == nil {
			hc = &hourStat{Hour: hourBracket}
			hourMap[hourBracket] = hc
		}
		hc.Trades++
		if gain >= 0 {
			hc.Wins++
		}
		hc.PnL += gain

		marketKey := t.MarketID
		mc := marketMap[marketKey]
		if mc == nil {
			mc = &marketStat{Question: t.Question, Slug: t.Slug}
			marketMap[marketKey] = mc
		}
		mc.Trades++
		if gain >= 0 {
			mc.Wins++
		}
		mc.PnL += gain

		if t.SourceJSON != "" && t.SourceJSON != "[]" {
			var sources []struct {
				Address string  `json:"address"`
				Name    string  `json:"name"`
				Outcome string  `json:"outcome"`
				Size    float64 `json:"size"`
				Price   float64 `json:"price"`
			}
			if err := json.Unmarshal([]byte(t.SourceJSON), &sources); err == nil && len(sources) > 0 {
				splitPnL := gain / float64(len(sources))
				splitInv := t.SizeUSDC / float64(len(sources))
				for _, s := range sources {
					tc := traderMap[s.Address]
					if tc == nil {
						tc = &traderStat{Address: s.Address, Name: s.Name}
						traderMap[s.Address] = tc
					}
					if tc.Name == "" && s.Name != "" {
						tc.Name = s.Name
					}
					tc.Trades++
					tc.Invested += splitInv
					if gain >= 0 {
						tc.Wins++
					}
					tc.PnL += splitPnL
					if tc.Best == 0 || splitPnL > tc.Best {
						tc.Best = splitPnL
					}
					if tc.Worst == 0 || splitPnL < tc.Worst {
						tc.Worst = splitPnL
					}
				}
			}
		}
	}

	totalSettled := winCount + lossCount
	winRate := 0.0
	if totalSettled > 0 {
		winRate = float64(winCount) / float64(totalSettled) * 100
	}
	avgGain := 0.0
	if totalSettled > 0 {
		avgGain = (totalRecovered - totalInvested) / float64(totalSettled)
	}

	data.PerfAvgWin = 0
	if winCount > 0 {
		data.PerfAvgWin = sumWinGain / float64(winCount)
	}
	data.PerfAvgLoss = 0
	if lossCount > 0 {
		data.PerfAvgLoss = sumLossGain / float64(lossCount)
	}
	data.PerfProfitFactor = 0
	if sumLossGain < 0 {
		data.PerfProfitFactor = sumWinGain / -sumLossGain
	}

	data.PerfStreakWins = 0
	data.PerfStreakLosses = 0
	curWin, curLoss := 0, 0
	for _, t := range trades {
		gain := gainFromTrade(t)
		if gain >= 0 {
			curWin++
			curLoss = 0
			if curWin > data.PerfStreakWins {
				data.PerfStreakWins = curWin
			}
		} else {
			curLoss++
			curWin = 0
			if curLoss > data.PerfStreakLosses {
				data.PerfStreakLosses = curLoss
			}
		}
	}

	overviewHTML := fmt.Sprintf(`
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
		totalInvested, totalRecovered,
		pnlClass(totalRecovered-totalInvested), totalRecovered-totalInvested,
		pnlClass(float64(winCount-lossCount)), winCount, totalSettled, winRate,
		bestTrade, worstTrade,
		pnlClass(avgGain), avgGain, totalSettled)
	data.StatsOverview = template.HTML(overviewHTML)

	// ── Trader table ──
	var traderList []*traderStat
	for _, tc := range traderMap {
		traderList = append(traderList, tc)
	}
	sort.Slice(traderList, func(i, j int) bool {
		return traderList[i].PnL > traderList[j].PnL
	})
	if len(traderList) > 20 {
		traderList = traderList[:20]
	}
	var traderSB strings.Builder
	if len(traderList) == 0 {
		traderSB.WriteString(`<tr><td colspan="7" class="empty">No source trader data</td></tr>`)
	} else {
		for _, tc := range traderList {
			wr := 0.0
			if tc.Trades > 0 {
				wr = float64(tc.Wins) / float64(tc.Trades) * 100
			}
			pnlCls := "val-pos"
			if tc.PnL < 0 {
				pnlCls = "val-neg"
			}
			name := tc.Name
			if name == "" {
				name = tc.Address
				if len(name) > 14 {
					name = name[:6] + "\u2026" + name[len(name)-4:]
				}
			}
			roi := 0.0
			if tc.Invested > 0 {
				roi = tc.PnL / tc.Invested * 100
			}
			traderSB.WriteString(fmt.Sprintf(`<tr><td class="src-name">%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num usd">$%.2f</td><td class="num %s">$%+.2f</td><td class="num">%.1f%%</td></tr>`,
				name, tc.Trades, tc.Wins, wr, tc.Invested, pnlCls, tc.PnL, roi))
		}
	}
	data.TraderStatsRows = template.HTML(traderSB.String())

	// ── Trader detail rows (for the trader detail sub-table) ──
	var detailSB strings.Builder
	if len(traderList) == 0 {
		detailSB.WriteString(`<tr><td colspan="5" class="empty">No data</td></tr>`)
	} else {
		for _, tc := range traderList {
			name := tc.Name
			if name == "" {
				name = tc.Address
				if len(name) > 14 {
					name = name[:6] + "\u2026" + name[len(name)-4:]
				}
			}
			wr := 0.0
			if tc.Trades > 0 {
				wr = float64(tc.Wins) / float64(tc.Trades) * 100
			}
			pnlCls := "val-pos"
			if tc.PnL < 0 {
				pnlCls = "val-neg"
			}
			detailSB.WriteString(fmt.Sprintf(`<tr><td class="src-name">%s</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td><td class="num val-pos">$%+.2f</td></tr>`,
				name, tc.Trades, wr, pnlCls, tc.PnL, tc.Best))
		}
	}
	data.TraderDetailRows = template.HTML(detailSB.String())

	// ── Price bracket table ──
	bracketOrder := []string{"0.00-0.20", "0.20-0.40", "0.40-0.60", "0.60-0.80", "0.80-1.00"}
	var priceSB strings.Builder
	for _, br := range bracketOrder {
		bc := bracketMap[br]
		if bc == nil {
			continue
		}
		wr := 0.0
		if bc.Trades > 0 {
			wr = float64(bc.Wins) / float64(bc.Trades) * 100
		}
		pnlCls := "val-pos"
		if bc.PnL < 0 {
			pnlCls = "val-neg"
		}
		priceSB.WriteString(fmt.Sprintf(`<tr><td>%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
			br, bc.Trades, bc.Wins, wr, pnlCls, bc.PnL))
	}
	data.PriceStatsRows = template.HTML(priceSB.String())

	// ── Day of week table ──
	dowOrder := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	var dowSB strings.Builder
	for _, d := range dowOrder {
		dc := dowMap[d]
		if dc == nil {
			continue
		}
		wr := 0.0
		if dc.Trades > 0 {
			wr = float64(dc.Wins) / float64(dc.Trades) * 100
		}
		pnlCls := "val-pos"
		if dc.PnL < 0 {
			pnlCls = "val-neg"
		}
		dowSB.WriteString(fmt.Sprintf(`<tr><td>%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
			d[:3], dc.Trades, dc.Wins, wr, pnlCls, dc.PnL))
	}
	data.DowStatsRows = template.HTML(dowSB.String())

	// ── Hour table ──
	hourOrder := []string{"00-03", "04-07", "08-11", "12-15", "16-19", "20-23"}
	var hourSB strings.Builder
	for _, h := range hourOrder {
		hc := hourMap[h]
		if hc == nil {
			continue
		}
		wr := 0.0
		if hc.Trades > 0 {
			wr = float64(hc.Wins) / float64(hc.Trades) * 100
		}
		pnlCls := "val-pos"
		if hc.PnL < 0 {
			pnlCls = "val-neg"
		}
		hourSB.WriteString(fmt.Sprintf(`<tr><td>%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
			h, hc.Trades, hc.Wins, wr, pnlCls, hc.PnL))
	}
	data.HourStatsRows = template.HTML(hourSB.String())

	// ── Market table ──
	var marketList []*marketStat
	for _, mc := range marketMap {
		marketList = append(marketList, mc)
	}
	sort.Slice(marketList, func(i, j int) bool {
		return marketList[i].PnL > marketList[j].PnL
	})
	if len(marketList) > 30 {
		marketList = marketList[:30]
	}
	var marketSB strings.Builder
	for _, mc := range marketList {
		wr := 0.0
		if mc.Trades > 0 {
			wr = float64(mc.Wins) / float64(mc.Trades) * 100
		}
		pnlCls := "val-pos"
		if mc.PnL < 0 {
			pnlCls = "val-neg"
		}
		label := mc.Question
		if len(label) > 50 {
			label = label[:50] + "\u2026"
		}
		marketSB.WriteString(fmt.Sprintf(`<tr><td class="mkt-cell">%s</td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
			label, mc.Trades, mc.Wins, wr, pnlCls, mc.PnL))
	}
	data.MarketStatsRows = template.HTML(marketSB.String())

	// ── Paper vs Live ──
	paperWr := 0.0
	if paperTrades > 0 {
		paperWr = float64(paperWins) / float64(paperTrades) * 100
	}
	liveWr := 0.0
	if liveTrades > 0 {
		liveWr = float64(liveWins) / float64(liveTrades) * 100
	}
	paperPnLCls := "val-pos"
	if paperPnL < 0 {
		paperPnLCls = "val-neg"
	}
	livePnLCls := "val-pos"
	if livePnL < 0 {
		livePnLCls = "val-neg"
	}
	data.PaperLiveStatsRow = template.HTML(fmt.Sprintf(`
<tr><td><span class="mode-paper">PAPER</span></td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>
<tr><td><span class="mode-live">LIVE</span></td><td class="num">%d</td><td class="num">%d</td><td class="num">%.1f%%</td><td class="num %s">$%+.2f</td></tr>`,
		paperTrades, paperWins, paperWr, paperPnLCls, paperPnL,
		liveTrades, liveWins, liveWr, livePnLCls, livePnL))

	// ── Chart.js JSON data ──

	// PnL timeline from snapshots
	if len(data.SnapshotsReversed) > 0 {
		var labels, values []string
		for _, s := range data.SnapshotsReversed {
			labels = append(labels, s.DateShort)
			values = append(values, fmt.Sprintf("%.2f", s.Realized))
		}
		data.PnLTimelineLabels = template.JS("[" + strings.Join(mapQuoted(labels), ",") + "]")
		data.PnLTimelineData = template.JS("[" + strings.Join(values, ",") + "]")
	} else {
		data.PnLTimelineLabels = template.JS("[]")
		data.PnLTimelineData = template.JS("[]")
	}

	// Trader chart
	if len(traderList) > 0 {
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
		data.TraderChartLabels = template.JS("[" + strings.Join(names, ",") + "]")
		data.TraderChartPnL = template.JS("[" + strings.Join(pnls, ",") + "]")
		data.TraderChartWins = template.JS("[" + strings.Join(wins, ",") + "]")
	} else {
		data.TraderChartLabels = template.JS("[]")
		data.TraderChartPnL = template.JS("[]")
		data.TraderChartWins = template.JS("[]")
	}

	// Price bracket chart
	var priceLabels, pricePnLs []string
	for _, br := range bracketOrder {
		bc := bracketMap[br]
		if bc == nil {
			continue
		}
		priceLabels = append(priceLabels, fmt.Sprintf("%q", br))
		pricePnLs = append(pricePnLs, fmt.Sprintf("%.2f", bc.PnL))
	}
	data.PriceChartLabels = template.JS("[" + strings.Join(priceLabels, ",") + "]")
	data.PriceChartPnL = template.JS("[" + strings.Join(pricePnLs, ",") + "]")

	// DOW chart
	var dowLabels, dowPnLs []string
	for _, d := range dowOrder {
		dc := dowMap[d]
		if dc == nil {
			continue
		}
		dowLabels = append(dowLabels, fmt.Sprintf("%q", d[:3]))
		dowPnLs = append(dowPnLs, fmt.Sprintf("%.2f", dc.PnL))
	}
	data.DowChartLabels = template.JS("[" + strings.Join(dowLabels, ",") + "]")
	data.DowChartPnL = template.JS("[" + strings.Join(dowPnLs, ",") + "]")

	// Hour chart
	var hourLabels, hourPnLs []string
	for _, h := range hourOrder {
		hc := hourMap[h]
		if hc == nil {
			continue
		}
		hourLabels = append(hourLabels, fmt.Sprintf("%q", h))
		hourPnLs = append(hourPnLs, fmt.Sprintf("%.2f", hc.PnL))
	}
	data.HourChartLabels = template.JS("[" + strings.Join(hourLabels, ",") + "]")
	data.HourChartPnL = template.JS("[" + strings.Join(hourPnLs, ",") + "]")

	// Win/Loss doughnut
	data.WinLossData = template.JS(fmt.Sprintf("[%d,%d]", winCount, lossCount))
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
			Risk:       cfg.RiskConfig{MaxPositionUSDC: 50, MaxOpenPositions: 10, DailyLossLimitUSDC: 200},
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

func refreshCache(dbPath string, tmpl *template.Template) {
	start := time.Now()
	data := query(dbPath)

	var htmlBuf bytes.Buffer
	if err := tmpl.Execute(&htmlBuf, data); err != nil {
		log.Printf("dashboard: cache render: %v", err)
		return
	}
	csvBytes := buildCSV(data)

	cacheMu.Lock()
	cacheHTML = htmlBuf.Bytes()
	cacheCSV = csvBytes
	cacheData = data
	cacheMu.Unlock()

	log.Printf("dashboard: cache refreshed in %v (%d positions, %d trades)", time.Since(start).Round(time.Millisecond), data.PositionCount, data.TradeCount)
}

func backgroundRefresh(dbPath string, tmpl *template.Template) {
	refreshCache(dbPath, tmpl)

	db, err := sql.Open("sqlite3", dbPath)
	if err == nil {
		backfillMeta(db)
		db.Close()
		refreshCache(dbPath, tmpl)
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		refreshCache(dbPath, tmpl)

		go func() {
			db, err := sql.Open("sqlite3", dbPath)
			if err == nil {
				backfillMeta(db)
				db.Close()
				refreshCache(dbPath, tmpl)
			}
		}()
	}
}

func Serve(port int, dbPath string, cfgPath string) {
	configPath = cfgPath
	tmpl := buildTemplate()

	go backgroundRefresh(dbPath, tmpl)

	time.Sleep(2 * time.Second)

	fs := http.FileServer(http.FS(staticFS))
	http.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		fs.ServeHTTP(w, r)
	}))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		cacheMu.RLock()
		html := cacheHTML
		cacheMu.RUnlock()
		if len(html) == 0 {
			data := query(dbPath)
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, data); err != nil {
				http.Error(w, "render error", http.StatusInternalServerError)
				return
			}
			html = buf.Bytes()
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(html)
	})

	http.HandleFunc("/csv", func(w http.ResponseWriter, r *http.Request) {
		cacheMu.RLock()
		csv := cacheCSV
		cacheMu.RUnlock()
		if len(csv) == 0 {
			data := query(dbPath)
			csv = buildCSV(data)
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=vagustrader_trades.csv")
		w.Write(csv)
	})

	addr := fmt.Sprintf("0.0.0.0:%d", port)
	log.Printf("Dashboard → http://localhost:%d", port)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatalf("dashboard: serve: %v", err)
	}
}
