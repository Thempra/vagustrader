package clob

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

var (
	usdcNative          = common.HexToAddress("0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359")
	usdcEBridged        = common.HexToAddress("0x2791Bca1f2de4661ED88A30C99A7a9449Aa84174")
	onrampContract      = common.HexToAddress("0x93070a847efEf7F70739046A929D47a521F5B8ee")
	exchangeContract    = common.HexToAddress(ExchangeV2)
	quickswapV2Router   = common.HexToAddress("0xa5E0829CaCEd8fFDD4De3c43696c57F7D7A678ff")
	quickswapV3Router   = common.HexToAddress("0xf6f32bC8C7fB840E0681f07C7A9b9D46D9F628f8")

	approveSelector            = crypto.Keccak256([]byte("approve(address,uint256)"))[:4]
	balanceOfSelector          = crypto.Keccak256([]byte("balanceOf(address)"))[:4]
	allowanceSelector          = crypto.Keccak256([]byte("allowance(address,address)"))[:4]
	wrapSelector               = crypto.Keccak256([]byte("wrap(address,address,uint256)"))[:4]
	swapV2Selector             = crypto.Keccak256([]byte("swapExactTokensForTokens(uint256,uint256,address[],address,uint256)"))[:4]
	exactInputSingleSelector   = crypto.Keccak256([]byte("exactInputSingle((address,address,uint24,address,uint256,uint256,uint256,uint160))"))[:4]
	pausedSelector             = crypto.Keccak256([]byte("paused(address)"))[:4]
)

func (c *ClobClient) CheckUSDCBalance(rpcURL string) (float64, float64, error) {
	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return 0, 0, fmt.Errorf("dial rpc: %w", err)
	}
	defer client.Close()

	addr := common.HexToAddress(c.signer.Address())
	balNative, _ := getTokenBalance(ctx, client, usdcNative, addr)
	balBridged, _ := getTokenBalance(ctx, client, usdcEBridged, addr)
	return fromWei(balNative, 6), fromWei(balBridged, 6), nil
}

func (c *ClobClient) ApproveToken(rpcURL string, token, spender common.Address, amountWei *big.Int) error {
	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return fmt.Errorf("dial rpc: %w", err)
	}
	defer client.Close()
	return c.approveTokenTx(ctx, client, token, spender, amountWei)
}

func (c *ClobClient) ApproveUSDC(rpcURL string, spender common.Address, amountWei *big.Int) error {
	return c.ApproveToken(rpcURL, usdcNative, spender, amountWei)
}

func (c *ClobClient) approveTokenTx(ctx context.Context, client *ethclient.Client, token, spender common.Address, amount *big.Int) error {
	from := common.HexToAddress(c.signer.Address())
	existing, err := getAllowance(ctx, client, token, from, spender)
	if err != nil {
		return fmt.Errorf("check allowance: %w", err)
	}
	if existing.Cmp(amount) >= 0 {
		slog.Info("Allowance sufficient", "token", token.Hex(), "spender", spender.Hex())
		return nil
	}
	slog.Info("Approving token", "token", token.Hex(), "spender", spender.Hex(), "amount", amount.String())
	tx, err := c.sendTx(ctx, client, token, big.NewInt(0), approveSelector,
		common.LeftPadBytes(spender.Bytes(), 32),
		common.LeftPadBytes(amount.Bytes(), 32))
	if err != nil {
		return fmt.Errorf("approve tx: %w", err)
	}
	return waitTx(ctx, client, tx)
}

func (c *ClobClient) isPaused(ctx context.Context, client *ethclient.Client, token common.Address) (bool, error) {
	data := append(pausedSelector, common.LeftPadBytes(token.Bytes(), 32)...)
	resp, err := client.CallContract(ctx, ethereum.CallMsg{To: &onrampContract, Data: data}, nil)
	if err != nil {
		return true, fmt.Errorf("check paused: %w", err)
	}
	return len(resp) > 0 && resp[len(resp)-1] != 0, nil
}

func (c *ClobClient) swapUSDCToUSDCe(ctx context.Context, client *ethclient.Client, amountIn *big.Int) error {
	addr := common.HexToAddress(c.signer.Address())

	// Approve for both routers upfront (cheap to do)
	if err := c.approveTokenTx(ctx, client, usdcNative, quickswapV3Router, amountIn); err != nil {
		return fmt.Errorf("approve USDC for V3: %w", err)
	}

	// Try QuickSwap V3 with different fee tiers
	feeTiers := []uint64{500, 100, 3000, 10000}
	for _, fee := range feeTiers {
		minOut := new(big.Int).Mul(amountIn, big.NewInt(99))
		minOut.Div(minOut, big.NewInt(100))

		data := append([]byte{}, exactInputSingleSelector...)
		data = append(data, common.LeftPadBytes(usdcNative.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(usdcEBridged.Bytes(), 32)...)
		feePadded := new(big.Int).SetUint64(fee)
		data = append(data, common.LeftPadBytes(feePadded.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(addr.Bytes(), 32)...)
		deadline := big.NewInt(time.Now().Add(30 * time.Minute).Unix())
		data = append(data, common.LeftPadBytes(deadline.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(amountIn.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(minOut.Bytes(), 32)...)
		data = append(data, common.LeftPadBytes(big.NewInt(0).Bytes(), 32)...)

		slog.Info("Trying QuickSwap V3 swap", "fee", fee)
		tx, err := c.sendTx(ctx, client, quickswapV3Router, big.NewInt(0), data)
		if err != nil {
			slog.Warn("V3 swap failed", "fee", fee, "err", err)
			continue
		}
		return waitTx(ctx, client, tx)
	}

	// V3 no pool found, approve V2 and try
	if err := c.approveTokenTx(ctx, client, usdcNative, quickswapV2Router, amountIn); err != nil {
		return fmt.Errorf("approve USDC for V2: %w", err)
	}

	slog.Info("V3 failed, trying QuickSwap V2 with very low minOut")
	minOut := big.NewInt(1)
	deadline := big.NewInt(time.Now().Add(30 * time.Minute).Unix())
	path := []common.Address{usdcNative, usdcEBridged}

	data := append([]byte{}, swapV2Selector...)
	data = append(data, common.LeftPadBytes(amountIn.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(minOut.Bytes(), 32)...)
	off := new(big.Int).SetInt64(160)
	data = append(data, common.LeftPadBytes(off.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(addr.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(deadline.Bytes(), 32)...)
	pathLen := new(big.Int).SetInt64(int64(len(path)))
	data = append(data, common.LeftPadBytes(pathLen.Bytes(), 32)...)
	for _, p := range path {
		data = append(data, common.LeftPadBytes(p.Bytes(), 32)...)
	}

	tx, err := c.sendTx(ctx, client, quickswapV2Router, big.NewInt(0), data)
	if err != nil {
		return fmt.Errorf("all swap routes failed (last V2: %w)", err)
	}
	return waitTx(ctx, client, tx)
}

func (c *ClobClient) DepositToExchange(rpcURL string, amountUSDC float64) error {
	ctx := context.Background()
	client, err := ethclient.DialContext(ctx, rpcURL)
	if err != nil {
		return fmt.Errorf("dial rpc: %w", err)
	}
	defer client.Close()

	amount := toWei(amountUSDC, 6)
	addr := common.HexToAddress(c.signer.Address())

	balNat, err := getTokenBalance(ctx, client, usdcNative, addr)
	if err != nil {
		return fmt.Errorf("check native USDC: %w", err)
	}
	balBri, err := getTokenBalance(ctx, client, usdcEBridged, addr)
	if err != nil {
		return fmt.Errorf("check bridged USDC: %w", err)
	}
	slog.Info("Balances", "USDC", fromWei(balNat, 6), "USDC.e", fromWei(balBri, 6))

	// If we have USDC.e, wrap it directly (no swap needed)
	if balBri.Cmp(amount) >= 0 {
		wrapAmount := amount
		slog.Info("Wrapping USDC.e to PMCT", "amount", fromWei(wrapAmount, 6))
		if err := c.approveTokenTx(ctx, client, usdcEBridged, onrampContract, wrapAmount); err != nil {
			return fmt.Errorf("approve USDC.e: %w", err)
		}
		tx, err := c.sendTx(ctx, client, onrampContract, big.NewInt(0), wrapSelector,
			common.LeftPadBytes(usdcEBridged.Bytes(), 32),
			common.LeftPadBytes(addr.Bytes(), 32),
			common.LeftPadBytes(wrapAmount.Bytes(), 32),
		)
		if err != nil {
			return fmt.Errorf("wrap USDC.e tx: %w", err)
		}
		return waitTx(ctx, client, tx)
	}

	if balBri.Cmp(big.NewInt(0)) > 0 {
		slog.Info("USDC.e balance below target, wrapping what we have", "amount", fromWei(balBri, 6))
		if err := c.approveTokenTx(ctx, client, usdcEBridged, onrampContract, balBri); err != nil {
			return fmt.Errorf("approve USDC.e: %w", err)
		}
		tx, err := c.sendTx(ctx, client, onrampContract, big.NewInt(0), wrapSelector,
			common.LeftPadBytes(usdcEBridged.Bytes(), 32),
			common.LeftPadBytes(addr.Bytes(), 32),
			common.LeftPadBytes(balBri.Bytes(), 32),
		)
		if err != nil {
			return fmt.Errorf("wrap USDC.e tx: %w", err)
		}
		return waitTx(ctx, client, tx)
	}

	// No USDC.e, try native USDC
	if balNat.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("no USDC or USDC.e available")
	}

	paused, err := c.isPaused(ctx, client, usdcNative)
	if err != nil {
		slog.Warn("Could not check pause status, assuming paused", "err", err)
		paused = true
	}

	if !paused {
		wrapAmount := amount
		if balNat.Cmp(amount) < 0 {
			wrapAmount = balNat
		}
		slog.Info("Wrapping USDC to PMCT", "amount", fromWei(wrapAmount, 6))
		if err := c.approveTokenTx(ctx, client, usdcNative, onrampContract, wrapAmount); err != nil {
			return fmt.Errorf("approve onramp: %w", err)
		}
		tx, err := c.sendTx(ctx, client, onrampContract, big.NewInt(0), wrapSelector,
			common.LeftPadBytes(usdcNative.Bytes(), 32),
			common.LeftPadBytes(addr.Bytes(), 32),
			common.LeftPadBytes(wrapAmount.Bytes(), 32),
		)
		if err != nil {
			return fmt.Errorf("wrap tx: %w", err)
		}
		return waitTx(ctx, client, tx)
	}

	slog.Info("USDC paused, swapping USDC -> USDC.e then wrapping")
	swapAmount := amount
	if balNat.Cmp(swapAmount) < 0 {
		swapAmount = balNat
	}
	if err := c.swapUSDCToUSDCe(ctx, client, swapAmount); err != nil {
		return fmt.Errorf("swap: %w", err)
	}

	balBri, err = getTokenBalance(ctx, client, usdcEBridged, addr)
	if err != nil {
		return fmt.Errorf("check USDC.e: %w", err)
	}
	if balBri.Cmp(big.NewInt(0)) <= 0 {
		return fmt.Errorf("USDC.e is zero after swap")
	}

	if err := c.approveTokenTx(ctx, client, usdcEBridged, onrampContract, balBri); err != nil {
		return fmt.Errorf("approve USDC.e: %w", err)
	}

	slog.Info("Wrapping USDC.e to PMCT", "amount", fromWei(balBri, 6))
	tx, err := c.sendTx(ctx, client, onrampContract, big.NewInt(0), wrapSelector,
		common.LeftPadBytes(usdcEBridged.Bytes(), 32),
		common.LeftPadBytes(addr.Bytes(), 32),
		common.LeftPadBytes(balBri.Bytes(), 32),
	)
	if err != nil {
		return fmt.Errorf("wrap USDC.e tx: %w", err)
	}
	return waitTx(ctx, client, tx)
}

func getTokenBalance(ctx context.Context, client *ethclient.Client, token, owner common.Address) (*big.Int, error) {
	data := append(balanceOfSelector, common.LeftPadBytes(owner.Bytes(), 32)...)
	resp, err := client.CallContract(ctx, ethereum.CallMsg{To: &token, Data: data}, nil)
	if err != nil {
		return big.NewInt(0), err
	}
	return new(big.Int).SetBytes(resp), nil
}

func getAllowance(ctx context.Context, client *ethclient.Client, token, owner, spender common.Address) (*big.Int, error) {
	data := append(allowanceSelector, common.LeftPadBytes(owner.Bytes(), 32)...)
	data = append(data, common.LeftPadBytes(spender.Bytes(), 32)...)
	resp, err := client.CallContract(ctx, ethereum.CallMsg{To: &token, Data: data}, nil)
	if err != nil {
		return big.NewInt(0), err
	}
	return new(big.Int).SetBytes(resp), nil
}

func (c *ClobClient) sendTx(ctx context.Context, client *ethclient.Client, to common.Address, value *big.Int, selector []byte, args ...[]byte) (*types.Transaction, error) {
	chainID := big.NewInt(int64(c.signer.ChainID()))
	from := common.HexToAddress(c.signer.Address())

	nonce, err := client.PendingNonceAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("get nonce: %w", err)
	}

	gasTip, err := client.SuggestGasTipCap(ctx)
	if err != nil {
		return nil, fmt.Errorf("suggest tip: %w", err)
	}
	gasFee, err := client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("suggest gas price: %w", err)
	}

	var data []byte
	data = append(data, selector...)
	for _, arg := range args {
		data = append(data, arg...)
	}

	gas, err := client.EstimateGas(ctx, ethereum.CallMsg{
		From:  from,
		To:    &to,
		Value: value,
		Data:  data,
	})
	if err != nil {
		return nil, fmt.Errorf("estimate gas: %w", err)
	}

	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: gasTip,
		GasFeeCap: gasFee,
		Gas:       gas,
		To:        &to,
		Value:     value,
		Data:      data,
	})

	signed, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), c.signer.PrivateKey())
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}

	if err := client.SendTransaction(ctx, signed); err != nil {
		return nil, fmt.Errorf("send tx: %w", err)
	}

	return signed, nil
}

func waitTx(ctx context.Context, client *ethclient.Client, tx *types.Transaction) error {
	slog.Info("Waiting for tx", "hash", tx.Hash().Hex())
	for i := 0; i < 300; i++ {
		_, isPending, err := client.TransactionByHash(ctx, tx.Hash())
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		if !isPending {
			receipt, err := client.TransactionReceipt(ctx, tx.Hash())
			if err != nil {
				return err
			}
			if receipt.Status == 0 {
				return fmt.Errorf("tx reverted: %s", tx.Hash().Hex())
			}
			slog.Info("Tx confirmed", "hash", tx.Hash().Hex(), "gasUsed", receipt.GasUsed)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("tx timeout: %s", tx.Hash().Hex())
}

func toWei(amount float64, decimals int) *big.Int {
	multiplier := new(big.Float).SetFloat64(math.Pow10(decimals))
	val := new(big.Float).SetFloat64(amount)
	val.Mul(val, multiplier)
	wei := new(big.Int)
	val.Int(wei)
	return wei
}

func fromWei(wei *big.Int, decimals int) float64 {
	divisor := new(big.Float).SetFloat64(math.Pow10(decimals))
	val := new(big.Float).SetInt(wei)
	val.Quo(val, divisor)
	f, _ := val.Float64()
	return f
}
