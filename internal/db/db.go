package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// CurrentSchemaVersion tags every new row so post-fix data can be filtered
// out of the legacy baseline. Bump this on each stabilization phase:
//   "v1" = post P1 deploy
//   "v2" = post P2 deploy (kelly/sizing/cache/fill/maxglobal)
//   "v3" = post P3 deploy (mark-to-market/wallet/stats/chart)
//   "v4" = post diagnostics deploy (strategy tick instrumentation)
//   "v5" = new strategies (resolution_sniper + momentum_rider)
const CurrentSchemaVersion = "v5"

const schema = `
CREATE TABLE IF NOT EXISTS market_meta (condition_id TEXT PRIMARY KEY, question TEXT NOT NULL, slug TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS trades (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, market_id TEXT NOT NULL, token_id TEXT NOT NULL, side TEXT NOT NULL, price REAL NOT NULL, size_usdc REAL NOT NULL, order_id TEXT, paper INTEGER NOT NULL DEFAULT 0, result REAL);
CREATE TABLE IF NOT EXISTS positions (token_id TEXT PRIMARY KEY, market_id TEXT NOT NULL, outcome TEXT NOT NULL, size REAL NOT NULL, avg_price REAL NOT NULL, updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS pnl_snapshots (date TEXT PRIMARY KEY, realized REAL NOT NULL, unrealized REAL NOT NULL, total_trades INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS paper_wallets (strategy TEXT PRIMARY KEY, initial_balance REAL NOT NULL DEFAULT 10000.0, spent REAL NOT NULL DEFAULT 0.0, recovered REAL NOT NULL DEFAULT 0.0, updated_ts INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS strategy_diagnostics (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, strategy TEXT NOT NULL, scanned INTEGER NOT NULL DEFAULT 0, skipped_not_ou INTEGER NOT NULL DEFAULT 0, skipped_price INTEGER NOT NULL DEFAULT 0, skipped_sport INTEGER NOT NULL DEFAULT 0, skipped_held INTEGER NOT NULL DEFAULT 0, skipped_dead INTEGER NOT NULL DEFAULT 0, opened INTEGER NOT NULL DEFAULT 0, errors INTEGER NOT NULL DEFAULT 0, note TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS idx_diag_strategy_ts ON strategy_diagnostics(strategy, ts);
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
	Status     string
	Strategy   string
	SchemaVersion string
}

type Position struct {
	TokenID   string
	MarketID  string
	Outcome   string
	Size      float64
	AvgPrice  float64
	Strategy  string
	UpdatedTs int64
	SchemaVersion string
}

type MarketMeta struct {
	ConditionID string
	Question    string
	Slug        string
}

type PNLSnapshot struct {
	Date        string
	Strategy    string
	Realized    float64
	Unrealized  float64
	TotalTrades int
	SchemaVersion string
}

type PaperWallet struct {
	Strategy       string
	InitialBalance float64
	Spent          float64
	Recovered      float64
	Balance        float64
	UpdatedTs      int64
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
	ldb.Exec("ALTER TABLE trades ADD COLUMN status TEXT")
	ldb.Exec("ALTER TABLE trades ADD COLUMN strategy TEXT")
	// P1.5: schema_version tagging for post-fix data isolation.
	ldb.Exec("ALTER TABLE trades ADD COLUMN schema_version TEXT NOT NULL DEFAULT 'legacy'")
	// P3.5: settled_at for settlement idempotency guard.
	ldb.Exec("ALTER TABLE trades ADD COLUMN settled_at INTEGER")
	ldb.Exec("UPDATE trades SET status = 'filled' WHERE result IS NOT NULL AND status IS NULL")
	ldb.Exec("UPDATE trades SET status = 'placed' WHERE result IS NULL AND status IS NULL")

	migratePositionsForStrategy(ldb)
	migratePNLSnapshotsForStrategy(ldb)
	migrateSchemaVersion(ldb)
	ldb.Exec("CREATE INDEX IF NOT EXISTS idx_trades_strategy ON trades(strategy)")
	ldb.Exec("CREATE INDEX IF NOT EXISTS idx_positions_strategy ON positions(strategy)")
	ldb.Exec("CREATE INDEX IF NOT EXISTS idx_trades_schema_version ON trades(schema_version)")
	// Dashboard perf: the positions query runs a correlated subquery
	// (latest source_json per token_id ORDER BY ts DESC LIMIT 1) per open
	// position. Without this index SQLite SCANs all ~36k trades for every
	// position (2131 open contrarian positions => minutes of CPU per
	// refresh). This composite index turns each probe into a seek.
	ldb.Exec("CREATE INDEX IF NOT EXISTS idx_trades_token_ts ON trades(token_id, ts)")

	return &DB{ldb}, nil
}

// migrateSchemaVersion adds the schema_version column to positions and
// pnl_snapshots (trades is handled inline above because it predates the
// pragma_table_info guard pattern).
func migrateSchemaVersion(ldb *sql.DB) {
	var count int
	ldb.QueryRow("SELECT COUNT(*) FROM pragma_table_info('positions') WHERE name='schema_version'").Scan(&count)
	if count == 0 {
		ldb.Exec("ALTER TABLE positions ADD COLUMN schema_version TEXT NOT NULL DEFAULT 'legacy'")
	}
	ldb.QueryRow("SELECT COUNT(*) FROM pragma_table_info('pnl_snapshots') WHERE name='schema_version'").Scan(&count)
	if count == 0 {
		ldb.Exec("ALTER TABLE pnl_snapshots ADD COLUMN schema_version TEXT NOT NULL DEFAULT 'legacy'")
	}
}

func migratePositionsForStrategy(ldb *sql.DB) {
	var count int
	ldb.QueryRow("SELECT COUNT(*) FROM pragma_table_info('positions') WHERE name='strategy'").Scan(&count)
	if count > 0 {
		return
	}

	ldb.Exec(`CREATE TABLE positions_new (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token_id TEXT NOT NULL,
		market_id TEXT NOT NULL,
		outcome TEXT NOT NULL,
		size REAL NOT NULL,
		avg_price REAL NOT NULL,
		strategy TEXT NOT NULL DEFAULT 'copy_trading',
		updated_ts INTEGER NOT NULL,
		UNIQUE(token_id, strategy)
	)`)
	ldb.Exec(`INSERT INTO positions_new (token_id, market_id, outcome, size, avg_price, strategy, updated_ts)
		SELECT token_id, market_id, outcome, size, avg_price, 'copy_trading', updated_ts FROM positions`)
	ldb.Exec(`DROP TABLE positions`)
	ldb.Exec(`ALTER TABLE positions_new RENAME TO positions`)
}

func migratePNLSnapshotsForStrategy(ldb *sql.DB) {
	var count int
	ldb.QueryRow("SELECT COUNT(*) FROM pragma_table_info('pnl_snapshots') WHERE name='strategy'").Scan(&count)
	if count > 0 {
		return
	}

	ldb.Exec(`CREATE TABLE pnl_snapshots_new (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		date TEXT NOT NULL,
		strategy TEXT NOT NULL DEFAULT 'copy_trading',
		realized REAL NOT NULL,
		unrealized REAL NOT NULL,
		total_trades INTEGER NOT NULL,
		UNIQUE(date, strategy)
	)`)
	ldb.Exec(`INSERT INTO pnl_snapshots_new (date, strategy, realized, unrealized, total_trades)
		SELECT date, 'copy_trading', realized, unrealized, total_trades FROM pnl_snapshots`)
	ldb.Exec(`DROP TABLE pnl_snapshots`)
	ldb.Exec(`ALTER TABLE pnl_snapshots_new RENAME TO pnl_snapshots`)
}

func (db *DB) RecordTrade(t *Trade) (int64, error) {
	paper := 0
	if t.Paper {
		paper = 1
	}
	sv := t.SchemaVersion
	if sv == "" {
		sv = CurrentSchemaVersion
	}
	res, err := db.Exec(
		"INSERT INTO trades (ts, market_id, token_id, side, outcome, price, size_usdc, order_id, paper, result, source_json, status, strategy, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		t.Ts, t.MarketID, t.TokenID, t.Side, t.Outcome, t.Price, t.SizeUSDC, t.OrderID, paper, t.Result, t.SourceJSON, t.Status, t.Strategy, sv,
	)
	if err != nil {
		return 0, fmt.Errorf("insert trade: %w", err)
	}
	return res.LastInsertId()
}

func (db *DB) UpsertPosition(p *Position) error {
	strategy := p.Strategy
	if strategy == "" {
		strategy = "copy_trading"
	}
	sv := p.SchemaVersion
	if sv == "" {
		sv = CurrentSchemaVersion
	}
	var existingSize, existingAvg float64
	err := db.QueryRow("SELECT size, avg_price FROM positions WHERE token_id = ? AND strategy = ?", p.TokenID, strategy).Scan(&existingSize, &existingAvg)
	if err == sql.ErrNoRows {
		_, err = db.Exec(
			"INSERT INTO positions (token_id, market_id, outcome, size, avg_price, strategy, updated_ts, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			p.TokenID, p.MarketID, p.Outcome, p.Size, p.AvgPrice, strategy, p.UpdatedTs, sv,
		)
	} else if err == nil {
		totalCost := existingSize*existingAvg + p.Size*p.AvgPrice
		totalSize := existingSize + p.Size
		newAvg := totalCost / totalSize
		_, err = db.Exec(
			"UPDATE positions SET market_id=?, outcome=?, size=?, avg_price=?, updated_ts=?, schema_version=? WHERE token_id=? AND strategy=?",
			p.MarketID, p.Outcome, totalSize, newAvg, p.UpdatedTs, sv, p.TokenID, strategy,
		)
	}
	if err != nil {
		return fmt.Errorf("upsert position: %w", err)
	}
	return nil
}

func (db *DB) SettleTrades(tokenID string, finalPrice float64) error {
	_, err := db.Exec(
		"UPDATE trades SET result = ?, settled_at = ? WHERE token_id = ? AND result IS NULL AND settled_at IS NULL",
		finalPrice, time.Now().Unix(), tokenID,
	)
	if err != nil {
		return fmt.Errorf("settle trades: %w", err)
	}
	return nil
}

// SettleTradesByStrategy settles only the trades that belong to (tokenID, strategy).
// This is the per-strategy idempotent version required for multi-strategy isolation:
// the legacy SettleTrades affected every strategy's rows for the same token, which
// caused double-counting when two strategies held the same token.
func (db *DB) SettleTradesByStrategy(tokenID, strategy string, finalPrice float64) error {
	_, err := db.Exec(
		"UPDATE trades SET result = ?, settled_at = ? WHERE token_id = ? AND strategy = ? AND result IS NULL AND settled_at IS NULL",
		finalPrice, time.Now().Unix(), tokenID, strategy,
	)
	if err != nil {
		return fmt.Errorf("settle trades by strategy: %w", err)
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

func (db *DB) DeletePositionStrategy(tokenID, strategy string) error {
	_, err := db.Exec("DELETE FROM positions WHERE token_id = ? AND strategy = ?", tokenID, strategy)
	if err != nil {
		return fmt.Errorf("delete position strategy: %w", err)
	}
	return nil
}

func (db *DB) GetPositions() ([]Position, error) {
	rows, err := db.Query("SELECT token_id, market_id, outcome, size, avg_price, COALESCE(strategy,'copy_trading'), updated_ts FROM positions ORDER BY updated_ts DESC")
	if err != nil {
		return nil, fmt.Errorf("query positions: %w", err)
	}
	defer rows.Close()

	var positions []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.TokenID, &p.MarketID, &p.Outcome, &p.Size, &p.AvgPrice, &p.Strategy, &p.UpdatedTs); err != nil {
			return nil, fmt.Errorf("scan position: %w", err)
		}
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

func (db *DB) GetPositionsByStrategy(strategy string) ([]Position, error) {
	rows, err := db.Query("SELECT token_id, market_id, outcome, size, avg_price, strategy, updated_ts FROM positions WHERE strategy = ? ORDER BY updated_ts DESC", strategy)
	if err != nil {
		return nil, fmt.Errorf("query positions by strategy: %w", err)
	}
	defer rows.Close()

	var positions []Position
	for rows.Next() {
		var p Position
		if err := rows.Scan(&p.TokenID, &p.MarketID, &p.Outcome, &p.Size, &p.AvgPrice, &p.Strategy, &p.UpdatedTs); err != nil {
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
		rows, err = db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, ''), COALESCE(strategy, '') FROM trades ORDER BY ts DESC")
	} else {
		rows, err = db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, ''), COALESCE(strategy, '') FROM trades WHERE date(ts, 'unixepoch') = ? ORDER BY ts DESC", dateStr)
	}
	if err != nil {
		return nil, fmt.Errorf("query trades: %w", err)
	}
	defer rows.Close()

	return scanTrades(rows)
}

func (db *DB) GetRecentTrades(limit int) ([]Trade, error) {
	rows, err := db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, ''), COALESCE(strategy, '') FROM trades ORDER BY ts DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("query recent trades: %w", err)
	}
	defer rows.Close()

	return scanTrades(rows)
}

func (db *DB) GetTodayTradesByStrategy(strategy, dateStr string) ([]Trade, error) {
	var rows *sql.Rows
	var err error
	if dateStr == "2000-01-01" {
		rows, err = db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, ''), COALESCE(strategy, '') FROM trades WHERE strategy = ? ORDER BY ts DESC", strategy)
	} else {
		rows, err = db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, ''), COALESCE(strategy, '') FROM trades WHERE strategy = ? AND date(ts, 'unixepoch') = ? ORDER BY ts DESC", strategy, dateStr)
	}
	if err != nil {
		return nil, fmt.Errorf("query trades by strategy: %w", err)
	}
	defer rows.Close()

	return scanTrades(rows)
}

func (db *DB) GetRecentTradesByStrategy(strategy string, limit int) ([]Trade, error) {
	rows, err := db.Query("SELECT id, ts, market_id, token_id, side, COALESCE(outcome, ''), price, size_usdc, COALESCE(order_id, ''), paper, result, COALESCE(source_json, ''), COALESCE(strategy, '') FROM trades WHERE strategy = ? ORDER BY ts DESC LIMIT ?", strategy, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent trades by strategy: %w", err)
	}
	defer rows.Close()

	return scanTrades(rows)
}

func scanTrades(rows *sql.Rows) ([]Trade, error) {
	var trades []Trade
	for rows.Next() {
		var t Trade
		var paper int
		if err := rows.Scan(&t.ID, &t.Ts, &t.MarketID, &t.TokenID, &t.Side, &t.Outcome, &t.Price, &t.SizeUSDC, &t.OrderID, &paper, &t.Result, &t.SourceJSON, &t.Strategy); err != nil {
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
	strategy := s.Strategy
	if strategy == "" {
		strategy = "copy_trading"
	}
	sv := s.SchemaVersion
	if sv == "" {
		sv = CurrentSchemaVersion
	}
	_, err := db.Exec(
		"INSERT INTO pnl_snapshots (date, strategy, realized, unrealized, total_trades, schema_version) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(date, strategy) DO UPDATE SET realized=excluded.realized, unrealized=excluded.unrealized, total_trades=excluded.total_trades, schema_version=excluded.schema_version",
		s.Date, strategy, s.Realized, s.Unrealized, s.TotalTrades, sv,
	)
	if err != nil {
		return fmt.Errorf("save pnl snapshot: %w", err)
	}
	return nil
}

// Diagnostic captures per-tick scan stats for a strategy so that "dead"
// strategies (0 trades) can be diagnosed without container log access.
type Diagnostic struct {
	Strategy       string
	Scanned        int
	SkippedNotOU   int
	SkippedPrice   int
	SkippedSport   int
	SkippedHeld    int
	SkippedDead    int
	Opened         int
	Errors         int
	Note           string
}

func (db *DB) RecordDiagnostic(d *Diagnostic) error {
	_, err := db.Exec(
		"INSERT INTO strategy_diagnostics (ts, strategy, scanned, skipped_not_ou, skipped_price, skipped_sport, skipped_held, skipped_dead, opened, errors, note) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		time.Now().Unix(), d.Strategy, d.Scanned, d.SkippedNotOU, d.SkippedPrice, d.SkippedSport, d.SkippedHeld, d.SkippedDead, d.Opened, d.Errors, d.Note,
	)
	if err != nil {
		return fmt.Errorf("record diagnostic: %w", err)
	}
	return nil
}

func (db *DB) GetLatestDiagnostics(strategy string, limit int) ([]Diagnostic, error) {
	rows, err := db.Query(
		"SELECT ts, strategy, scanned, skipped_not_ou, skipped_price, skipped_sport, skipped_held, skipped_dead, opened, errors, note FROM strategy_diagnostics WHERE strategy=? ORDER BY ts DESC LIMIT ?",
		strategy, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query diagnostics: %w", err)
	}
	defer rows.Close()
	var out []Diagnostic
	for rows.Next() {
		var d Diagnostic
		var ts int64
		if err := rows.Scan(&ts, &d.Strategy, &d.Scanned, &d.SkippedNotOU, &d.SkippedPrice, &d.SkippedSport, &d.SkippedHeld, &d.SkippedDead, &d.Opened, &d.Errors, &d.Note); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func (db *DB) GetPNLSnapshots(limit int) ([]PNLSnapshot, error) {
	rows, err := db.Query("SELECT date, COALESCE(strategy,'copy_trading'), realized, unrealized, total_trades FROM pnl_snapshots ORDER BY date DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("query pnl snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []PNLSnapshot
	for rows.Next() {
		var s PNLSnapshot
		if err := rows.Scan(&s.Date, &s.Strategy, &s.Realized, &s.Unrealized, &s.TotalTrades); err != nil {
			return nil, fmt.Errorf("scan pnl snapshot: %w", err)
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

func (db *DB) GetPNLSnapshotsByStrategy(strategy string, limit int) ([]PNLSnapshot, error) {
	rows, err := db.Query("SELECT date, strategy, realized, unrealized, total_trades FROM pnl_snapshots WHERE strategy = ? ORDER BY date DESC LIMIT ?", strategy, limit)
	if err != nil {
		return nil, fmt.Errorf("query pnl snapshots by strategy: %w", err)
	}
	defer rows.Close()

	var snapshots []PNLSnapshot
	for rows.Next() {
		var s PNLSnapshot
		if err := rows.Scan(&s.Date, &s.Strategy, &s.Realized, &s.Unrealized, &s.TotalTrades); err != nil {
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

func (db *DB) GetRealizedPnLByStrategy(strategy string) (float64, error) {
	var total sql.NullFloat64
	err := db.QueryRow("SELECT SUM(realized) FROM pnl_snapshots WHERE strategy = ?", strategy).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("query realized pnl by strategy: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

func (db *DB) InitPaperWallet(strategy string, initial float64) error {
	// Insert the wallet if it does not exist yet.
	_, err := db.Exec(
		"INSERT OR IGNORE INTO paper_wallets (strategy, initial_balance, spent, recovered, updated_ts) VALUES (?, ?, 0, 0, ?)",
		strategy, initial, 0,
	)
	if err != nil {
		return fmt.Errorf("init paper wallet: %w", err)
	}
	// If the account is configured to simulate a real-sized bankroll, reconcile
	// existing (previously inflated) paper wallets down to it. This keeps paper
	// results honest instead of letting a stale $10k+ cushion persist in the DB.
	var stored float64
	if err := db.QueryRow("SELECT initial_balance FROM paper_wallets WHERE strategy = ?", strategy).Scan(&stored); err != nil {
		return fmt.Errorf("read paper wallet: %w", err)
	}
	if stored != initial {
		_, err = db.Exec(
			"UPDATE paper_wallets SET initial_balance = ?, spent = 0, recovered = 0, updated_ts = ? WHERE strategy = ?",
			initial, 0, strategy,
		)
		if err != nil {
			return fmt.Errorf("reconcile paper wallet: %w", err)
		}
	}
	return nil
}

// SyncRealBalance (re)sets the wallet row to a real on-chain/CLOB balance. Live
// mode only: initial_balance mirrors the real USDC the wallet currently holds,
// and spent/recovered are reset to 0 so that Balance (initial - spent +
// recovered) always equals the last real balance the bot observed. This is what
// lets the dashboard show the actual Polymarket wallet funds instead of the
// paper default of $10000. Returns an error if the wallet could not be created
// or updated.
func (db *DB) SyncRealBalance(strategy string, realBalance float64) error {
	_, err := db.Exec(
		"INSERT INTO paper_wallets (strategy, initial_balance, spent, recovered, updated_ts) VALUES (?, ?, 0, 0, ?) "+
			"ON CONFLICT(strategy) DO UPDATE SET initial_balance=excluded.initial_balance, spent=0, recovered=0, updated_ts=excluded.updated_ts",
		strategy, realBalance, 0,
	)
	if err != nil {
		return fmt.Errorf("sync real balance: %w", err)
	}
	return nil
}

func (db *DB) DeductWallet(strategy string, amount float64) error {
	_, err := db.Exec(
		"UPDATE paper_wallets SET spent = spent + ?, updated_ts = ? WHERE strategy = ?",
		amount, 0, strategy,
	)
	if err != nil {
		return fmt.Errorf("deduct wallet: %w", err)
	}
	return nil
}

func (db *DB) CreditWallet(strategy string, amount float64) error {
	_, err := db.Exec(
		"UPDATE paper_wallets SET recovered = recovered + ?, updated_ts = ? WHERE strategy = ?",
		amount, 0, strategy,
	)
	if err != nil {
		return fmt.Errorf("credit wallet: %w", err)
	}
	return nil
}

func (db *DB) GetWallet(strategy string) (*PaperWallet, error) {
	var w PaperWallet
	var ts int64
	err := db.QueryRow(
		"SELECT strategy, initial_balance, spent, recovered, updated_ts FROM paper_wallets WHERE strategy = ?",
		strategy,
	).Scan(&w.Strategy, &w.InitialBalance, &w.Spent, &w.Recovered, &ts)
	if err == sql.ErrNoRows {
		return &PaperWallet{
			Strategy:       strategy,
			InitialBalance: 10000.0,
			Spent:          0,
			Recovered:      0,
			Balance:        10000.0,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get wallet: %w", err)
	}
	w.Balance = w.InitialBalance - w.Spent + w.Recovered
	w.UpdatedTs = ts
	return &w, nil
}

func (db *DB) GetAllWallets() ([]PaperWallet, error) {
	rows, err := db.Query("SELECT strategy, initial_balance, spent, recovered, updated_ts FROM paper_wallets ORDER BY strategy")
	if err != nil {
		return nil, fmt.Errorf("query wallets: %w", err)
	}
	defer rows.Close()

	var wallets []PaperWallet
	for rows.Next() {
		var w PaperWallet
		if err := rows.Scan(&w.Strategy, &w.InitialBalance, &w.Spent, &w.Recovered, &w.UpdatedTs); err != nil {
			return nil, fmt.Errorf("scan wallet: %w", err)
		}
		w.Balance = w.InitialBalance - w.Spent + w.Recovered
		wallets = append(wallets, w)
	}
	return wallets, rows.Err()
}
