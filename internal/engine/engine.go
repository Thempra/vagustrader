package engine

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

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
