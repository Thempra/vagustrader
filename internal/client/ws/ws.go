package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/gorilla/websocket"
)

type PriceUpdate struct {
	TokenID   string
	MarketID  string
	Price     float64
	Side      string
	Size      float64
	Timestamp int64
}

const MarketURL = "wss://ws-subscriptions-clob.polymarket.com/ws/market"

type MarketFeed struct {
	TokenIDs       []string
	ReconnectDelay time.Duration
}

func NewMarketFeed(tokenIDs []string) *MarketFeed {
	return &MarketFeed{TokenIDs: tokenIDs, ReconnectDelay: 5 * time.Second}
}

func (f *MarketFeed) Stream(ctx context.Context, out chan<- PriceUpdate) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := f.connect(ctx, out); err != nil {
			slog.Warn("WS disconnected", "error", err, "retry", f.ReconnectDelay)
			select {
			case <-ctx.Done():
				return
			case <-time.After(f.ReconnectDelay):
			}
		}
	}
}

func (f *MarketFeed) connect(ctx context.Context, out chan<- PriceUpdate) error {
	conn, _, err := websocket.DefaultDialer.Dial(MarketURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	sub := map[string]interface{}{"assets_ids": f.TokenIDs, "type": "market"}
	if err := conn.WriteJSON(sub); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var items []map[string]interface{}
		if err := json.Unmarshal(raw, &items); err != nil {
			var single map[string]interface{}
			if err2 := json.Unmarshal(raw, &single); err2 == nil {
				items = []map[string]interface{}{single}
			} else {
				continue
			}
		}
		for _, item := range items {
			pu := PriceUpdate{}
			if v, ok := item["asset_id"].(string); ok {
				pu.TokenID = v
			}
			if v, ok := item["market"].(string); ok {
				pu.MarketID = v
			}
			if v, ok := item["price"].(float64); ok {
				pu.Price = v
			}
			if v, ok := item["side"].(string); ok {
				pu.Side = v
			}
			if v, ok := item["size"].(float64); ok {
				pu.Size = v
			}
			if v, ok := item["timestamp"].(float64); ok {
				pu.Timestamp = int64(v)
			}
			select {
			case out <- pu:
			case <-ctx.Done():
				return nil
			}
		}
	}
}
