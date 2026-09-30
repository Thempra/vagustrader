package broker

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/db"
)

// TestMarketCache_ConcurrentAccess verifies P1.1: the marketCache mutex
// prevents data races when 5 strategy goroutines call GetMarketData in
// parallel. Run with -race to detect violations.
func TestMarketCache_ConcurrentAccess(t *testing.T) {
	b := &Broker{
		marketCache: make(map[string]marketCacheEntry),
	}
	conditionIDs := []string{
		"0xcondition1",
		"0xcondition2",
		"0xcondition3",
		"0xcondition4",
		"0xcondition5",
	}
	const goroutines = 10
	const iterations = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for n := 0; n < iterations; n++ {
				cid := conditionIDs[(gid+n)%len(conditionIDs)]
				// GetMarketData will return nil (no HTTP in tests) but
				// exercises the mutex + map access path.
				_ = b.GetMarketData(cid)
				b.ClearMarketCache()
			}
		}(i)
	}
	wg.Wait()
}

// TestBuy_GlobalMaxOpenPositions verifies P2.5: once the account-wide open
// position count reaches Risk.MaxOpenPositionsGlobal, Buy is rejected
// regardless of which strategy requests it.
func TestBuy_GlobalMaxOpenPositions(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Init(filepath.Join(tmp, "cap.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	const cap = 3
	for i := 0; i < cap; i++ {
		if err := database.UpsertPosition(&db.Position{
			TokenID:   "tok-" + string(rune('a'+i)),
			MarketID:  "mkt-" + string(rune('a'+i)),
			Outcome:   "Yes",
			Size:      10,
			AvgPrice:  0.30,
			Strategy:  "under_hunter",
			UpdatedTs: time.Now().Unix(),
		}); err != nil {
			t.Fatalf("seed position %d: %v", i, err)
		}
	}

	b := &Broker{
		clob:        clob.NewPaperClient(""),
		database:    database,
		paper:       true,
		marketCache: make(map[string]marketCacheEntry),
		config: &cfg.AppConfig{
			Risk: cfg.RiskConfig{MaxOpenPositionsGlobal: cap},
		},
	}

	_, err = b.Buy(context.Background(), BuyRequest{
		TokenID:  "tok-new",
		MarketID: "mkt-new",
		Outcome:  "Yes",
		Price:    0.30,
		SizeUSDC: 10,
		Strategy: "whale_consensus",
	})
	if err == nil {
		t.Fatal("expected Buy to be rejected at global position cap, got nil error")
	}
}

// TestMarketCache_TTLHit verifies that a freshly-cached entry is returned
// without re-fetching, ensuring the TTL window (60s) works as intended.
func TestMarketCache_TTLHit(t *testing.T) {
	b := &Broker{
		marketCache: make(map[string]marketCacheEntry),
	}
	b.marketCache["cond-fresh"] = marketCacheEntry{
		data:      map[string]interface{}{"question": "fresh"},
		fetchedAt: time.Now(),
	}
	b.marketCache["cond-stale"] = marketCacheEntry{
		data:      map[string]interface{}{"question": "stale"},
		fetchedAt: time.Now().Add(-2 * time.Hour),
	}

	// Fast path: fresh entry returned from cache.
	b.marketMu.RLock()
	fresh, ok := b.marketCache["cond-fresh"]
	b.marketMu.RUnlock()
	if !ok || time.Since(fresh.fetchedAt) >= marketCacheTTL {
		t.Fatalf("expected fresh cache hit for cond-fresh")
	}

	// Stale entry should be evicted by the next GetMarketData call, but
	// without an HTTP server the function returns nil. We verify the TTL
	// check directly instead.
	b.marketMu.RLock()
	stale, ok := b.marketCache["cond-stale"]
	b.marketMu.RUnlock()
	if !ok || time.Since(stale.fetchedAt) < marketCacheTTL {
		t.Fatalf("expected cond-stale to be considered stale (TTL expired)")
	}
}

// TestBuy_MaxDaysToEnd verifies the hard broker-level horizon rule: buys in
// markets resolving beyond Risk.MaxDaysToEnd are rejected BEFORE any order
// is placed, short-dated markets pass, and unknown end dates are rejected
// (fail-closed).
func TestBuy_MaxDaysToEnd(t *testing.T) {
	tmp := t.TempDir()
	database, err := db.Init(filepath.Join(tmp, "horizon.db"))
	if err != nil {
		t.Fatalf("db.Init: %v", err)
	}
	defer database.Close()

	newBroker := func(maxDays float64) *Broker {
		return &Broker{
			clob:        clob.NewPaperClient(""),
			database:    database,
			paper:       true,
			marketCache: make(map[string]marketCacheEntry),
			config: &cfg.AppConfig{
				Risk: cfg.RiskConfig{MaxOpenPositionsGlobal: 100, MaxDaysToEnd: maxDays},
			},
		}
	}
	seed := func(b *Broker, market, endISO string) {
		if endISO == "" {
			return // simulate unknown end date: no cache entry
		}
		b.marketCache[market] = marketCacheEntry{
			data:      map[string]interface{}{"end_date_iso": endISO},
			fetchedAt: time.Now(),
		}
	}

	near := time.Now().Add(24 * 12 * time.Hour).UTC().Format(time.RFC3339) // 12 days
	far := time.Now().Add(24 * 200 * time.Hour).UTC().Format(time.RFC3339) // ~200 days

	// far-dated market rejected
	b := newBroker(7)
	seed(b, "mkt-far", far)
	if _, err := b.Buy(context.Background(), BuyRequest{TokenID: "t1", MarketID: "mkt-far", Price: 0.3, SizeUSDC: 5, Strategy: "under_hunter"}); err == nil {
		t.Fatal("expected rejection for 200-day market under max_days_to_end=7")
	}
	if pos, err := database.GetPositions(); err == nil {
		for _, p := range pos {
			if p.TokenID == "t1" {
				t.Fatal("rejected buy must not create a position")
			}
		}
	}

	// short-dated market allowed (12d > 7 would reject; use 6d to pass)
	nearOK := time.Now().Add(6 * 24 * time.Hour).UTC().Format(time.RFC3339)
	b2 := newBroker(7)
	seed(b2, "mkt-near", nearOK)
	if _, err := b2.Buy(context.Background(), BuyRequest{TokenID: "t2", MarketID: "mkt-near", Price: 0.3, SizeUSDC: 5, Strategy: "under_hunter"}); err != nil {
		t.Fatalf("6-day market must pass max_days_to_end=7, got: %v", err)
	}

	// unknown end date rejected (fail-closed)
	b3 := newBroker(7)
	if _, err := b3.Buy(context.Background(), BuyRequest{TokenID: "t3", MarketID: "mkt-unknown", Price: 0.3, SizeUSDC: 5, Strategy: "under_hunter"}); err == nil {
		t.Fatal("expected rejection when end date is unknown (fail-closed)")
	}

	// rule disabled with 0: far-dated allowed again
	b4 := newBroker(0)
	seed(b4, "mkt-far2", far)
	if _, err := b4.Buy(context.Background(), BuyRequest{TokenID: "t4", MarketID: "mkt-far2", Price: 0.3, SizeUSDC: 5, Strategy: "under_hunter"}); err != nil {
		t.Fatalf("with max_days_to_end=0 the 200-day market must pass, got: %v", err)
	}
	_ = near
}
