package kelly

import "testing"

// TestOptimalSize_PredictionMarketPayoff locks in the P2.1 fix: a winning
// prediction-market share pays $1.00, so the payoff-to-loss ratio must be
// (1-price)/price, NOT 1 (the old degenerate even-money bug). A longshot
// with a genuine edge should size a bet; the buggy even-money math rejected
// it entirely.
func TestOptimalSize_PredictionMarketPayoff(t *testing.T) {
	const (
		price    = 0.30
		bankroll = 10000.0
		maxSize  = 100.0
		fraction = 0.5
	)
	// We believe the true win prob is higher than the price implies.
	winProb := 0.45

	correct := OptimalSize(EdgeEstimate{
		WinProb:    winProb,
		Payout:     1 - price, // 0.70 gain per share on win
		LossAmount: price,      // 0.30 loss per share on loss
	}, bankroll, maxSize, fraction)

	buggy := OptimalSize(EdgeEstimate{
		WinProb:    winProb,
		Payout:     price, // old code: b = price/price = 1
		LossAmount: price,
	}, bankroll, maxSize, fraction)

	if correct <= 0 {
		t.Fatalf("correct payoff math should size a positive-EV longshot, got %v", correct)
	}
	if buggy > 0 {
		t.Fatalf("even-money math should reject this bet, got %v", buggy)
	}
}

// TestOptimalSize_NegativeEdgeSkipped ensures a genuine negative-edge bet is
// still skipped under the corrected payoff math.
func TestOptimalSize_NegativeEdgeSkipped(t *testing.T) {
	const price = 0.70
	// Win prob below the implied price => negative edge.
	size := OptimalSize(EdgeEstimate{
		WinProb:    0.40,
		Payout:     1 - price,
		LossAmount: price,
	}, 10000.0, 100.0, 0.5)
	if size != 0 {
		t.Fatalf("negative-edge bet should be skipped, got %v", size)
	}
}
