package clob

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"

	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

type RoundConfig struct {
	Price  int
	Size   int
	Amount int
}

var roundingConfigs = map[string]RoundConfig{
	"0.01":   {2, 2, 4},
	"0.001":  {3, 2, 5},
	"0.1":    {1, 2, 3},
	"0.0001": {4, 2, 6},
}

func toTokenDecimals(x float64) int64 {
	return int64(math.Round(x * 1e6))
}

func roundDown(x float64, places int) float64 {
	pow := math.Pow10(places)
	return math.Floor(x*pow) / pow
}

func roundNormal(x float64, places int) float64 {
	pow := math.Pow10(places)
	return math.Round(x*pow) / pow
}

func decimalPlaces(f float64) int {
	str := strconv.FormatFloat(f, 'f', -1, 64)
	dot := len(str) - 1
	for i, c := range str {
		if c == '.' {
			dot = i
			break
		}
	}
	dp := len(str) - 1 - dot
	if dp < 0 {
		return 0
	}
	return dp
}

type SignedOrderV2 struct {
	Salt          int64  `json:"salt"`
	Maker         string `json:"maker"`
	Signer        string `json:"signer"`
	TokenID       string `json:"taker_id"`
	MakerAmount   string `json:"maker_amount"`
	TakerAmount   string `json:"taker_amount"`
	Side          int    `json:"side"`
	Expiration    int    `json:"expiration"`
	SignatureType int    `json:"signature_type"`
	Timestamp     string `json:"timestamp"`
	Metadata      string `json:"metadata,omitempty"`
	Builder       string `json:"builder,omitempty"`
	Signature     string `json:"signature"`
}

func BuildSignedOrder(signer *Signer, args OrderArgs, exchangeAddr string, tickSize string) (*SignedOrderV2, error) {
	rc, ok := roundingConfigs[tickSize]
	if !ok {
		rc = roundingConfigs["0.01"]
	}

	price := args.Price
	size := args.SizeUSDC

	var makerAmt, takerAmt float64
	switch args.Side {
	case "BUY":
		takerAmt = roundDown(size, rc.Size)
		makerAmt = roundNormal(takerAmt*price, rc.Amount)
		dp := decimalPlaces(makerAmt)
		if dp > rc.Amount {
			makerAmt = roundDown(makerAmt, rc.Amount)
		}
	case "SELL":
		makerAmt = roundDown(size, rc.Size)
		takerAmt = roundNormal(makerAmt*price, rc.Amount)
		dp := decimalPlaces(takerAmt)
		if dp > rc.Amount {
			takerAmt = roundDown(takerAmt, rc.Amount)
		}
	default:
		return nil, fmt.Errorf("invalid side: %s", args.Side)
	}

	makerAmtTok := toTokenDecimals(makerAmt)
	takerAmtTok := toTokenDecimals(takerAmt)

	saltBig, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 256))
	if err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	now := time.Now().UnixMilli()
	timestamp := strconv.FormatInt(now, 10)

	sideInt := 0
	if args.Side == "SELL" {
		sideInt = 1
	}

	domain := apitypes.TypedDataDomain{
		Name:              "Polymarket CTF Exchange",
		Version:           "1",
		ChainId:           ethmath.NewHexOrDecimal256(int64(signer.ChainID())),
		VerifyingContract: exchangeAddr,
	}

	td := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
				{Name: "verifyingContract", Type: "address"},
			},
			"Order": []apitypes.Type{
				{Name: "salt", Type: "uint256"},
				{Name: "maker", Type: "address"},
				{Name: "signer", Type: "address"},
				{Name: "taker", Type: "address"},
				{Name: "tokenId", Type: "uint256"},
				{Name: "makerAmount", Type: "uint256"},
				{Name: "takerAmount", Type: "uint256"},
				{Name: "expiration", Type: "uint256"},
				{Name: "nonce", Type: "uint256"},
				{Name: "feeRateBps", Type: "uint256"},
				{Name: "side", Type: "uint8"},
				{Name: "signatureType", Type: "uint8"},
			},
		},
		PrimaryType: "Order",
		Domain:      domain,
		Message: apitypes.TypedDataMessage{
			"salt":          saltBig.String(),
			"maker":         signer.Address(),
			"signer":        signer.Address(),
			"taker":         ZeroAddress,
			"tokenId":       args.TokenID,
			"makerAmount":   strconv.FormatInt(makerAmtTok, 10),
			"takerAmount":   strconv.FormatInt(takerAmtTok, 10),
			"expiration":    "0",
			"nonce":         "0",
			"feeRateBps":    "0",
			"side":          strconv.Itoa(sideInt),
			"signatureType": "0",
		},
	}

	domainSep, err := td.HashStruct("EIP712Domain", td.Domain.Map())
	if err != nil {
		return nil, fmt.Errorf("hash domain: %w", err)
	}

	msgHash, err := td.HashStruct("Order", td.Message)
	if err != nil {
		return nil, fmt.Errorf("hash order: %w", err)
	}

	rawHash := crypto.Keccak256(
		[]byte{0x19, 0x01},
		domainSep,
		msgHash,
	)

	sig, err := signer.SignHash(rawHash)
	if err != nil {
		return nil, fmt.Errorf("sign order: %w", err)
	}

	return &SignedOrderV2{
		Salt:          saltBig.Int64(),
		Maker:         signer.Address(),
		Signer:        signer.Address(),
		TokenID:       args.TokenID,
		MakerAmount:   strconv.FormatInt(makerAmtTok, 10),
		TakerAmount:   strconv.FormatInt(takerAmtTok, 10),
		Side:          sideInt,
		Expiration:    0,
		SignatureType: 0,
		Timestamp:     timestamp,
		Signature:     "0x" + hex.EncodeToString(sig),
	}, nil
}
