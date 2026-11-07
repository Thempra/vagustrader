package dataapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// apiPrefix is the version path of the public Polymarket data API. v1 was
// retired by Polymarket (every path under the root answers
// 410 "Data API v1 has been retired"), and v2 differs from it in three ways
// this client has to honour:
//
//  1. every response is an envelope {"data": [...], "pagination": {...}}
//     instead of a bare JSON array;
//  2. pagination is cursor-only — passing "offset" is a hard 400, the next
//     page is requested with pagination.next_cursor;
//  3. the field names changed (asset -> token_id, size -> current_size,
//     currentValue -> current_value, ...).
const apiPrefix = "/v2"

// pageCap bounds how many pages a paginated walk follows, so a pagination
// bug on the remote side can never spin forever.
const pageCap = 40

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

// pagination is the v2 cursor block returned alongside every list.
type pagination struct {
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// envelope is the v2 response shape: rows live under "data".
type envelope struct {
	Data       []map[string]interface{} `json:"data"`
	Pagination pagination               `json:"pagination"`
}

// getJSON fetches path (relative to the API root) with the given query and
// decodes the v2 envelope. It identifies itself with a User-Agent because
// Polymarket's CDN 403s default Go/Python client agents.
func (c *Client) getJSON(path string, q url.Values) (*envelope, error) {
	u, err := url.Parse(c.baseURL + apiPrefix + path)
	if err != nil {
		return nil, err
	}
	if q != nil {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "vagustrader/1.0")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s request failed: %d", path, resp.StatusCode)
	}
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	return &env, nil
}

// getPaged walks every page of path/q with the cursor, calling fn for each
// row. It stops at pageCap pages or when the server reports no more data.
func (c *Client) getPaged(path string, q url.Values, fn func(map[string]interface{})) error {
	if q == nil {
		q = url.Values{}
	}
	cursor := ""
	for page := 0; page < pageCap; page++ {
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		env, err := c.getJSON(path, q)
		if err != nil {
			return err
		}
		if len(env.Data) == 0 {
			return nil
		}
		for _, d := range env.Data {
			fn(d)
		}
		if !env.Pagination.HasMore || env.Pagination.NextCursor == "" {
			return nil
		}
		cursor = env.Pagination.NextCursor
		// Be gentle with the public API to avoid 403 rate limits.
		time.Sleep(150 * time.Millisecond)
	}
	return nil
}

func (c *Client) GetLeaderboard(limit, offset int) ([]LeaderboardEntry, error) {
	// v2 has no offset: walk from the start and drop the first `offset` rows.
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit+offset))
	q.Set("timePeriod", "ALL")
	q.Set("orderBy", "PNL")

	var out []LeaderboardEntry
	err := c.getPaged("/leaderboard", q, func(d map[string]interface{}) {
		out = append(out, parseEntry(d))
	})
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if offset >= len(out) {
			return nil, nil
		}
		out = out[offset:]
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
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
// read-only.
func (c *Client) PortfolioValue(userAddress string) (float64, error) {
	q := url.Values{}
	q.Set("user", userAddress)
	q.Set("sizeThreshold", "0")
	q.Set("limit", "500")

	var total float64
	err := c.getPaged("/positions", q, func(d map[string]interface{}) {
		// current_size is the still-open size; the response also carries
		// fully closed rows, which must not count towards the value.
		if floatVal(d, "current_size") <= 0 {
			return
		}
		total += floatVal(d, "current_value")
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

func (c *Client) GetPositions(userAddress string) ([]Position, error) {
	q := url.Values{}
	q.Set("user", userAddress)
	q.Set("sizeThreshold", "0")
	q.Set("limit", "500")

	positions := make([]Position, 0, 32)
	err := c.getPaged("/positions", q, func(d map[string]interface{}) {
		if floatVal(d, "current_size") <= 0 {
			return
		}
		positions = append(positions, parsePosition(d))
	})
	if err != nil {
		return nil, err
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
	q := url.Values{}
	q.Set("user", userAddress)
	q.Set("limit", "100")
	q.Set("takerOnly", "false")

	var agg *FillCheck
	var total float64
	err := c.getPaged("/trades", q, func(d map[string]interface{}) {
		if stringVal(d, "token_id") != tokenID {
			return
		}
		if int64(floatVal(d, "timestamp")) < sinceUnix {
			return
		}
		size := floatVal(d, "size")
		price := floatVal(d, "price")
		if agg == nil {
			agg = &FillCheck{}
		}
		agg.Size += size
		agg.Price = price
		total += size * price
	})
	if err != nil {
		return nil, err
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
	q := url.Values{}
	q.Set("user", userAddress)
	q.Set("limit", strconv.Itoa(limit))

	out := make([]map[string]interface{}, 0, limit)
	err := c.getPaged("/activity", q, func(d map[string]interface{}) {
		out = append(out, d)
	})
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func parseEntry(d map[string]interface{}) LeaderboardEntry {
	return LeaderboardEntry{
		Address: stringVal(d, "user_id"),
		Name:    stringVal(d, "user_name"),
		PnL:     floatVal(d, "pnl"),
		Volume:  floatVal(d, "volume"),
	}
}

func parsePosition(d map[string]interface{}) Position {
	return Position{
		TokenID:      stringVal(d, "token_id"),
		MarketID:     stringVal(d, "condition_id"),
		Outcome:      stringVal(d, "outcome"),
		Size:         floatVal(d, "current_size"),
		AvgPrice:     floatVal(d, "avg_price"),
		CurrentValue: floatVal(d, "current_value"),
		Slug:         stringVal(d, "slug"),
		Icon:         stringVal(d, "icon"),
		CashPnl:      floatVal(d, "total_pnl"),
		PercentPnl:   floatVal(d, "percent_pnl"),
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
