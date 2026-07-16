package clob

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"time"
)

type ClobClient struct {
	host   string
	signer *Signer
	creds  *ClobCreds
	client *http.Client
}

func NewClobClient(privateKey string, creds *ClobCreds, host string, chainID int) (*ClobClient, error) {
	signer, err := NewSigner(privateKey, chainID)
	if err != nil {
		return nil, fmt.Errorf("create signer: %w", err)
	}
	return &ClobClient{
		host:   host,
		signer: signer,
		creds:  creds,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (c *ClobClient) GetOrderbook(tokenID string) (*Orderbook, error) {
	var raw struct {
		Bids []struct {
			Price string `json:"price"`
			Size  string `json:"size"`
		} `json:"bids"`
		Asks []struct {
			Price string `json:"price"`
			Size  string `json:"size"`
		} `json:"asks"`
	}
	if err := c.get("/book?token_id="+tokenID, &raw); err != nil {
		return nil, err
	}
	ob := &Orderbook{TokenID: tokenID}
	for _, b := range raw.Bids {
		price, _ := strconv.ParseFloat(b.Price, 64)
		size, _ := strconv.ParseFloat(b.Size, 64)
		ob.Bids = append(ob.Bids, OrderbookLevel{Price: price, Size: size})
	}
	for _, a := range raw.Asks {
		price, _ := strconv.ParseFloat(a.Price, 64)
		size, _ := strconv.ParseFloat(a.Size, 64)
		ob.Asks = append(ob.Asks, OrderbookLevel{Price: price, Size: size})
	}
	return ob, nil
}

func (c *ClobClient) PlaceLimitOrder(args OrderArgs, marketID string) (map[string]interface{}, error) {
	signed, err := BuildSignedOrder(c.signer, args, ExchangeV2, "0.01")
	if err != nil {
		return nil, fmt.Errorf("build signed order: %w", err)
	}
	payload := map[string]interface{}{
		"order":     signed,
		"owner":     c.signer.Address(),
		"orderType": "GTC",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal order: %w", err)
	}
	var result map[string]interface{}
	if err := c.post("/order", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *ClobClient) GetBalance() (float64, error) {
	var raw struct {
		Balance string `json:"balance"`
	}
	if err := c.authedGet("/balance-allowance?asset_type=COLLATERAL", &raw); err != nil {
		return 0, err
	}
	bal, err := strconv.ParseFloat(raw.Balance, 64)
	if err != nil {
		return 0, fmt.Errorf("parse balance: %w", err)
	}
	return bal / 1e6, nil
}

func (c *ClobClient) GetOpenOrders() ([]map[string]interface{}, error) {
	var result []map[string]interface{}
	if err := c.authedGet("/data/orders", &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *ClobClient) CancelOrder(orderID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := c.authedDelete("/order/"+orderID, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *ClobClient) DeriveAPIKey() (*ClobCreds, error) {
	ts := time.Now().Unix()
	nonceBig, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 256))
	if err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	sig, err := c.signer.SignClobAuth(ts, int(nonceBig.Int64()))
	if err != nil {
		return nil, fmt.Errorf("sign clob auth: %w", err)
	}
	headers := map[string]string{
		"POLY_ADDRESS":   c.signer.Address(),
		"POLY_SIGNATURE": sig,
		"POLY_TIMESTAMP": strconv.FormatInt(ts, 10),
		"POLY_NONCE":     nonceBig.String(),
	}
	var resp struct {
		APIKey        string `json:"apiKey"`
		APISecret     string `json:"apiSecret"`
		APIPassphrase string `json:"apiPassphrase"`
	}
	if err := c.doRequest(http.MethodPost, c.host+"/auth/api-key", nil, headers, &resp); err != nil {
		return nil, err
	}
	return &ClobCreds{
		APIKey:        resp.APIKey,
		APISecret:     resp.APISecret,
		APIPassphrase: resp.APIPassphrase,
	}, nil
}

func (c *ClobClient) CreateOrDeriveAPIKey() (*ClobCreds, error) {
	return c.DeriveAPIKey()
}

func DeriveAPIKeyFromSigner(signer *Signer, host string) (*ClobCreds, error) {
	ts := time.Now().Unix()
	nonceBig, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 256))
	if err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	sig, err := signer.SignClobAuth(ts, int(nonceBig.Int64()))
	if err != nil {
		return nil, fmt.Errorf("sign clob auth: %w", err)
	}
	headers := map[string]string{
		"POLY_ADDRESS":   signer.Address(),
		"POLY_SIGNATURE": sig,
		"POLY_TIMESTAMP": strconv.FormatInt(ts, 10),
		"POLY_NONCE":     nonceBig.String(),
	}
	var resp struct {
		APIKey        string `json:"apiKey"`
		APISecret     string `json:"apiSecret"`
		APIPassphrase string `json:"apiPassphrase"`
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodPost, host+"/auth/api-key", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp2, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp2.Body.Close()
	data, err := io.ReadAll(resp2.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp2.StatusCode, string(data))
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &ClobCreds{
		APIKey:        resp.APIKey,
		APISecret:     resp.APISecret,
		APIPassphrase: resp.APIPassphrase,
	}, nil
}

func (c *ClobClient) FetchMarketData(conditionID string) (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := c.get("/markets/"+conditionID, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *ClobClient) get(path string, result interface{}) error {
	return c.doRequest(http.MethodGet, c.host+path, nil, nil, result)
}

func (c *ClobClient) authedGet(path string, result interface{}) error {
	ts := time.Now().Unix()
	sig := BuildHMACSignature(c.creds.APISecret, ts, "GET", path, "")
	headers := c.authHeaders(ts, sig)
	return c.doRequest(http.MethodGet, c.host+path, nil, headers, result)
}

func (c *ClobClient) post(path string, body []byte, result interface{}) error {
	ts := time.Now().Unix()
	bodyStr := ""
	if body != nil {
		bodyStr = string(body)
	}
	sig := BuildHMACSignature(c.creds.APISecret, ts, "POST", path, bodyStr)
	headers := c.authHeaders(ts, sig)
	return c.doRequest(http.MethodPost, c.host+path, body, headers, result)
}

func (c *ClobClient) authedDelete(path string, result interface{}) error {
	ts := time.Now().Unix()
	sig := BuildHMACSignature(c.creds.APISecret, ts, "DELETE", path, "")
	headers := c.authHeaders(ts, sig)
	return c.doRequest(http.MethodDelete, c.host+path, nil, headers, result)
}

func (c *ClobClient) authHeaders(ts int64, sig string) map[string]string {
	return map[string]string{
		"POLY_ADDRESS":     c.signer.Address(),
		"POLY_SIGNATURE":   sig,
		"POLY_TIMESTAMP":   strconv.FormatInt(ts, 10),
		"POLY_API_KEY":     c.creds.APIKey,
		"POLY_PASSPHRASE":  c.creds.APIPassphrase,
	}
}

func (c *ClobClient) doRequest(method, url string, body []byte, headers map[string]string, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "vagustrader/1.0")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(data))
	}
	if result != nil {
		if err := json.Unmarshal(data, result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}
