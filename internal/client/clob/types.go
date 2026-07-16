package clob

type OrderbookLevel struct {
	Price float64
	Size  float64
}

type Orderbook struct {
	TokenID string
	Bids    []OrderbookLevel
	Asks    []OrderbookLevel
}

type OrderArgs struct {
	TokenID  string
	Side     string
	Price    float64
	SizeUSDC float64
}

type ClobCreds struct {
	APIKey        string
	APISecret     string
	APIPassphrase string
}

type Client interface {
	GetOrderbook(tokenID string) (*Orderbook, error)
	PlaceLimitOrder(args OrderArgs, marketID string) (map[string]interface{}, error)
	GetBalance() (float64, error)
	GetOpenOrders() ([]map[string]interface{}, error)
	CancelOrder(orderID string) (map[string]interface{}, error)
}
