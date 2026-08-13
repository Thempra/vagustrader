package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/db"
)

type BuyRequest struct {
	TokenID     string
	MarketID    string
	Outcome     string
	Price       float64
	SizeUSDC    float64
	TickSize    string
	NegRisk     bool
	SourceJSON  string
	Strategy    string
}

type TradeResult struct {
	OrderID    string
	FilledSize float64
	Price      float64
	Status     string
}

func (b *Broker) Buy(ctx context.Context, req BuyRequest) (*TradeResult, error) {
	if req.TickSize == "" {
		req.TickSize = "0.01"
	}
	buyPrice := snapToTick(req.Price, req.TickSize)

	if b.paper && req.Strategy != "" {
		walletBalance, _ := b.GetStrategyBalance(req.Strategy)
		if walletBalance < req.SizeUSDC {
			return nil, fmt.Errorf("insufficient paper balance for %s: %.2f < %.2f", req.Strategy, walletBalance, req.SizeUSDC)
		}
	}

	var exchangeAddr string
	if req.NegRisk {
		exchangeAddr = clob.NegRiskExchangeV2
	} else {
		exchangeAddr = clob.ExchangeV2
	}

	orderResult, err := b.clob.PlaceLimitOrder(clob.OrderArgs{
		TokenID:  req.TokenID,
		Side:     "BUY",
		Price:    buyPrice,
		SizeUSDC: req.SizeUSDC,
		TickSize: req.TickSize,
	}, req.MarketID, exchangeAddr)
	if err != nil {
		if isNoBalanceError(err) {
			return nil, fmt.Errorf("insufficient balance: %w", err)
		}
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(strings.ToLower(err.Error()), "orderbook") {
			return nil, &DeadTokenError{TokenID: req.TokenID}
		}
		return nil, fmt.Errorf("place order: %w", err)
	}

	var orderID string
	if orderResult != nil {
		if oid, ok := orderResult["orderID"].(string); ok {
			orderID = oid
		} else if oid, ok := orderResult["id"].(string); ok {
			orderID = oid
		}
	}

	filledSize := req.SizeUSDC
	fillStatus := "filled"
	if orderID != "" {
		time.Sleep(2 * time.Second)
		orderInfo, orderErr := b.clob.GetOrder(orderID)
		if orderErr != nil {
			slog.Warn("Could not verify order fill", "orderID", orderID, "error", orderErr)
		} else if orderInfo != nil {
			fillStatus = orderInfo.Status
			if orderInfo.SizeFilled > 0 && orderInfo.Price > 0 {
				filledSize = orderInfo.SizeFilled * orderInfo.Price
			}
			slog.Info("Order status", "orderID", orderID, "status", fillStatus, "filledUSDC", filledSize)
		}
	}

	if !b.paper {
		if fillStatus != "FILLED" && fillStatus != "filled" && fillStatus != "MATCHED" && fillStatus != "LIVE" {
			return nil, fmt.Errorf("order not filled: %s (orderID=%s)", fillStatus, orderID)
		}
	}

	if filledSize < 1.0 {
		return nil, fmt.Errorf("fill too small: $%.2f (orderID=%s)", filledSize, orderID)
	}

	if _, dbErr := b.database.RecordTrade(&db.Trade{
		Ts:         time.Now().Unix(),
		MarketID:   req.MarketID,
		TokenID:    req.TokenID,
		Side:       "BUY",
		Outcome:    req.Outcome,
		Price:      buyPrice,
		SizeUSDC:   filledSize,
		OrderID:    orderID,
		Paper:      b.paper,
		SourceJSON: req.SourceJSON,
		Status:     "filled",
		Strategy:   req.Strategy,
	}); dbErr != nil {
		slog.Error("record trade", "error", dbErr)
	}

	if dbErr := b.database.UpsertPosition(&db.Position{
		TokenID:   req.TokenID,
		MarketID:  req.MarketID,
		Outcome:   req.Outcome,
		Size:      filledSize / buyPrice,
		AvgPrice:  buyPrice,
		Strategy:  req.Strategy,
		UpdatedTs: time.Now().Unix(),
	}); dbErr != nil {
		slog.Error("upsert position", "error", dbErr)
	}

	if b.paper && req.Strategy != "" {
		if err := b.database.DeductWallet(req.Strategy, filledSize); err != nil {
			slog.Error("deduct wallet", "strategy", req.Strategy, "error", err)
		}
	}

	sourceJSON := req.SourceJSON
	var sources []map[string]interface{}
	if json.Unmarshal([]byte(sourceJSON), &sources) == nil && len(sources) > 0 {
	} else {
		sourceJSON = "[]"
	}

	return &TradeResult{
		OrderID:    orderID,
		FilledSize: filledSize,
		Price:      buyPrice,
		Status:     fillStatus,
	}, nil
}

type DeadTokenError struct {
	TokenID string
}

func (e *DeadTokenError) Error() string {
	return "dead orderbook for token " + e.TokenID[:min(12, len(e.TokenID))]
}
