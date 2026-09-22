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

	var clobClient clob.Client
	if e.paper {
		clobClient = clob.NewPaperClient("data/vagustrader.db")
		slog.Info("Paper trading mode")
	} else {
		creds, err := e.resolveCreds()
		if err != nil {
			return err
		}
		clobClient, err = clob.NewClobClient(e.settings.PrivateKey, creds, e.settings.ClobHost, e.settings.ChainID)
		if err != nil {
			return err
		}
		if e.settings.ClobDepositWallet != "" {
			clobClient.(*clob.ClobClient).DepositWallet = e.settings.ClobDepositWallet
			slog.Info("POLY_1271 deposit wallet", "addr", e.settings.ClobDepositWallet)
		}
		slog.Info("Live trading mode")
	}

	gammaClient := gamma.NewClient(e.settings.GammaAPIURL)
	dataAPI := dataapi.NewClient(e.settings.DataAPIURL)
	notifier := notify.New(e.settings.TelegramBotToken, e.settings.TelegramChannelID)

	brk := broker.New(clobClient, dataAPI, gammaClient, database, &e.config, e.paper, notifier)

	if !e.paper && e.settings.ClobDepositWallet != "" {
		brk.SetTradingWallet(e.settings.ClobDepositWallet)
	}

	if e.paper {
		for _, s := range e.strategies {
			if s.Name() != "settlement" {
				database.InitPaperWallet(s.Name(), e.paperBankroll())
				w, _ := database.GetWallet(s.Name())
				slog.Info("Paper wallet initialized", "strategy", s.Name(), "balance", w.Balance)
			}
		}
	}

	if !e.paper {
		tradingCount := 0
		for _, s := range e.strategies {
			if s.Name() != "settlement" {
				tradingCount++
			}
		}
		if tradingCount > 1 {
			return fmt.Errorf("live mode: only 1 trading strategy allowed, got %d — use paper mode for multi-strategy", tradingCount)
		}
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
		var balance float64
		var err error
		// The wallet row feeds GetStrategyBalance, which gates live buys
		// (via the exchange, which sees on-chain collateral). It must hold
		// the AVAILABLE USDC in the proxy wallet — NOT the data-api
		// portfolio value, which is money already locked in open positions.
		// Using portfolio value here made the bot believe it had ~$55 of
		// spendable cash while on-chain collateral was $0, producing an
		// endless stream of "insufficient balance" retries.
		if e.settings.ClobDepositWallet != "" {
			native, bridged, cerr := clob.CheckWalletUSDCBalance(e.settings.RPCURL, e.settings.ClobDepositWallet)
			if cerr == nil {
				balance = native + bridged
				slog.Info("Live collateral synced",
					"wallet", e.settings.ClobDepositWallet,
					"usdc_native", native, "usdc_bridged", bridged)
			} else {
				slog.Warn("live on-chain collateral read failed, falling back to CLOB balance", "error", cerr)
				balance, err = brk.GetBalance()
			}
		} else {
			balance, err = brk.GetBalance()
		}
		if err != nil {
			slog.Warn("live balance sync failed", "error", err)
			return
		}
		for _, s := range e.strategies {
			if err := database.SyncRealBalance(s.Name(), balance); err != nil {
				slog.Warn("live balance sync write failed", "strategy", s.Name(), "error", err)
			}
		}
		slog.Info("Live balance synced", "balance", balance, "strategies", len(e.strategies))

		// Portfolio (open positions) value is informational only; log it so
		// the real account state is visible in logs without polluting the
		// spendable-balance row.
		if e.settings.ClobDepositWallet != "" {
			if pv, perr := brk.GetDataAPI().PortfolioValue(e.settings.ClobDepositWallet); perr == nil {
				slog.Info("Live portfolio value", "wallet", e.settings.ClobDepositWallet, "balance", pv)
			}
		}
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
