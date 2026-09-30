package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/vagustrader/vagustrader/internal/broker"
	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/client/dataapi"
	"github.com/vagustrader/vagustrader/internal/client/gamma"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/notify"
	"github.com/vagustrader/vagustrader/internal/strategy"
)

type Engine struct {
	settings   cfg.Settings
	config     cfg.AppConfig
	paper      bool
	strategies []strategy.Strategy
}

// paperBankroll returns the starting virtual balance for each paper strategy
// wallet. Defaults to the real account size (127 USDC) when PAPER_BANKROLL is
// unset, so paper results reflect the live account instead of an unrealistic
// $10k cushion.
func (e *Engine) paperBankroll() float64 {
	if e.settings.PaperBankroll > 0 {
		return e.settings.PaperBankroll
	}
	return 127.0
}

func New(settings cfg.Settings, config cfg.AppConfig, paper bool) *Engine {
	return &Engine{settings: settings, config: config, paper: paper}
}

func (e *Engine) Register(s strategy.Strategy) {
	e.strategies = append(e.strategies, s)
}

func (e *Engine) Start(ctx context.Context) error {
	database, err := db.Init("data/vagustrader.db")
	if err != nil {
		return err
	}

	clobClient, err := e.buildClobClient()
	if err != nil {
		return err
	}

	gammaClient := gamma.NewClient(e.settings.GammaAPIURL)
	dataAPI := dataapi.NewClient(e.settings.DataAPIURL)
	notifier := notify.New(e.settings.TelegramBotToken, e.settings.TelegramChannelID)

	brk := broker.New(clobClient, dataAPI, gammaClient, database, &e.config, e.paper, notifier)

	if !e.paper && e.settings.ClobDepositWallet != "" {
		brk.SetTradingWallet(e.settings.ClobDepositWallet)
	}

	if err := e.prepareWallets(database); err != nil {
		return err
	}

	names := make([]string, len(e.strategies))
	for i, s := range e.strategies {
		names[i] = s.Name()
	}
	slog.Info("Starting engine", "strategies", names)

	g, gctx := errgroup.WithContext(ctx)

	if !e.paper {
		// Live mode: keep the wallet rows in sync with the real CLOB/on-chain
		// balance so the dashboard shows the actual Polymarket wallet funds
		// rather than the paper default. Read-only CLOB calls; failures are
		// logged and do not stop the engine.
		g.Go(func() error {
			return e.syncLiveBalances(gctx, brk, database)
		})
	}

	for _, s := range e.strategies {
		s := s
		g.Go(func() error {
			return s.Run(gctx, brk)
		})
	}

	if err := g.Wait(); err != nil {
		slog.Error("Engine stopped", "error", err)
		return err
	}
	slog.Info("Engine shutdown complete")
	return nil
}

// buildClobClient constructs the trading client for the current mode: a
// paper simulator in paper mode, or a live CLOB client (env creds or derived
// from the private key) otherwise.
func (e *Engine) buildClobClient() (clob.Client, error) {
	if e.paper {
		slog.Info("Paper trading mode")
		return clob.NewPaperClient("data/vagustrader.db"), nil
	}
	creds, err := e.resolveCreds()
	if err != nil {
		return nil, err
	}
	clobClient, err := clob.NewClobClient(e.settings.PrivateKey, creds, e.settings.ClobHost, e.settings.ChainID)
	if err != nil {
		return nil, err
	}
	if e.settings.ClobDepositWallet != "" {
		clobClient.DepositWallet = e.settings.ClobDepositWallet
		slog.Info("POLY_1271 deposit wallet", "addr", e.settings.ClobDepositWallet)
	}
	slog.Info("Live trading mode")
	return clobClient, nil
}

// prepareWallets initializes paper wallets (paper mode) and enforces the
// live-mode single-trading-strategy rule.
func (e *Engine) prepareWallets(database *db.DB) error {
	if e.paper {
		for _, s := range e.strategies {
			if s.Name() != "settlement" {
				database.InitPaperWallet(s.Name(), e.paperBankroll())
				w, _ := database.GetWallet(s.Name())
				slog.Info("Paper wallet initialized", "strategy", s.Name(), "balance", w.Balance)
			}
		}
		return nil
	}
	tradingCount := 0
	for _, s := range e.strategies {
		if s.Name() != "settlement" {
			tradingCount++
		}
	}
	if tradingCount > 1 {
		return fmt.Errorf("live mode: only 1 trading strategy allowed, got %d — use paper mode for multi-strategy", tradingCount)
	}
	return nil
}

func (e *Engine) resolveCreds() (*clob.ClobCreds, error) {
	if e.settings.ClobAPIKey != "" {
		creds := &clob.ClobCreds{
			APIKey:        e.settings.ClobAPIKey,
			APISecret:     e.settings.ClobAPISecret,
			APIPassphrase: e.settings.ClobAPIPassphrase,
			Address:       e.settings.ClobAddress,
		}
		slog.Info("CLOB creds loaded from env", "address", creds.Address, "api_key", creds.APIKey[:8]+"...")
		return creds, nil
	}
	slog.Info("Deriving CLOB credentials from private key")
	signer, err := clob.NewSigner(e.settings.PrivateKey, e.settings.ChainID)
	if err != nil {
		return nil, err
	}
	slog.Info("Signer address derived", "address", signer.Address())
	targetAddr := signer.Address()
	if e.settings.ClobDepositWallet != "" {
		targetAddr = e.settings.ClobDepositWallet
		slog.Info("Using deposit wallet for API key binding", "deposit_wallet", targetAddr)
	}
	creds, err := clob.DeriveAPIKeyForWallet(signer, e.settings.ClobHost, targetAddr)
	if err != nil {
		return nil, err
	}
	slog.Info("CLOB creds derived", "address", targetAddr, "api_key", creds.APIKey[:8]+"...", "secret_len", len(creds.APISecret), "passphrase_len", len(creds.APIPassphrase))
	return creds, nil
}

func RunWithGracefulShutdown(settings cfg.Settings, config cfg.AppConfig, paper bool, strategies ...strategy.Strategy) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("Shutdown signal received")
		cancel()
	}()

	engine := New(settings, config, paper)
	for _, s := range strategies {
		engine.Register(s)
	}
	return engine.Start(ctx)
}

// syncLiveBalances runs for the whole lifetime of a live engine. It periodically
// reads the real CLOB balance (the available USDC collateral in the Polymarket
// wallet) and writes it into the paper_wallets rows so the dashboard reflects
// the real funds. On error it logs and continues with the last known value; the
// ticker stops when the parent context is cancelled.
func (e *Engine) syncLiveBalances(ctx context.Context, brk *broker.Broker, database *db.DB) error {
	const interval = 60 * time.Second

	syncOnce := func() {
		balance, err := brk.GetBalance()
		if err != nil {
			slog.Warn("live CLOB balance read failed", "error", err)
			slog.Warn("live balance sync failed", "error", err)
			return
		}
		slog.Info("Live collateral synced (CLOB balance)",
			"wallet", e.settings.ClobDepositWallet, "balance", balance, "strategy", len(e.strategies))
		syncStrategyBalances(database, balance, e.strategies)
		slog.Info("Live balance synced", "balance", balance, "strategies", len(e.strategies))

		// Portfolio (open positions) value is informational only; log it so
		// the real account state is visible in logs without polluting the
		// spendable-balance row.
		reportPortfolioValue(brk, e.settings.ClobDepositWallet)
	}

	// Sync immediately at startup so the dashboard is correct right away.
	syncOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			syncOnce()
		}
	}
}

// syncStrategyBalances writes the synced collateral balance into every
// strategy wallet row (best-effort per strategy).
func syncStrategyBalances(database *db.DB, balance float64, strategies []strategy.Strategy) {
	for _, s := range strategies {
		if err := database.SyncRealBalance(s.Name(), balance); err != nil {
			slog.Warn("live balance sync write failed", "strategy", s.Name(), "error", err)
		}
	}
}

// reportPortfolioValue logs the open-positions portfolio value when a deposit
// wallet is configured.
func reportPortfolioValue(brk *broker.Broker, wallet string) {
	if wallet == "" {
		return
	}
	if pv, perr := brk.GetDataAPI().PortfolioValue(wallet); perr == nil {
		slog.Info("Live portfolio value", "wallet", wallet, "balance", pv)
	}
}
