package dataapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// v2Server fakes the Polymarket data API v2 contract: envelope responses,
// cursor-only pagination (an "offset" param is a 400), and the renamed
// position/trade fields. Anything that regresses to v1 behaviour fails here.
func v2Server(t *testing.T, rows [][]map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("offset") != "" {
			http.Error(w, `{"error":"'offset' is not a query param on this API"}`, http.StatusBadRequest)
			return
		}
		if r.Header.Get("User-Agent") == "" || r.Header.Get("User-Agent") == "Go-http-client/1.1" {
			http.Error(w, "blocked", http.StatusForbidden)
			return
		}
		page := 0
		if c := q.Get("cursor"); c != "" {
			fmt.Sscanf(c, "page-%d", &page)
		}
		if page >= len(rows) {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data":       []interface{}{},
				"pagination": map[string]interface{}{"has_more": false},
			})
			return
		}
		next := ""
		if page+1 < len(rows) {
			next = fmt.Sprintf("page-%d", page+1)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": rows[page],
			"pagination": map[string]interface{}{
				"limit":       500,
				"offset":      page * 500,
				"has_more":    next != "",
				"next_cursor": next,
			},
		})
	}))
}

func TestPortfolioValueFollowsCursorAndCountsOpenOnly(t *testing.T) {
	open := map[string]interface{}{"current_size": 10.0, "current_value": 7.5, "token_id": "a"}
	closed := map[string]interface{}{"current_size": 0.0, "current_value": 0.0, "token_id": "b"}
	open2 := map[string]interface{}{"current_size": 5.0, "current_value": 2.5, "token_id": "c"}
	srv := v2Server(t, [][]map[string]interface{}{{open, closed}, {open2}})
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.PortfolioValue("0xabc")
	if err != nil {
		t.Fatalf("PortfolioValue: %v", err)
	}
	if got != 10.0 {
		t.Fatalf("PortfolioValue = %v, want 10.0 (7.5 + 2.5, closed row ignored)", got)
	}
}

func TestGetPositionsUsesV2FieldNames(t *testing.T) {
	row := map[string]interface{}{
		"token_id":      "tok1",
		"condition_id":  "0xcond",
		"outcome":       "Yes",
		"current_size":  12.5,
		"avg_price":     0.42,
		"current_value": 5.25,
		"slug":          "some-market",
		"total_pnl":     1.5,
		"percent_pnl":   40.0,
	}
	srv := v2Server(t, [][]map[string]interface{}{{row}})
	defer srv.Close()

	c := NewClient(srv.URL)
	pos, err := c.GetPositions("0xabc")
	if err != nil {
		t.Fatalf("GetPositions: %v", err)
	}
	if len(pos) != 1 {
		t.Fatalf("got %d positions, want 1", len(pos))
	}
	p := pos[0]
	if p.TokenID != "tok1" || p.MarketID != "0xcond" || p.Size != 12.5 || p.AvgPrice != 0.42 || p.CurrentValue != 5.25 {
		t.Fatalf("unexpected position mapping: %+v", p)
	}
	if p.CashPnl != 1.5 || p.PercentPnl != 40.0 {
		t.Fatalf("pnl mapping wrong: %+v", p)
	}
}

func TestFindRecentFillMatchesTokenIDAndWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" {
			http.Error(w, "offset not allowed", http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"token_id": "target", "size": 2.0, "price": 0.5, "timestamp": 2000},
				{"token_id": "target", "size": 1.0, "price": 0.6, "timestamp": 2100},
				{"token_id": "other", "size": 9.0, "price": 0.9, "timestamp": 2200},
				{"token_id": "target", "size": 9.0, "price": 0.9, "timestamp": 100}, // before window
			},
			"pagination": map[string]interface{}{"has_more": false},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	fill, err := c.FindRecentFill("0xabc", "target", 1500)
	if err != nil {
		t.Fatalf("FindRecentFill: %v", err)
	}
	if fill == nil {
		t.Fatal("expected a fill, got nil")
	}
	if fill.Size != 3.0 {
		t.Fatalf("size = %v, want 3.0", fill.Size)
	}
	// weighted price: (2*0.5 + 1*0.6) / 3
	if diff := fill.Price - 0.5333333; diff > 0.001 || diff < -0.001 {
		t.Fatalf("price = %v, want ~0.5333", fill.Price)
	}
}

func TestGetLeaderboardMapsV2FieldsAndAppliesOffset(t *testing.T) {
	srv := v2Server(t, [][]map[string]interface{}{{
		{"user_id": "0x1", "user_name": "one", "pnl": 10.0, "volume": 100.0},
		{"user_id": "0x2", "user_name": "two", "pnl": 9.0, "volume": 90.0},
		{"user_id": "0x3", "user_name": "three", "pnl": 8.0, "volume": 80.0},
	}})
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.GetLeaderboard(1, 1)
	if err != nil {
		t.Fatalf("GetLeaderboard: %v", err)
	}
	if len(got) != 1 || got[0].Address != "0x2" || got[0].Volume != 90.0 {
		t.Fatalf("unexpected leaderboard slice: %+v", got)
	}
}

func TestClientSurfacesHTTPErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"Data API v1 has been retired"}`, http.StatusGone)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if _, err := c.PortfolioValue("0xabc"); err == nil {
		t.Fatal("expected an error on a 410 response, got nil")
	}
}
