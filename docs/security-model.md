# Security Model

## Overview

VagusTrader interacts with Polymarket's CLOB exchange, which requires two forms of authentication:

1. **EIP-712 typed data signing** — cryptographic proof of wallet control
2. **HMAC-SHA256 request signing** — per-request authentication for CLOB API

---

## Key Architecture

```
┌─────────────────────────────────┐
│        Private Key (ECDSA)      │
│         (from PRIVATE_KEY)      │
└────────┬────────────────────────┘
         │
         ▼
    ┌──────────┐
    │  Signer  │
    │ EIP-712  │
    └────┬─────┘
         │
    ┌────┴──────┐
    │ SignClobAuth()  │── POST /auth/api-key ──▶ Derive CLOB API Credentials
    │ SignOrderHash()  │── POST /order        ──▶ Place signed order
    └───────────────┘
```

## EIP-712 Signing (go-ethereum)

All signing is done natively in Go using `github.com/ethereum/go-ethereum` with zero Python dependencies.

### Signer Struct

```go
type Signer struct {
    privateKey *ecdsa.PrivateKey
    address    common.Address
    chainID    int
}
```

Initialized from hex-encoded private key (with or without `0x` prefix):

```go
signer, err := NewSigner(privateKeyHex, chainID)
```

### CLOB Auth Signing (`SignClobAuth`)

Used to derive API credentials. Signs a typed data message proving wallet ownership:

```go
func (s *Signer) SignClobAuth(timestamp int64, nonce int) (string, error)
```

The typed data uses domain `ClobAuthDomain` with the wallet address as the verifier. The signature is sent to `POST /auth/api-key` which returns `(apiKey, secret, passphrase)`.

### Order Signing (`SignOrderStructHash`)

Used to place limit orders:

```go
func (s *Signer) SignOrderStructHash(structHash []byte) (string, error)
```

The order struct hash is derived from the EIP-712 typed data for `"Polymarket CTF Exchange"` (domain version 1). Contains: maker address, token amounts, side, salt, expiration, signature type.

The full order signing flow:

```
OrderArgs → BuildSignedOrder
  ├── Create EIP-712 typed data with domain + message
  ├── HashStruct() → structHash
  ├── Sign(structHash) → signature
  └── Return SignedOrderV2 with all fields + signature
```

Signature format: standard ECDSA with `v = v + 27` recovery byte adjustment.

## HMAC Request Signing

Authenticated CLOB API requests use HMAC-SHA256:

```go
func BuildHMACSignature(secret string, timestamp int64, method, requestPath, body string) string
```

Algorithm:
```
key     = base64_decode(api_secret)
message = timestamp + method + path + body
signature = HMAC-SHA256(key, message)
```

Sent as headers:

| Header | Value |
|---|---|
| `POLY_ADDRESS` | Ethereum address |
| `POLY_SIGNATURE` | EIP-712 auth signature |
| `POLY_TIMESTAMP` | Current Unix millis |
| `POLY_API_KEY` | Derived API key |
| `POLY_PASSPHRASE` | Derived API passphrase |

## Credential Derivation Flow

```
1. Startup:
   ├── Are CLOB_API_KEY/SECRET/PASSPHRASE set?
   │   ├── YES → use them directly
   │   └── NO  → derive from private key:
   │       ├── EIP-712 sign current timestamp + nonce
   │       ├── POST /auth/api-key
   │       └── Store result as ClobCreds
   └── Return ClobCreds

2. Order Placement:
   ├── BuildSignedOrder(signer, args, exchange, tickSize)
   ├── Add HMAC signature to request
   └── POST /order with auth headers
```

Credentials are resolved once at startup and reused for the lifetime of the process. They are never written to disk by the bot.

## Paper Trading Mode

In paper mode (`--paper` flag):

- `PaperClient` replaces `ClobClient` for all trading operations
- No API keys, signing, or network requests are required
- Order IDs are generated as `paper-{timestamp}`
- Balance is simulated (always returns $10,000)
- Trades are still recorded in the database with `paper = 1`

## Database Security

- SQLite database is stored in `./data/vagustrader.db`
- No encryption at rest (not suitable for environments where DB file is accessible to untrusted parties)
- WAL mode enables concurrent read/write
- The DB contains trade history and PnL data but **never** contains private keys or API secrets

## Recommendations

| Concern | Recommendation |
|---|---|
| Private key storage | Use a dedicated Polymarket wallet with limited funds. Never share `.env` files. |
| API credentials | Derive on each startup rather than storing in `.env`. |
| Production deployment | Run behind a reverse proxy (e.g. nginx) for the dashboard. Use `--paper` for testing. |
| Logging | Log files contain market data and trade info but never secrets. Rotate logs daily (built-in). |
| Dashboard exposure | Dashboard is unprotected HTTP. Do not expose to the public internet without authentication. |
