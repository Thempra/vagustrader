package gamma

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type Market struct {
	ID             string
	Question       string
	Outcomes       []string
	OutcomePrices  []float64
	TokenIDs       []string
	Volume         float64
	Liquidity      float64
	Active         bool
	EndDate        string
	ConditionID    string
	Slug           string
}

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) GetMarkets(limit, offset int, active, fetchAll bool) ([]Market, error) {
	if !fetchAll {
		return c.fetchPage(limit, offset, active)
	}

	var all []Market
	pageSize := 500
	cur := offset
	for {
		page, err := c.fetchPage(pageSize, cur, active)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
		cur += pageSize
	}
	return all, nil
}

func (c *Client) GetTradeableMarkets(minVolume, priceGuard float64, limit int) ([]Market, error) {
	all, err := c.GetMarkets(limit, 0, true, true)
	if err != nil {
		return nil, err
	}

	var filtered []Market
	for i := range all {
		m := &all[i]
		if m.Volume < minVolume {
			continue
		}
		if len(m.OutcomePrices) == 0 {
			continue
		}
		ok := true
		for _, p := range m.OutcomePrices {
			if p <= priceGuard || p >= 1-priceGuard {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		filtered = append(filtered, *m)
	}
	return filtered, nil
}

func (c *Client) FetchMetaByToken(tokenID string) (conditionID, question, slug string, err error) {
	u := fmt.Sprintf("%s/markets?clob_token_ids=%s", c.baseURL, tokenID)
	resp, err := c.http.Get(u)
	if err != nil {
		return "", "", "", fmt.Errorf("gamma fetch meta: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", fmt.Errorf("gamma fetch meta read: %w", err)
	}

	var raw rawResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", "", "", fmt.Errorf("gamma fetch meta unmarshal: %w", err)
	}

	items := raw.Data
	if items == nil {
		if err := json.Unmarshal(body, &items); err != nil {
			return "", "", "", fmt.Errorf("gamma fetch meta parse array: %w", err)
		}
	}

	if len(items) == 0 {
		return "", "", "", nil
	}

	m, err := parseMarket(items[0])
	if err != nil {
		return "", "", "", err
	}
	return m.ConditionID, m.Question, m.Slug, nil
}

func (c *Client) fetchPage(limit, offset int, active bool) ([]Market, error) {
	u := fmt.Sprintf("%s/markets?limit=%d&offset=%d&active=%s",
		c.baseURL, limit, offset, strconv.FormatBool(active))
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("gamma get markets: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("gamma get markets read: %w", err)
	}

	return parseMarketsResponse(body)
}

type rawResponse struct {
	Data []json.RawMessage `json:"data"`
}

func parseMarketsResponse(body []byte) ([]Market, error) {
	var raw rawResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		var items []json.RawMessage
		if err2 := json.Unmarshal(body, &items); err2 != nil {
			return nil, fmt.Errorf("gamma parse response: %w", err)
		}
		raw.Data = items
	}

	var markets []Market
	for _, item := range raw.Data {
		m, err := parseMarket(item)
		if err != nil {
			continue
		}
		markets = append(markets, *m)
	}
	return markets, nil
}

func parseMarket(raw json.RawMessage) (*Market, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}

	getStr := func(key string) string {
		if v, ok := obj[key]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
		return ""
	}

	getFloat := func(key string) float64 {
		if v, ok := obj[key]; ok {
			var f float64
			if json.Unmarshal(v, &f) == nil {
				return f
			}
			var s string
			if json.Unmarshal(v, &s) == nil {
				f, _ = strconv.ParseFloat(s, 64)
				return f
			}
		}
		return 0
	}

	getBool := func(key string) bool {
		if v, ok := obj[key]; ok {
			var b bool
			if json.Unmarshal(v, &b) == nil {
				return b
			}
		}
		return false
	}

	conditionID := getStr("conditionId")
	if conditionID == "" {
		conditionID = getStr("id")
	}

	outcomes := parseStringArray(obj["outcomes"])
	pricesRaw := parseStringArray(obj["outcomePrices"])
	var outcomePrices []float64
	for _, p := range pricesRaw {
		f, _ := strconv.ParseFloat(p, 64)
		outcomePrices = append(outcomePrices, f)
	}

	tokenIDs := parseTokenIDs(obj["clobTokenIds"])

	return &Market{
		ID:            getStr("id"),
		Question:      getStr("question"),
		Outcomes:      outcomes,
		OutcomePrices: outcomePrices,
		TokenIDs:      tokenIDs,
		Volume:        getFloat("volume"),
		Liquidity:     getFloat("liquidity"),
		Active:        getBool("active"),
		EndDate:       getStr("endDate"),
		ConditionID:   conditionID,
		Slug:          getStr("slug"),
	}, nil
}

func parseStringArray(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}

	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		var inner []string
		if json.Unmarshal([]byte(s), &inner) == nil {
			return inner
		}
	}

	return nil
}

func parseTokenIDs(raw json.RawMessage) []string {
	if raw == nil {
		return nil
	}

	var items []json.RawMessage
	if json.Unmarshal(raw, &items) == nil {
		return extractTokenIDs(items)
	}

	var s string
	if json.Unmarshal(raw, &s) == nil {
		var inner []json.RawMessage
		if json.Unmarshal([]byte(s), &inner) == nil {
			return extractTokenIDs(inner)
		}
	}

	return nil
}

func extractTokenIDs(items []json.RawMessage) []string {
	var ids []string
	for _, item := range items {
		var obj map[string]json.RawMessage
		if json.Unmarshal(item, &obj) == nil {
			if v, ok := obj["token_id"]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil {
					ids = append(ids, s)
				}
			}
			continue
		}
		var s string
		if json.Unmarshal(item, &s) == nil {
			ids = append(ids, s)
		}
	}
	return ids
}
