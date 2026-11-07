package broker

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestFetchClobMarketUsesMinimumTickSize locks the field the CLOB actually
// returns. The old code read a "tick_size" string that never exists, so every
// market fell back to 0.01 and 0.001-tick markets were mispriced.
func TestFetchClobMarketUsesMinimumTickSize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{
			"condition_id":"0xabc",
			"question":"Will it rain?",
			"market_slug":"will-it-rain",
			"end_date_iso":"2026-11-10T00:00:00Z",
			"active":true,"closed":false,
			"minimum_tick_size":0.001,
			"neg_risk":false,
			"tokens":[{"token_id":"111","outcome":"Yes","price":0.4}]
		}`)
	}))
	defer srv.Close()

	old := clobREST
	clobREST = srv.URL
	defer func() { clobREST = old }()

	md := fetchClobMarket("0xabc")
	if md == nil {
		t.Fatal("fetchClobMarket returned nil")
	}
	if got := md["tick_size"]; got != "0.001" {
		t.Fatalf("tick_size = %v, want 0.001", got)
	}
	if got := md["slug"]; got != "will-it-rain" {
		t.Fatalf("slug = %v, want will-it-rain", got)
	}
	if got := md["end_date_iso"]; got != "2026-11-10T00:00:00Z" {
		t.Fatalf("end_date_iso = %v", got)
	}
}

// TestFetchClobMarketRetriesTransientFailure covers the cold-start burst:
// the first call is rate limited, the retry must succeed so the market is
// not rejected as "end date unknown".
func TestFetchClobMarketRetriesTransientFailure(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"condition_id":"0xdef","end_date_iso":"2026-11-09T12:00:00Z","minimum_tick_size":0.01}`)
	}))
	defer srv.Close()

	old := clobREST
	clobREST = srv.URL
	defer func() { clobREST = old }()

	md := fetchClobMarket("0xdef")
	if md == nil {
		t.Fatal("fetchClobMarket returned nil after a retry")
	}
	if got := md["end_date_iso"]; got != "2026-11-09T12:00:00Z" {
		t.Fatalf("end_date_iso = %v", got)
	}
	if atomic.LoadInt32(&hits) < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", hits)
	}
}

// TestFetchClobMarketFallsBackToGamma covers markets the CLOB cannot serve:
// gamma still publishes endDate, and without it the max_days_to_end rule
// rejects every such market as unbounded.
func TestFetchClobMarketFallsBackToGamma(t *testing.T) {
	clobSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer clobSrv.Close()

	gammaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "condition_ids=0xfeed") {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[{"question":"Q","slug":"q","endDate":"2026-11-12T00:00:00Z","active":true,"closed":false}]`)
	}))
	defer gammaSrv.Close()

	oldClob, oldGamma := clobREST, gammaREST
	clobREST, gammaREST = clobSrv.URL, gammaSrv.URL
	defer func() { clobREST, gammaREST = oldClob, oldGamma }()

	md := fetchClobMarket("0xfeed")
	if md == nil {
		t.Fatal("expected gamma fallback to produce market data")
	}
	if got := md["end_date_iso"]; got != "2026-11-12T00:00:00Z" {
		t.Fatalf("end_date_iso = %v, want gamma endDate", got)
	}
}
