package clob

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

type ClobClient struct {
	host           string
	signer         *Signer
	creds          *ClobCreds
	client         *http.Client
	DepositWallet  string
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

func (c *ClobClient) PlaceLimitOrder(args OrderArgs, marketID string, exchangeAddr string) (map[string]interface{}, error) {
	tickSize := args.TickSize
	if tickSize == "" {
		tickSize = "0.01"
	}
	if exchangeAddr == "" {
		exchangeAddr = ExchangeV2
	}

	var signed *SignedOrderV2
	var err error
	if c.DepositWallet != "" {
		signed, err = BuildSignedOrderPoly1271(c.signer, args, exchangeAddr, tickSize, c.DepositWallet)
	} else {
		signed, err = BuildSignedOrder(c.signer, args, exchangeAddr, tickSize)
	}
	if err != nil {
		return nil, fmt.Errorf("build signed order: %w", err)
	}
	payload := map[string]interface{}{
		"order":     signed,
		"owner":     c.creds.APIKey,
		"orderType": "GTC",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal order: %w", err)
	}
	slog.Info("Order payload", "body", string(body))
	var result map[string]interface{}
	if err := c.post("/order", body, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *ClobClient) GetBalance() (float64, error) {
	ts := time.Now().Unix()
	sig := BuildHMACSignature(c.creds.APISecret, ts, "GET", "/balance-allowance", "")
	headers := c.authHeaders(ts, sig)
	url := c.host + "/balance-allowance?asset_type=COLLATERAL"
	var rawBytes []byte
	if err := c.doRequestRaw(http.MethodGet, url, nil, headers, &rawBytes); err != nil {
		return 0, err
	}
	slog.Info("GetBalance raw body", "body", string(rawBytes))
	var raw struct {
		Balance   string `json:"balance"`
		Allowance string `json:"allowance"`
	}
	if err := json.Unmarshal(rawBytes, &raw); err != nil {
		return 0, fmt.Errorf("decode balance: %w", err)
	}
	addr := c.signer.Address()
	if c.creds.Address != "" {
		addr = c.creds.Address
	}
	slog.Info("GetBalance parsed",
		"balance", raw.Balance,
		"allowance", raw.Allowance,
		"pol_address", addr,
		"signer_address", c.signer.Address(),
		"api_key_prefix", c.creds.APIKey[:min(8, len(c.creds.APIKey))],
	)
	bal, err := strconv.ParseFloat(raw.Balance, 64)
	if err != nil {
		return 0, fmt.Errorf("parse balance: %w", err)
	}
	return bal / 1e6, nil
}

func (c *ClobClient) GetOpenOrders() ([]map[string]interface{}, error) {
	var result []map[string]interface{}
	if err := c.authedGet("/data/orders", "", &result); err != nil {
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
	sig, err := c.signer.SignClobAuth("", ts, "0")
	if err != nil {
		return nil, fmt.Errorf("sign clob auth: %w", err)
	}
	headers := map[string]string{
		"POLY_ADDRESS":   c.signer.Address(),
		"POLY_SIGNATURE": sig,
		"POLY_TIMESTAMP": strconv.FormatInt(ts, 10),
		"POLY_NONCE":     "0",
	}
	var resp struct {
		APIKey        string `json:"apiKey"`
		APISecret     string `json:"secret"`
		APIPassphrase string `json:"passphrase"`
	}
	if err := c.doRequest(http.MethodPost, c.host+"/auth/api-key", nil, headers, &resp); err != nil {
		return nil, err
	}
	return &ClobCreds{
		APIKey:        resp.APIKey,
		APISecret:     resp.APISecret,
		APIPassphrase: resp.APIPassphrase,
		Address:       c.signer.Address(),
	}, nil
}

func (c *ClobClient) CreateOrDeriveAPIKey() (*ClobCreds, error) {
	return c.DeriveAPIKey()
}

func DeriveAPIKeyFromSigner(signer *Signer, host string) (*ClobCreds, error) {
	return DeriveAPIKeyForWallet(signer, host, signer.Address())
}

func DeriveAPIKeyForWallet(signer *Signer, host, walletAddr string) (*ClobCreds, error) {
	ts := time.Now().Unix()
	nonce := time.Now().UnixMilli()
	nonceStr := strconv.FormatInt(nonce, 10)

	usePoly1271 := walletAddr != "" && !strings.EqualFold(walletAddr, signer.Address())

	var sig string
	var err error
	if usePoly1271 {
		depositWallet := common.HexToAddress(walletAddr)
		sig, err = signer.SignClobAuthPoly1271(ts, nonceStr, depositWallet)
		if err != nil {
			return nil, fmt.Errorf("sign clob auth poly1271: %w", err)
		}
	} else {
		sig, err = signer.SignClobAuth(walletAddr, ts, nonceStr)
		if err != nil {
			return nil, fmt.Errorf("sign clob auth: %w", err)
		}
	}

	headers := map[string]string{
		"POLY_ADDRESS":   walletAddr,
		"POLY_SIGNATURE": sig,
		"POLY_TIMESTAMP": strconv.FormatInt(ts, 10),
		"POLY_NONCE":     nonceStr,
	}
	var resp struct {
		APIKey        string `json:"apiKey"`
		APISecret     string `json:"secret"`
		APIPassphrase string `json:"passphrase"`
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
	slog.Info("Derive response", "body", string(data))
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &ClobCreds{
		APIKey:        resp.APIKey,
		APISecret:     resp.APISecret,
		APIPassphrase: resp.APIPassphrase,
		Address:       walletAddr,
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

func (c *ClobClient) authedGet(hsignPath, queryString string, result interface{}) error {
	ts := time.Now().Unix()
	sig := BuildHMACSignature(c.creds.APISecret, ts, "GET", hsignPath, "")
	headers := c.authHeaders(ts, sig)
	url := c.host + hsignPath
	if queryString != "" {
		url += "?" + queryString
	}
	return c.doRequest(http.MethodGet, url, nil, headers, result)
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
	addr := c.signer.Address()
	if c.creds.Address != "" {
		addr = c.creds.Address
	}
	return map[string]string{
		"POLY_ADDRESS":    addr,
		"POLY_SIGNATURE":  sig,
		"POLY_TIMESTAMP":  strconv.FormatInt(ts, 10),
		"POLY_API_KEY":    c.creds.APIKey,
		"POLY_PASSPHRASE": c.creds.APIPassphrase,
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

func (c *ClobClient) doRequestRaw(method, url string, body []byte, headers map[string]string, out *[]byte) error {
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
	*out = data
	return nil
}
