package kelly

import "math"

type EdgeEstimate struct {
	WinProb    float64
	Payout     float64
	LossAmount float64
}

func OptimalSize(edge EdgeEstimate, bankroll, maxSize, kellyFraction float64) float64 {
	if edge.WinProb <= 0 || edge.WinProb >= 1 || edge.Payout <= 0 || bankroll <= 0 {
		return 0
	}

	b := edge.Payout / edge.LossAmount
	p := edge.WinProb
	q := 1 - p

	f := (b*p - q) / b

	if f <= 0 {
		return 0
	}

	sized := f * bankroll * kellyFraction

	sized = math.Min(sized, maxSize)
	sized = math.Max(sized, 0)

	if sized < 2.0 {
		return 0
	}

	return math.Round(sized*100) / 100
}

func EstimateWinProb(price float64, sport string, outcomeType string, consensusCount int) float64 {
	base := 0.50

	if price >= 0.30 && price <= 0.50 {
		base += 0.08
	} else if price >= 0.50 && price <= 0.65 {
		base += 0.10
	} else if price >= 0.65 && price <= 0.80 {
		base += 0.15
	} else if price < 0.20 {
		base -= 0.25
	} else if price < 0.30 {
		base -= 0.10
	}

	switch sport {
	case "nor", "fif", "atp", "itf":
		base += 0.04
	case "es2", "cs2", "lol":
		base += 0.08
	case "mlb":
		base -= 0.06
	case "nba", "nhl":
		base -= 0.03
	}

	if outcomeType == "under" {
		base += 0.05
	} else if outcomeType == "over" {
		base -= 0.05
	}

	if consensusCount >= 3 {
		base += 0.10
	} else if consensusCount >= 2 {
		base += 0.06
	}

	base = math.Max(0.05, math.Min(0.95, base))

	return math.Round(base*1000) / 1000
}
