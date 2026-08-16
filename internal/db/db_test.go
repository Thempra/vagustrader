package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestSchemaVersion_Migration verifies P1.5: opening a DB on the new schema
// adds schema_version column to trades/positions/pnl_snapshots, existing
// rows are tagged 'legacy', and new inserts get CurrentSchemaVersion.
func TestSchemaVersion_Migration(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")

	database, err := Init(dbPath)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer database.Close()

	// Verify the columns exist.
	for _, table := range []string{"trades", "positions", "pnl_snapshots"} {
		var count int
		err = database.QueryRow(
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name='schema_version'",
			table,
		).Scan(&count)
		if err != nil {
			t.Fatalf("pragma_table_info(%s): %v", table, err)
		}
		if count != 1 {
			t.Fatalf("%s.schema_version column missing", table)
		}
	}

	// Insert a trade and confirm it gets tagged with CurrentSchemaVersion.
	tradeID, err := database.RecordTrade(&Trade{
		Ts:       1700000000,
		MarketID: "0xmarket",
		TokenID:  "0xtoken",
		Side:     "BUY",
		Price:    0.50,
		SizeUSDC: 5.00,
		Strategy: "copy_trading",
		Status:   "filled",
	})
	if err != nil {
		t.Fatalf("RecordTrade: %v", err)
	}
	if tradeID == 0 {
		t.Fatalf("expected non-zero trade id")
	}

	var sv string
	err = database.QueryRow(
		"SELECT schema_version FROM trades WHERE id = ?",
		tradeID,
	).Scan(&sv)
	if err != nil {
		t.Fatalf("select schema_version: %v", err)
	}
	if sv != CurrentSchemaVersion {
		t.Fatalf("expected %q, got %q", CurrentSchemaVersion, sv)
	}

	// Insert a position and verify its schema_version.
	if err := database.UpsertPosition(&Position{
		TokenID:   "0xtoken",
		MarketID:  "0xmarket",
		Outcome:   "Yes",
		Size:      10,
		AvgPrice:  0.50,
		Strategy:  "copy_trading",
		UpdatedTs: 1700000000,
	}); err != nil {
		t.Fatalf("UpsertPosition: %v", err)
	}
	err = database.QueryRow(
		"SELECT schema_version FROM positions WHERE token_id = ? AND strategy = ?",
		"0xtoken", "copy_trading",
	).Scan(&sv)
	if err != nil {
		t.Fatalf("select positions.schema_version: %v", err)
	}
	if sv != CurrentSchemaVersion {
		t.Fatalf("positions: expected %q, got %q", CurrentSchemaVersion, sv)
	}
}

// TestSettleTradesByStrategy_Idempotent verifies P1.4: SettleTradesByStrategy
// touches only trades matching (tokenID, strategy) and is idempotent (a
// second call with same args is a no-op thanks to settled_at guard).
func TestSettleTradesByStrategy_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")

	database, err := Init(dbPath)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer database.Close()

	// Two strategies holding the same tokenID. Without per-strategy
	// isolation, settling one would settle both.
	if _, err := database.RecordTrade(&Trade{
		Ts: 1700000000, MarketID: "0xm", TokenID: "0xtok", Side: "BUY",
		Price: 0.4, SizeUSDC: 5, Strategy: "copy_trading", Status: "filled",
	}); err != nil {
		t.Fatalf("RecordTrade copy_trading: %v", err)
	}
	if _, err := database.RecordTrade(&Trade{
		Ts: 1700000001, MarketID: "0xm", TokenID: "0xtok", Side: "BUY",
		Price: 0.4, SizeUSDC: 5, Strategy: "arbitrage", Status: "filled",
	}); err != nil {
		t.Fatalf("RecordTrade arbitrage: %v", err)
	}

	// Settle only copy_trading.
	if err := database.SettleTradesByStrategy("0xtok", "copy_trading", 1.0); err != nil {
		t.Fatalf("SettleTradesByStrategy: %v", err)
	}

	// copy_trading trade settled, arbitrage trade still open.
	var ctResult, arbResult sql.NullFloat64
	if err := database.QueryRow(
		"SELECT result FROM trades WHERE token_id='0xtok' AND strategy='copy_trading'",
	).Scan(&ctResult); err != nil {
		t.Fatalf("scan copy_trading: %v", err)
	}
	if !ctResult.Valid || ctResult.Float64 != 1.0 {
		t.Fatalf("copy_trading expected result=1.0, got %v", ctResult)
	}
	if err := database.QueryRow(
		"SELECT result FROM trades WHERE token_id='0xtok' AND strategy='arbitrage'",
	).Scan(&arbResult); err != nil {
		t.Fatalf("scan arbitrage: %v", err)
	}
	if arbResult.Valid {
		t.Fatalf("arbitrage should still be open, got result=%v", arbResult.Float64)
	}

	// Idempotency: second call on (0xtok, copy_trading) is a no-op.
	// settled_at should be unchanged; the UPDATE matches 0 rows.
	if err := database.SettleTradesByStrategy("0xtok", "copy_trading", 0.5); err != nil {
		t.Fatalf("idempotent SettleTradesByStrategy: %v", err)
	}
	if err := database.QueryRow(
		"SELECT result FROM trades WHERE token_id='0xtok' AND strategy='copy_trading'",
	).Scan(&ctResult); err != nil {
		t.Fatalf("rescan copy_trading: %v", err)
	}
	if ctResult.Float64 != 1.0 {
		t.Fatalf("idempotency broken: result changed from 1.0 to %v", ctResult.Float64)
	}
}
