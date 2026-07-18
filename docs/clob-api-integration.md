# CLOB API Integration

## Overview

The Polymarket CLOB (Central Limit Order Book) API is the primary execution layer. VagusTrader interacts with it for:

- Market data retrieval
- Limit order placement and cancellation
- Balance and open order queries
- API key derivation

---

## API Endpoints

| Endpoint | Method | Auth | Purpose |
|---|---|---|---|
| `/book?token_id=` | GET | No | Orderbook for a token |
| `/order` | POST | Yes | Place signed limit order |
| `/order/{id}` | DELETE | Yes | Cancel order |
| `/balance-allowance?asset_type=COLLATERAL` | GET | Yes | USDC balance |
| `/data/orders` | GET | Yes | Open orders |
| `/markets/{condition_id}` | GET | No | Market metadata + resolved status |
| `/auth/api-key` | POST | EIP-712 | Derive API credentials |

---

## Client Interface

All trading operations go through the `Client` interface:

```go
type Client interface {
    GetOrderbook(tokenID string) (*Orderbook, error)
    PlaceLimitOrder(args OrderArgs, marketID string) (map[string]interface{}, error)
    GetBalance() (float64, error)
    GetOpenOrders() ([]map[string]interface{}, error)
    CancelOrder(orderID string) (map[string]interface{}, error)
}
```

Two implementations:

| Implementation | Use Case | Balance | Orders |
|---|---|---|---|
| `ClobClient` | Live trading | Real from API | Real orders on CLOB |
| `PaperClient` | Simulation | Always $10,000 | Fake (no network) |

---

## Authentication Flow

### Step 1: Derive API Credentials

```go
func (c *ClobClient) DeriveAPIKey() (*ClobCreds, error)
```

1. Generate EIP-712 typed data signing the current timestamp + nonce
2. POST to `/auth/api-key` with the signature
3. Receive `{ apiKey, secret, passphrase }`

Credentials are cached for the process lifetime.

### Step 2: Authenticate Requests

Every authenticated request includes HMAC headers:

| Header | Source |
|---|---|
| `POLY_ADDRESS` | `signer.Address()` |
| `POLY_SIGNATURE` | `signer.SignClobAuth(timestamp, nonce)` |
| `POLY_TIMESTAMP` | Current Unix timestamp in milliseconds |
| `POLY_API_KEY` | Derived API key |
| `POLY_PASSPHRASE` | Derived passphrase |

HMAC signature is computed as:

```go
func BuildHMACSignature(secret string, timestamp int64, method, requestPath, body string) string {
    key := base64decode(secret)
    message := fmt.Sprintf("%d%s%s%s", timestamp, method, requestPath, body)
    mac := hmac.New(sha256.New, key)
    mac.Write([]byte(message))
    return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
```

---

## Order Building

### Order Parameters

```go
type OrderArgs struct {
    TokenID  string  // CLOB token ID
    Side     string  // always "BUY" for Polymarket
    Price    float64 // limit price in USDC
    SizeUSDC float64 // total USDC to spend
}
```

### Signed Order Structure

```go
type SignedOrderV2 struct {
    Salt          string
    Maker         string
    Signer        string
    TokenID       string
    MakerAmount   string  // size in token decimals (rounded down)
    TakerAmount   string  // cost in USDC decimals (rounded normal)
    Side          string  // "BUY"
    Expiration    string  // Unix timestamp + 1 year
    SignatureType string  // "2" (EIP-712)
    Timestamp     string  // milliseconds
    Metadata      string  // condition_id padded to 32 bytes
    Builder       string  // zero address
    Signature     string  // ECDSA signature (r, s, v)
}
```

### Rounding

Token amounts are rounded according to CLOB tick size:

| Tick Size | Price Decimals | Size Decimals |
|---|---|---|
| 0.1 | 1 | 0 (round down) |
| 0.01 | 2 | 1 (round down) |
| 0.001 | 3 | 3 (round down) |
| 0.0001 | 4 | 4 (round down) |

Taker amount (USDC) always uses "normal" rounding (math.Round).

### Amount Computation

```
makerAmount = roundDown(sizeUSDC / price, tickSizeDecimals)
takerAmount = roundNormal(makerAmount * price * 1e6, 0)
```

Where `1e6` accounts for USDC being a 6-decimal token.

---

## Contract Addresses

| Contract | Address |
|---|---|
| Exchange (CTF) | `0xE111180000d2663C0091e4f400237545B87B996B` |
| NegRisk Exchange | `0xe2222d279d744050d28e00520010520000310F59` |
| Zero Address | `0x0000000000000000000000000000000000000000` |
| Bytes32 Zero | `0x0000000000000000000000000000000000000000000000000000000000000000` |

---

## Market Data Resolution

To determine if a market is resolved, VagusTrader fetches market data from:

```go
func (c *ClobClient) FetchMarketData(conditionID string) (map[string]interface{}, error)
```

Expected response fields:

| Field | Type | Description |
|---|---|---|
| `condition_id` | string | Market condition ID |
| `tokens` | `[]Token` | Outcome tokens with current prices |
| `resolved` | bool | Whether market is resolved |
| `winner` | string | Token ID of winning outcome (when resolved) |
| `price` | float | Final settlement price per outcome |

---

## Websocket Price Feed

For real-time price tracking, the `ws` package provides:

```go
func NewMarketFeed(tokenIDs []string) *MarketFeed
func (f *MarketFeed) Stream(ctx context.Context, out chan<- PriceUpdate)
```

Connects to `wss://ws-subscriptions-clob.polymarket.com/ws/market` with automatic reconnection (5s delay, infinite retries).

---

## Error Handling

- Network errors: all HTTP clients use 10-15s timeouts
- Failed HMAC: returns HTTP 401 — automatically retried if credentials were freshly derived
- Order rejection: CLOB returns error details in response body
- Rate limiting: no explicit rate limit backoff (depends on poll interval)
