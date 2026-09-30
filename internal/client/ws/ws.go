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
	for ctx.Err() == nil {
		if err := f.connect(ctx, out); err != nil {
			slog.Warn("WS disconnected", "error", err, "retry", f.ReconnectDelay)
			if !waitOrDoneCtx(ctx, f.ReconnectDelay) {
				return
			}
		}
	}
}

// waitOrDoneCtx sleeps for d and reports true, or returns false early when ctx
// is cancelled first.
func waitOrDoneCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
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
		stop, err := emitPriceUpdates(raw, out, ctx.Done())
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
}

// decodeFeedItems tolerates both shapes the Polymarket WS feed sends: a JSON
// array of events, or a single JSON object.
func decodeFeedItems(raw []byte) []map[string]interface{} {
	var items []map[string]interface{}
	if err := json.Unmarshal(raw, &items); err != nil {
		var single map[string]interface{}
		if err2 := json.Unmarshal(raw, &single); err2 == nil {
			items = []map[string]interface{}{single}
		}
	}
	return items
}

// emitPriceUpdates forwards every price update decoded from one raw WS frame.
// stop is true when the done channel fired mid-batch.
func emitPriceUpdates(raw []byte, out chan<- PriceUpdate, done <-chan struct{}) (stop bool, err error) {
	for _, item := range decodeFeedItems(raw) {
		select {
		case out <- parsePriceUpdate(item):
		case <-done:
			return true, nil
		}
	}
	return false, nil
}

// parsePriceUpdate maps one raw WS market message onto a PriceUpdate.
// Unknown or wrongly-typed fields keep their zero value.
func parsePriceUpdate(item map[string]interface{}) PriceUpdate {
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
	return pu
}
