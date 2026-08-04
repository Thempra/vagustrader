package clob

import (
	"fmt"
	"time"
)

type PaperClient struct {
	dbPath string
}

func NewPaperClient(dbPath string) *PaperClient {
	return &PaperClient{dbPath: dbPath}
}

func (p *PaperClient) GetOrderbook(tokenID string) (*Orderbook, error) {
	return &Orderbook{TokenID: tokenID}, nil
}

func (p *PaperClient) PlaceLimitOrder(args OrderArgs, marketID string, exchangeAddr string) (map[string]interface{}, error) {
	orderID := fmt.Sprintf("paper-%d", time.Now().UnixMilli())
	return map[string]interface{}{
		"orderID": orderID,
		"paper":   true,
	}, nil
}

func (p *PaperClient) GetBalance() (float64, error) {
	return 10000.0, nil
}

func (p *PaperClient) GetOpenOrders() ([]map[string]interface{}, error) {
	return nil, nil
}

func (p *PaperClient) CancelOrder(orderID string) (map[string]interface{}, error) {
	return map[string]interface{}{"success": true}, nil
}
