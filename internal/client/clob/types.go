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
	TickSize string
}

type ClobCreds struct {
	APIKey        string
	APISecret     string
	APIPassphrase string
	Address       string
}

type Client interface {
	GetOrderbook(tokenID string) (*Orderbook, error)
	PlaceLimitOrder(args OrderArgs, marketID string, exchangeAddr string) (map[string]interface{}, error)
	GetBalance() (float64, error)
	GetOpenOrders() ([]map[string]interface{}, error)
	CancelOrder(orderID string) (map[string]interface{}, error)
	RedeemPosition(rpcURL, conditionID string, negRisk bool, shares float64) (string, error)
}
