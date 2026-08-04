package clob

import (
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
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

func (s *Signer) PrivateKey() *ecdsa.PrivateKey {
	return s.privateKey
}

func (s *Signer) SignHash(hash []byte) ([]byte, error) {
	sig, err := crypto.Sign(hash, s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	sig[64] += 27
	return sig, nil
}

func (s *Signer) SignClobAuth(walletAddr string, timestamp int64, nonce string) (string, error) {
	addr := s.address.Hex()
	if walletAddr != "" {
		addr = walletAddr
	}
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
			"address":   addr,
			"timestamp": fmt.Sprintf("%d", timestamp),
			"nonce":     nonce,
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

// SignPoly1271Order computes a Solady TypedDataSign wrapped signature for POLY_1271 (signatureType=3).
// contentsHash is the EIP-712 struct hash of the Order.
// depositWallet is the address of the proxy wallet acting as maker/signer.
// exchangeDomainSep is the domain separator of the exchange contract (4-field, no salt).
func (s *Signer) SignPoly1271Order(contentsHash []byte, depositWallet common.Address, exchangeDomainSep []byte) (string, error) {
	nameHash := crypto.Keccak256Hash([]byte("DepositWallet"))
	versionHash := crypto.Keccak256Hash([]byte("1"))
	chainID := big.NewInt(int64(s.chainID))
	depositAddr := common.LeftPadBytes(depositWallet.Bytes(), 32)
	zeroSalt := make([]byte, 32)

	// TypedDataSign struct hash: keccak256(abi.encode(TypedDataSign typeHash, contents, nameHash, versionHash, chainId, verifyingContract, salt))
	// Per ERC-7739, the typedDataSignTypeHash appends the full contentsType after the closing paren.
	orderTypeStr := "Order(uint256 salt,address maker,address signer,uint256 tokenId,uint256 makerAmount,uint256 takerAmount,uint8 side,uint8 signatureType,uint256 timestamp,bytes32 metadata,bytes32 builder)"
	typedDataSignTypeHash := crypto.Keccak256([]byte("TypedDataSign(Order contents,string name,string version,uint256 chainId,address verifyingContract,bytes32 salt)" + orderTypeStr))
	structInput := append([]byte{}, typedDataSignTypeHash...)
	structInput = append(structInput, contentsHash...)
	structInput = append(structInput, nameHash.Bytes()...)
	structInput = append(structInput, versionHash.Bytes()...)
	structInput = append(structInput, common.LeftPadBytes(chainID.Bytes(), 32)...)
	structInput = append(structInput, depositAddr...)
	structInput = append(structInput, zeroSalt...)
	typedStructHash := crypto.Keccak256(structInput)

	// Digest = keccak(0x1901 || exchangeDomainSep || typedStructHash)
	// The outer domain is the EXCHANGE domain (not the deposit wallet domain).
	// The Solady wallet reconstructs: keccak256(0x1901 || appDomainSep || hashStruct(TypedDataSign))
	// where appDomainSep is the exchange domain separator stored in the packed sig.
	digest := crypto.Keccak256([]byte{0x19, 0x01}, exchangeDomainSep, typedStructHash)

	sig, err := s.SignHash(digest)
	if err != nil {
		return "", fmt.Errorf("sign poly1271: %w", err)
	}

	// Pack: ECDSA_sig_65 + exchangeDomainSep_32 + contentsHash_32 + typeString + typeLen_2
	typeBytes := []byte(orderTypeStr)
	typeLen := make([]byte, 2)
	binary.BigEndian.PutUint16(typeLen, uint16(len(typeBytes)))

	finalSig := make([]byte, 0, 65+32+32+len(typeBytes)+2)
	finalSig = append(finalSig, sig...)
	finalSig = append(finalSig, exchangeDomainSep...)
	finalSig = append(finalSig, contentsHash...)
	finalSig = append(finalSig, typeBytes...)
	finalSig = append(finalSig, typeLen...)

	return "0x" + hex.EncodeToString(finalSig), nil
}

// SignClobAuthPoly1271 wraps the ClobAuth EIP-712 struct hash in a Solady TypedDataSign
// (ERC-7739) wrapper, enabling ERC-1271 verification by the deposit wallet contract.
// The CLOB API uses this to derive API keys bound to the deposit wallet address.
func (s *Signer) SignClobAuthPoly1271(timestamp int64, nonce string, depositWallet common.Address) (string, error) {
	td := apitypes.TypedData{
		Types: apitypes.Types{
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
			"address":   depositWallet.Hex(),
			"timestamp": fmt.Sprintf("%d", timestamp),
			"nonce":     nonce,
			"message":   "This message attests that I control the given wallet",
		},
	}

	clobAuthHash, err := td.HashStruct("ClobAuth", td.Message)
	if err != nil {
		return "", fmt.Errorf("hash clob auth: %w", err)
	}

	nameHash := crypto.Keccak256Hash([]byte("DepositWallet"))
	versionHash := crypto.Keccak256Hash([]byte("1"))
	chainID := big.NewInt(int64(s.chainID))
	depositAddr := common.LeftPadBytes(depositWallet.Bytes(), 32)
	zeroSalt := make([]byte, 32)

	typedDomainTypeHash := crypto.Keccak256([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract,bytes32 salt)"))
	sepInput := append([]byte{}, typedDomainTypeHash...)
	sepInput = append(sepInput, nameHash.Bytes()...)
	sepInput = append(sepInput, versionHash.Bytes()...)
	sepInput = append(sepInput, common.LeftPadBytes(chainID.Bytes(), 32)...)
	sepInput = append(sepInput, depositAddr...)
	sepInput = append(sepInput, zeroSalt...)
	typedDomainSep := crypto.Keccak256(sepInput)

	clobAuthTypeStr := "ClobAuth(address address,string timestamp,uint256 nonce,string message)"
	typedDataSignTypeHash := crypto.Keccak256([]byte("TypedDataSign(ClobAuth contents,string name,string version,uint256 chainId,address verifyingContract,bytes32 salt)" + clobAuthTypeStr))
	structInput := append([]byte{}, typedDataSignTypeHash...)
	structInput = append(structInput, clobAuthHash...)
	structInput = append(structInput, nameHash.Bytes()...)
	structInput = append(structInput, versionHash.Bytes()...)
	structInput = append(structInput, common.LeftPadBytes(chainID.Bytes(), 32)...)
	structInput = append(structInput, depositAddr...)
	structInput = append(structInput, zeroSalt...)
	typedStructHash := crypto.Keccak256(structInput)

	digest := crypto.Keccak256([]byte{0x19, 0x01}, typedDomainSep, typedStructHash)

	sig, err := s.SignHash(digest)
	if err != nil {
		return "", fmt.Errorf("sign clob auth poly1271: %w", err)
	}

	typeBytes := []byte(clobAuthTypeStr)
	typeLen := make([]byte, 2)
	binary.BigEndian.PutUint16(typeLen, uint16(len(typeBytes)))

	finalSig := make([]byte, 0, 65+32+32+len(typeBytes)+2)
	finalSig = append(finalSig, sig...)
	finalSig = append(finalSig, typedDomainSep...)
	finalSig = append(finalSig, clobAuthHash...)
	finalSig = append(finalSig, typeBytes...)
	finalSig = append(finalSig, typeLen...)

	return "0x" + hex.EncodeToString(finalSig), nil
}
