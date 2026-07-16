package clob

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

type Signer struct {
	privateKey *ecdsa.PrivateKey
	address    common.Address
	chainID    int
}

func NewSigner(privateKeyHex string, chainID int) (*Signer, error) {
	key := privateKeyHex
	if strings.HasPrefix(key, "0x") || strings.HasPrefix(key, "0X") {
		key = key[2:]
	}
	privateKey, err := crypto.HexToECDSA(key)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}
	pub, ok := privateKey.Public().(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("failed to derive public key")
	}
	return &Signer{
		privateKey: privateKey,
		address:    crypto.PubkeyToAddress(*pub),
		chainID:    chainID,
	}, nil
}

func (s *Signer) Address() string {
	return s.address.Hex()
}

func (s *Signer) ChainID() int {
	return s.chainID
}

func (s *Signer) SignHash(hash []byte) ([]byte, error) {
	sig, err := crypto.Sign(hash, s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	sig[64] += 27
	return sig, nil
}

func (s *Signer) SignClobAuth(timestamp int64, nonce int) (string, error) {
	td := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{Name: "name", Type: "string"},
				{Name: "version", Type: "string"},
				{Name: "chainId", Type: "uint256"},
			},
			"ClobAuth": []apitypes.Type{
				{Name: "address", Type: "address"},
				{Name: "timestamp", Type: "string"},
				{Name: "nonce", Type: "uint256"},
				{Name: "message", Type: "string"},
			},
		},
		PrimaryType: "ClobAuth",
		Domain: apitypes.TypedDataDomain{
			Name:    "ClobAuthDomain",
			Version: "1",
			ChainId: math.NewHexOrDecimal256(int64(s.chainID)),
		},
		Message: map[string]interface{}{
			"address":   s.address.Hex(),
			"timestamp": fmt.Sprintf("%d", timestamp),
			"nonce":     fmt.Sprintf("%d", nonce),
			"message":   "This message attests that I control the given wallet",
		},
	}

	domainSep, err := td.HashStruct("EIP712Domain", td.Domain.Map())
	if err != nil {
		return "", fmt.Errorf("hash domain: %w", err)
	}

	msgHash, err := td.HashStruct("ClobAuth", td.Message)
	if err != nil {
		return "", fmt.Errorf("hash message: %w", err)
	}

	rawHash := crypto.Keccak256(
		[]byte{0x19, 0x01},
		domainSep,
		msgHash,
	)

	sig, err := s.SignHash(rawHash)
	if err != nil {
		return "", fmt.Errorf("sign clob auth: %w", err)
	}

	return "0x" + hex.EncodeToString(sig), nil
}

func (s *Signer) SignOrderStructHash(structHash []byte) (string, error) {
	sig, err := s.SignHash(structHash)
	if err != nil {
		return "", fmt.Errorf("sign order struct hash: %w", err)
	}
	return "0x" + hex.EncodeToString(sig), nil
}
