package broker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/db"
)

type BuyRequest struct {
	TokenID    string
	MarketID   string
	Outcome    string
	Price      float64
	SizeUSDC   float64
	TickSize   string
	NegRisk    bool
	SourceJSON string
	Strategy   string
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

	if err := b.checkGlobalPositionCap(req); err != nil {
		return nil, err
	}
	if err := b.checkPaperBalance(req); err != nil {
		return nil, err
	}
	if err := b.checkMaxDaysToEnd(req); err != nil {
		return nil, err
	}

	exchangeAddr := exchangeForOrder(req.NegRisk)

	// submittedAt must be captured BEFORE the POST: the data-api fill match
	// is timestamped server-side at match time, and the POST itself can take
	// seconds, so stamping after would miss early matches.
	submittedAt := time.Now().Unix() - 2
	orderResult, err := b.clob.PlaceLimitOrder(clob.OrderArgs{
		TokenID:  req.TokenID,
		Side:     "BUY",
		Price:    buyPrice,
		SizeUSDC: req.SizeUSDC,
		TickSize: req.TickSize,
	}, req.MarketID, exchangeAddr)
	if err != nil {
		return nil, classifyPlaceOrderError(err, req.TokenID)
	}

	orderID := extractOrderID(orderResult)

	filledSize := req.SizeUSDC
	fillStatus := "filled"
	verified := b.paper // paper fills are simulated, treat as verified
	if orderID != "" {
		filledSize, fillStatus, verified = b.verifyOrderFill(orderID, req, submittedAt, filledSize, fillStatus, verified)
	}

	if !b.paper {
		if err := b.checkLiveFill(orderID, fillStatus, verified); err != nil {
			return nil, err
		}
	}

	if filledSize < 1.0 {
		return nil, fmt.Errorf("fill too small: $%.2f (orderID=%s)", filledSize, orderID)
	}

	b.recordFilledBuy(req, buyPrice, filledSize, orderID)

	return &TradeResult{
		OrderID:    orderID,
		FilledSize: filledSize,
		Price:      buyPrice,
		Status:     fillStatus,
	}, nil
}

// P2.5: global hard cap across ALL strategies. This is the last line of
// defence so that no single strategy (or a sizing/counting bug inside
// one) can blow past the account-wide open-position limit. Per-strategy
// limits still apply upstream; this only enforces the aggregate ceiling.
func (b *Broker) checkGlobalPositionCap(req BuyRequest) error {
	cap := b.config.Risk.MaxOpenPositionsGlobal
	if cap <= 0 {
		return nil
	}
	positions, err := b.database.GetPositions()
	if err != nil {
		slog.Warn("Could not count open positions for global cap", "error", err)
		return nil
	}
	if len(positions) >= cap {
		slog.Warn("Global max open positions reached, rejecting buy",
			"open", len(positions), "cap", cap, "strategy", req.Strategy)
		return fmt.Errorf("global max open positions reached: %d >= %d", len(positions), cap)
	}
	return nil
}

func (b *Broker) checkPaperBalance(req BuyRequest) error {
	if !b.paper || req.Strategy == "" {
		return nil
	}
	walletBalance, _ := b.GetStrategyBalance(req.Strategy)
	if walletBalance < req.SizeUSDC {
		return fmt.Errorf("insufficient paper balance for %s: %.2f < %.2f", req.Strategy, walletBalance, req.SizeUSDC)
	}
	return nil
}

// Hard rule, enforced for EVERY strategy at the broker gate: never open
// a position in a market resolving further out than Risk.MaxDaysToEnd
// (default 7 days). Long-dated markets lock collateral for months and
// were the bulk of the live drawdown (Heisman-2026, MLB-season leads).
// Checked BEFORE placing any order. Markets with an unknown end date are
// rejected too — an unverifiable horizon is not a short-dated bet.
func (b *Broker) checkMaxDaysToEnd(req BuyRequest) error {
	maxDays := b.config.Risk.MaxDaysToEnd
	if maxDays <= 0 {
		return nil
	}
	endDateStr := ""
	if md := b.GetMarketData(req.MarketID); md != nil {
		endDateStr, _ = md["end_date_iso"].(string)
	}
	if endDateStr == "" {
		slog.Warn("rejecting buy: market end date unknown (max_days_to_end rule)",
			"market", req.MarketID[:min(12, len(req.MarketID))], "strategy", req.Strategy)
		return fmt.Errorf("market end date unknown; max_days_to_end=%.0f rule rejects unbounded horizons", maxDays)
	}
	endDt, perr := time.Parse(time.RFC3339, endDateStr)
	if perr != nil {
		return fmt.Errorf("market end date %q unparseable; max_days_to_end rule: %w", endDateStr, perr)
	}
	if daysLeft := time.Until(endDt).Hours() / 24; daysLeft > maxDays {
		slog.Warn("rejecting buy: market resolves beyond max_days_to_end",
			"market", req.MarketID[:min(12, len(req.MarketID))], "strategy", req.Strategy,
			"days_left", int(daysLeft), "max_days", int(maxDays))
		return fmt.Errorf("market resolves in %.1f days > max_days_to_end %.0f", daysLeft, maxDays)
	}
	return nil
}

func exchangeForOrder(negRisk bool) string {
	if negRisk {
		return clob.NegRiskExchangeV2
	}
	return clob.ExchangeV2
}

// classifyPlaceOrderError maps a PlaceLimitOrder failure to the error the
// strategies expect: insufficient-balance, dead orderbook, or generic.
func classifyPlaceOrderError(err error, tokenID string) error {
	if isNoBalanceError(err) {
		return fmt.Errorf("insufficient balance: %w", err)
	}
	if strings.Contains(err.Error(), "does not exist") || strings.Contains(strings.ToLower(err.Error()), "orderbook") {
		return &DeadTokenError{TokenID: tokenID}
	}
	return fmt.Errorf("place order: %w", err)
}

func extractOrderID(orderResult map[string]interface{}) string {
	if orderResult == nil {
		return ""
	}
	if oid, ok := orderResult["orderID"].(string); ok {
		return oid
	}
	if oid, ok := orderResult["id"].(string); ok {
		return oid
	}
	return ""
}

// verifyOrderFill confirms execution via the CLOB order endpoint, falling
// back to the public data-api when the order endpoint cannot verify.
// Returns the (possibly corrected) filledSize, fillStatus and verified flag.
func (b *Broker) verifyOrderFill(orderID string, req BuyRequest, submittedAt int64, filledSize float64, fillStatus string, verified bool) (float64, string, bool) {
	orderInfo, orderErr := retryGetOrder(b.clob, orderID, 3)
	if orderErr != nil {
		slog.Warn("Could not verify order fill via CLOB", "orderID", orderID, "error", orderErr)
	} else if orderInfo != nil {
		verified = true
		fillStatus = orderInfo.Status
		if orderInfo.SizeFilled > 0 && orderInfo.Price > 0 {
			filledSize = orderInfo.SizeFilled * orderInfo.Price
		}
		slog.Info("Order status", "orderID", orderID, "status", fillStatus, "filledUSDC", filledSize)
	}

	if !verified && !b.paper && b.tradingWallet != "" {
		filledSize, fillStatus, verified = b.confirmFillViaDataAPI(orderID, req.TokenID, submittedAt, filledSize)
	}
	return filledSize, fillStatus, verified
}

// confirmFillViaDataAPI is the fallback verification path: the authenticated
// /order/{id} endpoint 404s for API keys not bound via POLY_1271 (observed in
// production). The public data-api /trades endpoint needs no auth and reflects
// matched fills within seconds, so use it to confirm execution against the
// portal.
func (b *Broker) confirmFillViaDataAPI(orderID, tokenID string, submittedAt int64, filledSize float64) (float64, string, bool) {
	for attempt := 0; attempt < 4; attempt++ {
		time.Sleep(2 * time.Second)
		fill, ferr := b.dataAPI.FindRecentFill(b.tradingWallet, tokenID, submittedAt)
		if ferr != nil {
			slog.Warn("data-api fill check failed", "error", ferr)
			break
		}
		if fill != nil && fill.Size > 0 {
			filledSize = fill.Size * fill.Price
			slog.Info("Fill confirmed via data-api", "orderID", orderID, "filledUSDC", filledSize, "avgPrice", fill.Price)
			return filledSize, "MATCHED", true
		}
	}
	return filledSize, "", false
}

// cancelUnverified best-effort cancels an order we could not confirm so a
// resting GTC order does not keep collateral locked and fill later outside
// our books. logResult distinguishes the unverified path (full logging) from
// the not-filled path (failure-only logging).
func (b *Broker) cancelUnverified(orderID string, logResult bool) {
	if orderID == "" {
		return
	}
	if _, cerr := b.clob.CancelOrder(orderID); cerr != nil {
		slog.Warn("cancel of unverified order failed", "orderID", orderID, "error", cerr)
		return
	}
	if logResult {
		slog.Info("cancelled unverified resting order", "orderID", orderID)
	}
}

// checkLiveFill enforces P2.4: never silently assume a fill in live mode. If
// we could not verify the order, or it is only resting on the book (LIVE),
// treat it as a non-fill and surface an error instead of recording a
// phantom trade / position.
func (b *Broker) checkLiveFill(orderID, fillStatus string, verified bool) error {
	if !verified {
		slog.Warn("Live order fill unverified, refusing to record", "orderID", orderID, "status", fillStatus)
		// Best-effort cancel so an unconfirmed resting order does not
		// keep collateral locked and fill later outside our books.
		b.cancelUnverified(orderID, true)
		return fmt.Errorf("order fill unverified (orderID=%s)", orderID)
	}
	if fillStatus != "FILLED" && fillStatus != "filled" && fillStatus != "MATCHED" {
		slog.Warn("Live order not filled", "orderID", orderID, "status", fillStatus)
		b.cancelNotFilled(orderID)
		return fmt.Errorf("order not filled: %s (orderID=%s)", fillStatus, orderID)
	}
	return nil
}

// cancelNotFilled best-effort cancels an order that rested unfilled.
func (b *Broker) cancelNotFilled(orderID string) {
	if orderID == "" {
		return
	}
	if _, cerr := b.clob.CancelOrder(orderID); cerr != nil {
		slog.Warn("cancel of unfilled order failed", "orderID", orderID, "error", cerr)
	}
}

func (b *Broker) recordFilledBuy(req BuyRequest, buyPrice, filledSize float64, orderID string) {
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
}

// retryGetOrder polls GetOrder a few times: matched fills can take a couple
// of seconds to register on the CLOB order endpoint after placement.
func retryGetOrder(c clob.Client, orderID string, attempts int) (*clob.OrderStatus, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second)
		}
		info, err := c.GetOrder(orderID)
		if err == nil && info != nil {
			return info, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

type DeadTokenError struct {
	TokenID string
}

func (e *DeadTokenError) Error() string {
	return "dead orderbook for token " + e.TokenID[:min(12, len(e.TokenID))]
}
