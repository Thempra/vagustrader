package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const schema = `
CREATE TABLE IF NOT EXISTS market_meta (condition_id TEXT PRIMARY KEY, question TEXT NOT NULL, slug TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS trades (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, market_id TEXT NOT NULL, token_id TEXT NOT NULL, side TEXT NOT NULL, price REAL NOT NULL, size_usdc REAL NOT NULL, order_id TEXT, paper INTEGER NOT NULL DEFAULT 0, result REAL);
CREATE TABLE IF NOT EXISTS positions (token_id TEXT PRIMARY KEY, market_id TEXT NOT NULL, outcome TEXT NOT NULL, size REAL NOT NULL, avg_price REAL NOT NULL, updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS pnl_snapshots (date TEXT PRIMARY KEY, realized REAL NOT NULL, unrealized REAL NOT NULL, total_trades INTEGER NOT NULL);
`

type DB struct {
	*sql.DB
}

type Trade struct {
	ID         int64
	Ts         int64
	MarketID   string
	TokenID    string
	Side       string
	Outcome    string
	Price      float64
	SizeUSDC   float64
	OrderID    string
	Paper      bool
	Result     sql.NullFloat64
	SourceJSON string
}

type Position struct {
	TokenID   string
	MarketID  string
	Outcome   string
	Size      float64
	AvgPrice  float64
	UpdatedTs int64
}

type MarketMeta struct {
	ConditionID string
	Question    string
	Slug        string
}

type PNLSnapshot struct {
	Date        string
	Realized    float64
	Unrealized  float64
	TotalTrades int
}

func Init(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	ldb, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if _, err := ldb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		ldb.Close()
		return nil, fmt.Errorf("set wal mode: %w", err)
	}

	if _, err := ldb.Exec(schema); err != nil {
		ldb.Close()
		return nil, fmt.Errorf("run schema: %w", err)
	}

	ldb.Exec("ALTER TABLE trades ADD COLUMN result REAL")
	ldb.Exec("ALTER TABLE trades ADD COLUMN source_json TEXT")
	ldb.Exec("ALTER TABLE trades ADD COLUMN outcome TEXT")

	return &DB{ldb}, nil
}

func (db *DB) RecordTrade(t *Trade) (int64, error) {
	paper := 0
	if t.Paper {
		paper = 1
	}
	res, err := db.Exec(
		"INSERT INTO trades (ts, market_id, token_id, side, outcome, price, size_usdc, order_id, paper, result, source_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		t.Ts, t.MarketID, t.TokenID, t.Side, t.Outcome, t.Price, t.SizeUSDC, t.OrderID, paper, t.Result, t.SourceJSON,
	)
	if err != nil {
		return 0, fmt.Errorf("insert trade: %w", err)
	}
	return res.LastInsertId()
}

func (db *DB) UpsertPosition(p *Position) error {
	var existingSize, existingAvg float64
	err := db.QueryRow("SELECT size, avg_price FROM positions WHERE token_id = ?", p.TokenID).Scan(&existingSize, &existingAvg)
	if err == sql.ErrNoRows {
		_, err = db.Exec(
			"INSERT INTO positions (token_id, market_id, outcome, size, avg_price, updated_ts) VALUES (?, ?, ?, ?, ?, ?)",
			p.TokenID, p.MarketID, p.Outcome, p.Size, p.AvgPrice, p.UpdatedTs,
		)
	} else if err == nil {
		totalCost := existingSize*existingAvg + p.Size*p.AvgPrice
		totalSize := existingSize + p.Size
		newAvg := totalCost / totalSize
		_, err = db.Exec(
			"UPDATE positions SET market_id=?, outcome=?, size=?, avg_price=?, updated_ts=? WHERE token_id=?",
			p.MarketID, p.Outcome, totalSize, newAvg, p.UpdatedTs, p.TokenID,
		)
	}
	if err != nil {
		return fmt.Errorf("upsert position: %w", err)
	}
	return nil
}

func (db *DB) SettleTrades(tokenID string, finalPrice float64) error {
	_, err := db.Exec(
		"UPDATE trades SET result = ? WHERE token_id = ? AND result IS NULL",
		finalPrice, tokenID,
	)
	if err != nil {
		return fmt.Errorf("settle trades: %w", err)
	}
	return nil
}

func (db *DB) DeletePosition(tokenID string) error {
	_, err := db.Exec("DELETE FROM positions WHERE token_id = ?", tokenID)
	if err != nil {
		return fmt.Errorf("delete position: %w", err)
	}
	return nil
}

func (db *DB) GetPositions() ([]Position, error) {
	rows, err := db.Query("SELECT token_id, market_id, outcome, size, avg_price, updated_ts FROM positions ORDER BY updated_ts DESC")
	if err != nil {
		return nil, fmt.Errorf("query positions: %w", err)
	}
	defer rows.Close()

	var positions []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.TokenID, &p.MarketID, &p.Outcome, &p.Size, &p.AvgPrice, &p.UpdatedTs); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func (db *DB) GetTodayTrades(dateStr string) ([]Trade, error) {
	var rows *sql.Rows
	var err error
	if dateStr == "2000-01-01" {
		rows, err = db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, '') FROM trades ORDER BY ts DESC")
	} else {
		rows, err = db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, '') FROM trades WHERE date(ts, 'unixepoch') = ? ORDER BY ts DESC", dateStr)
	}
	if err != nil {
		return nil, fmt.Errorf("query trades: %w", err)
	}
	defer rows.Close()

	return scanTrades(rows)
}

func (db *DB) GetRecentTrades(limit int) ([]Trade, error) {
	rows, err := db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, '') FROM trades ORDER BY ts DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("query recent trades: %w", err)
	}
	defer rows.Close()

	return scanTrades(rows)
}

func scanTrades(rows *sql.Rows) ([]Trade, error) {
	var trades []Trade
	for rows.Next() {
		var t Trade
		var paper int
		if err := rows.Scan(&t.ID, &t.Ts, &t.MarketID, &t.TokenID, &t.Side, &t.Outcome, &t.Price, &t.SizeUSDC, &t.OrderID, &paper, &t.Result, &t.SourceJSON); err != nil {
			return nil, fmt.Errorf("scan trade: %w", err)
		}
		t.Paper = paper == 1
		trades = append(trades, t)
	}
	return trades, rows.Err()
}

func (db *DB) UpsertMarketMeta(m *MarketMeta) error {
	_, err := db.Exec(
		"INSERT INTO market_meta (condition_id, question, slug) VALUES (?, ?, ?) ON CONFLICT(condition_id) DO UPDATE SET question=excluded.question, slug=excluded.slug",
		m.ConditionID, m.Question, m.Slug,
	)
	if err != nil {
		return fmt.Errorf("upsert market meta: %w", err)
	}
	return nil
}

func (db *DB) GetMarketMeta(conditionID string) (string, string, error) {
	var question, slug string
	err := db.QueryRow("SELECT question, slug FROM market_meta WHERE condition_id = ?", conditionID).Scan(&question, &slug)
	if err == sql.ErrNoRows {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("query market meta: %w", err)
	}
	return question, slug, nil
}

func (db *DB) SavePNLSnapshot(s *PNLSnapshot) error {
	_, err := db.Exec(
		"INSERT INTO pnl_snapshots (date, realized, unrealized, total_trades) VALUES (?, ?, ?, ?) ON CONFLICT(date) DO UPDATE SET realized=excluded.realized, unrealized=excluded.unrealized, total_trades=excluded.total_trades",
		s.Date, s.Realized, s.Unrealized, s.TotalTrades,
	)
	if err != nil {
		return fmt.Errorf("save pnl snapshot: %w", err)
	}
	return nil
}

func (db *DB) GetPNLSnapshots(limit int) ([]PNLSnapshot, error) {
	rows, err := db.Query("SELECT date, realized, unrealized, total_trades FROM pnl_snapshots ORDER BY date DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("query pnl snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []PNLSnapshot
	for rows.Next() {
		var s PNLSnapshot
		if err := rows.Scan(&s.Date, &s.Realized, &s.Unrealized, &s.TotalTrades); err != nil {
			return nil, fmt.Errorf("scan pnl snapshot: %w", err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

func (db *DB) GetRealizedPnL() (float64, error) {
	var total sql.NullFloat64
	err := db.QueryRow("SELECT SUM(realized) FROM pnl_snapshots").Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("query realized pnl: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}
