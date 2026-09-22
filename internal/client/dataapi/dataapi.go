package dataapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type LeaderboardEntry struct {
	Address string
	Name    string
	PnL     float64
	Volume  float64
}

type Position struct {
	TokenID      string
	MarketID     string
	Outcome      string
	Size         float64
	AvgPrice     float64
	CurrentValue float64
	Slug         string
	Icon         string
	CashPnl      float64
	PercentPnl   float64
}

type Client struct {
	baseURL string
	client  *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) GetLeaderboard(limit, offset int) ([]LeaderboardEntry, error) {
	u, err := url.Parse(c.baseURL + "/v1/leaderboard")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	q.Set("timePeriod", "ALL")
	q.Set("orderBy", "PNL")
	u.RawQuery = q.Encode()

	resp, err := c.client.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("leaderboard request failed: %d", resp.StatusCode)
	}

	var raw []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	entries := make([]LeaderboardEntry, 0, len(raw))
	for _, d := range raw {
		entries = append(entries, parseEntry(d))
	}
	return entries, nil
}

func (c *Client) GetTopTraders(minPnL float64, limit int) ([]LeaderboardEntry, error) {
	entries, err := c.GetLeaderboard(limit, 0)
	if err != nil {
		return nil, err
	}
	filtered := make([]LeaderboardEntry, 0, len(entries))
	for _, e := range entries {
		if e.PnL >= minPnL {
			filtered = append(filtered, e)
		}
	}
	return filtered, nil
}

// PortfolioValue returns the sum of the current value (USDC) of all open
// positions held by a wallet, read from the public Polymarket data API.
// Polymarket holds trading collateral in a separate proxy wallet (the
// deposit/CIP wallet, which owns the positions), so this is the number the
// dashboard should show as the real wallet balance. It is auth-free and
// read-only. Pages through /positions with an offset up to a sane cap.
func (c *Client) PortfolioValue(userAddress string) (float64, error) {
	const pageSize = 500
	const maxPages = 40 // 20k positions cap for safety
	var total float64
	for page := 0; page < maxPages; page++ {
		u, err := url.Parse(c.baseURL + "/positions")
		if err != nil {
			return 0, err
		}
		q := u.Query()
		q.Set("user", userAddress)
		q.Set("sizeThreshold", "0")
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(page*pageSize))
		u.RawQuery = q.Encode()

		resp, err := c.client.Get(u.String())
		if err != nil {
			return 0, err
		}
		var raw []map[string]interface{}
		decodeErr := json.NewDecoder(resp.Body).Decode(&raw)
		resp.Body.Close()
		if decodeErr != nil {
			return 0, decodeErr
		}
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("portfolio request failed: %d", resp.StatusCode)
		}
		if len(raw) == 0 {
			break
		}
		for _, d := range raw {
			total += floatVal(d, "currentValue")
		}
		if len(raw) < pageSize {
			break
		}
		// Be gentle with the public API to avoid 403 rate limits.
		time.Sleep(150 * time.Millisecond)
	}
	return total, nil
}

func (c *Client) GetPositions(userAddress string) ([]Position, error) {
	u, err := url.Parse(c.baseURL + "/positions")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("user", userAddress)
	u.RawQuery = q.Encode()

	resp, err := c.client.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("positions request failed: %d", resp.StatusCode)
	}

	var raw []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	positions := make([]Position, 0, len(raw))
	for _, d := range raw {
		positions = append(positions, parsePosition(d))
	}
	return positions, nil
}

// FillCheck is a confirmed on-portal fill for a token, read from the public
// trades endpoint. Used to verify live order fills when the authenticated
// CLOB /order/{id} endpoint is unavailable (it 404s for L1/L2 keys derived
// without 1271 binding).
type FillCheck struct {
	Size  float64 // shares filled
	Price float64
}

// FindRecentFill returns fills for tokenID placed after `sinceUnix` by the
// given wallet. The public /trades endpoint reflects matched fills within a
// couple of seconds and requires no auth, so it is the reliable source of
// "did my order actually execute".
func (c *Client) FindRecentFill(userAddress, tokenID string, sinceUnix int64) (*FillCheck, error) {
	u, err := url.Parse(c.baseURL + "/trades")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("user", userAddress)
	q.Set("limit", "100")
	q.Set("takerOnly", "false")
	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	// Polymarket's CDN 403s default HTTP client user agents
	// (Go-http-client/Python-urllib); identify ourselves.
	req.Header.Set("User-Agent", "vagustrader/1.0")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trades request failed: %d", resp.StatusCode)
	}
	var raw []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var agg *FillCheck
	var total float64
	for _, d := range raw {
		if stringVal(d, "asset") != tokenID {
			continue
		}
		if int64(floatVal(d, "timestamp")) < sinceUnix {
			continue
		}
		size := floatVal(d, "size")
		price := floatVal(d, "price")
		if agg == nil {
			agg = &FillCheck{}
		}
		agg.Size += size
		agg.Price = price
		total += size * price
	}
	if agg == nil || total <= 0 {
		return nil, nil
	}
	if agg.Size > 0 {
		agg.Price = total / agg.Size
	}
	return agg, nil
}

func (c *Client) GetActivity(userAddress string, limit int) ([]map[string]interface{}, error) {
	u, err := url.Parse(c.baseURL + "/activity")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("user", userAddress)
	q.Set("limit", strconv.Itoa(limit))
	u.RawQuery = q.Encode()

	resp, err := c.client.Get(u.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("activity request failed: %d", resp.StatusCode)
	}

	var result []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func parseEntry(d map[string]interface{}) LeaderboardEntry {
	return LeaderboardEntry{
		Address: stringVal(d, "proxyWallet"),
		Name:    stringVal(d, "userName"),
		PnL:     floatVal(d, "pnl"),
		Volume:  floatVal(d, "vol"),
	}
}

func parsePosition(d map[string]interface{}) Position {
	tokenID := stringVal(d, "asset")
	if tokenID == "" {
		tokenID = stringVal(d, "tokenId")
	}
	marketID := stringVal(d, "conditionId")
	if marketID == "" {
		marketID = stringVal(d, "market")
	}
	return Position{
		TokenID:      tokenID,
		MarketID:     marketID,
		Outcome:      stringVal(d, "outcome"),
		Size:         floatVal(d, "size"),
		AvgPrice:     floatVal(d, "avgPrice"),
		CurrentValue: floatVal(d, "currentValue"),
		Slug:         stringVal(d, "slug"),
		Icon:         stringVal(d, "icon"),
		CashPnl:      floatVal(d, "cashPnl"),
		PercentPnl:   floatVal(d, "percentPnl"),
	}
}

func stringVal(d map[string]interface{}, key string) string {
	v, ok := d[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func floatVal(d map[string]interface{}, key string) float64 {
	v, ok := d[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}
