package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/vagustrader/vagustrader/internal/cfg"
	"github.com/vagustrader/vagustrader/internal/client/clob"
	"github.com/vagustrader/vagustrader/internal/client/dataapi"
	"github.com/vagustrader/vagustrader/internal/client/gamma"
	"github.com/vagustrader/vagustrader/internal/dashboard"
	"github.com/vagustrader/vagustrader/internal/db"
	"github.com/vagustrader/vagustrader/internal/engine"
	"github.com/vagustrader/vagustrader/internal/logging"
	"github.com/vagustrader/vagustrader/internal/strategy"
	"github.com/vagustrader/vagustrader/internal/strategy/copytrading"
	"github.com/vagustrader/vagustrader/internal/strategy/settlement"
)

var rootCmd = &cobra.Command{
	Use:   "vagustrader",
	Short: "Polymarket automated trading bot",
}

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start the trading engine",
	RunE: func(cmd *cobra.Command, args []string) error {
		paper, _ := cmd.Flags().GetBool("paper")
		configPath, _ := cmd.Flags().GetString("config")

		if err := logging.Setup("logs"); err != nil {
			fmt.Fprintf(os.Stderr, "warning: file logging disabled: %v\n", err)
		}

		settings := cfg.LoadSettings()
		appConfig := cfg.LoadAppConfig(configPath)

		mode := "PAPER"
		if !paper {
			mode = "LIVE"
		}
		fmt.Printf("VagusTrader starting — mode: %s\n\n", mode)

		var strategies []strategy.Strategy
		if appConfig.Strategies.CopyTrading.Enabled {
			strategies = append(strategies, copytrading.New())
		}
		strategies = append(strategies, &settlement.SettlementStrategy{})

		return engine.RunWithGracefulShutdown(settings, appConfig, paper, strategies...)
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show open positions and today's trade count",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		database, err := db.Init(dbPath)
		if err != nil {
			return fmt.Errorf("init db: %w", err)
		}

		positions, err := database.GetPositions()
		if err != nil {
			return fmt.Errorf("get positions: %w", err)
		}

		dateStr := time.Now().UTC().Format("2006-01-02")
		trades, err := database.GetTodayTrades(dateStr)
		if err != nil {
			return fmt.Errorf("get trades: %w", err)
		}

		var liveCount, paperCount int
		for _, t := range trades {
			if t.Paper {
				paperCount++
			} else {
				liveCount++
			}
		}

		fmt.Printf("Open Positions (%d)\n", len(positions))
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TOKEN\tOUTCOME\tSIZE\tAVG PRICE")
		for _, p := range positions {
			token := p.TokenID
			if len(token) > 14 {
				token = token[:14] + "\u2026"
			}
			fmt.Fprintf(tw, "%s\t%s\t%.2f\t%.3f\n", token, p.Outcome, p.Size, p.AvgPrice)
		}
		tw.Flush()
		fmt.Printf("\nToday (%s): %d live  %d paper\n", dateStr, liveCount, paperCount)
		return nil
	},
}

var marketsCmd = &cobra.Command{
	Use:   "markets",
	Short: "List tradeable markets right now",
	RunE: func(cmd *cobra.Command, args []string) error {
		minVol, _ := cmd.Flags().GetFloat64("min-vol")
		limit, _ := cmd.Flags().GetInt("limit")

		settings := cfg.LoadSettings()
		client := gamma.NewClient(settings.GammaAPIURL)
		markets, err := client.GetTradeableMarkets(minVol, 0.05, limit)
		if err != nil {
			return fmt.Errorf("get tradeable markets: %w", err)
		}

		fmt.Printf("Tradeable Markets  (vol \u2265 $%.0f)\n", minVol)
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "QUESTION\tYES/NO\tVOLUME")
		for _, m := range markets {
			question := m.Question
			if len(question) > 52 {
				question = question[:52]
			}
			prices := ""
			for i, p := range m.OutcomePrices {
				if i > 0 {
					prices += " / "
				}
				prices += fmt.Sprintf("%.2f", p)
			}
			fmt.Fprintf(tw, "%s\t%s\t$%.0f\n", question, prices, m.Volume)
		}
		tw.Flush()
		return nil
	},
}

var topTradersCmd = &cobra.Command{
	Use:   "top-traders",
	Short: "List top traders from the Polymarket leaderboard",
	RunE: func(cmd *cobra.Command, args []string) error {
		minPnL, _ := cmd.Flags().GetFloat64("min-pnl")
		limit, _ := cmd.Flags().GetInt("limit")

		settings := cfg.LoadSettings()
		client := dataapi.NewClient(settings.DataAPIURL)
		traders, err := client.GetTopTraders(minPnL, limit)
		if err != nil {
			return fmt.Errorf("get top traders: %w", err)
		}

		fmt.Printf("Top Traders  (P&L \u2265 $%.0f)\n", minPnL)
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME/ADDRESS\tP&L\tVOLUME")
		for _, t := range traders {
			label := t.Name
			if label == "" {
				label = t.Address
				if len(label) > 14 {
					label = label[:14] + "\u2026"
				}
			}
			fmt.Fprintf(tw, "%s\t$%.0f\t$%.0f\n", label, t.PnL, t.Volume)
		}
		tw.Flush()
		return nil
	},
}

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Start the web dashboard (http://localhost:<port>)",
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		dbPath, _ := cmd.Flags().GetString("db")
		dashboard.Serve(port, dbPath)
		return nil
	},
}

var deriveCredsCmd = &cobra.Command{
	Use:   "derive-creds",
	Short: "Derive CLOB API credentials from your private key",
	RunE: func(cmd *cobra.Command, args []string) error {
		settings := cfg.LoadSettings()
		fmt.Println("Deriving CLOB credentials\u2026")

		signer, err := clob.NewSigner(settings.PrivateKey, settings.ChainID)
		if err != nil {
			return fmt.Errorf("create signer: %w", err)
		}

		creds, err := clob.DeriveAPIKeyFromSigner(signer, settings.ClobHost)
		if err != nil {
			return fmt.Errorf("derive api key: %w", err)
		}

		fmt.Println("\nAdd these to your .env file:\n")
		fmt.Printf("CLOB_API_KEY=%s\n", creds.APIKey)
		fmt.Printf("CLOB_API_SECRET=%s\n", creds.APISecret)
		fmt.Printf("CLOB_API_PASSPHRASE=%s\n", creds.APIPassphrase)
		fmt.Println()
		return nil
	},
}

var simulateSettleCmd = &cobra.Command{
	Use:   "simulate-settlement",
	Short: "Simulate 1 WIN and 1 LOSS settlement for testing",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		database, err := db.Init(dbPath)
		if err != nil {
			return fmt.Errorf("init db: %w", err)
		}

		positions, err := database.GetPositions()
		if err != nil {
			return fmt.Errorf("get positions: %w", err)
		}
		if len(positions) < 2 {
			return fmt.Errorf("need at least 2 positions to simulate")
		}

		var winPos, lossPos *db.Position
		for i := range positions {
			if winPos == nil || positions[i].AvgPrice > winPos.AvgPrice {
				winPos = &positions[i]
			}
			if lossPos == nil || positions[i].AvgPrice < lossPos.AvgPrice {
				lossPos = &positions[i]
			}
		}

		winCost := winPos.Size * winPos.AvgPrice
		winPnL := winPos.Size*1.0 - winCost
		lossCost := lossPos.Size * lossPos.AvgPrice
		lossPnL := lossPos.Size*0.0 - lossCost

		fmt.Printf("WIN  token=%s... shares=%.2f avg=%.3f cost=$%.2f pnl=$%+.2f\n",
			winPos.TokenID[:min(16, len(winPos.TokenID))], winPos.Size, winPos.AvgPrice, winCost, winPnL)
		fmt.Printf("LOSS token=%s... shares=%.2f avg=%.3f cost=$%.2f pnl=$%+.2f\n",
			lossPos.TokenID[:min(16, len(lossPos.TokenID))], lossPos.Size, lossPos.AvgPrice, lossCost, lossPnL)

		if err := database.SettleTrades(winPos.TokenID, 1.0); err != nil {
			return fmt.Errorf("settle WIN: %w", err)
		}
		if err := database.SettleTrades(lossPos.TokenID, 0.0); err != nil {
			return fmt.Errorf("settle LOSS: %w", err)
		}
		if err := database.DeletePosition(winPos.TokenID); err != nil {
			return fmt.Errorf("delete WIN pos: %w", err)
		}
		if err := database.DeletePosition(lossPos.TokenID); err != nil {
			return fmt.Errorf("delete LOSS pos: %w", err)
		}

		today := time.Now().UTC().Format("2006-01-02")
		totalPnL := winPnL + lossPnL

		snapshots, _ := database.GetPNLSnapshots(1000)
		var existing float64
		var existingTrades int
		for _, s := range snapshots {
			if s.Date == today {
				existing = s.Realized
				existingTrades = s.TotalTrades
				break
			}
		}
		if err := database.SavePNLSnapshot(&db.PNLSnapshot{
			Date:        today,
			Realized:    existing + totalPnL,
			Unrealized:  0.0,
			TotalTrades: existingTrades + 2,
		}); err != nil {
			return fmt.Errorf("save snapshot: %w", err)
		}

		fmt.Printf("\nSettled: 1 WIN + 1 LOSS\n")
		fmt.Printf("Snapshot: date=%s realized=$%+.2f trades=%d\n", today, existing+totalPnL, existingTrades+2)

		remaining, _ := database.GetPositions()
		fmt.Printf("Remaining positions: %d\n", len(remaining))

		fmt.Println("\n=== Accounting verification ===")
		trades, _ := database.GetRecentTrades(100)
		for _, t := range trades {
			if !t.Result.Valid {
				continue
			}
			shares := t.SizeUSDC / t.Price
			recovered := shares * t.Result.Float64
			gain := recovered - t.SizeUSDC
			result := "WIN"
			if t.Result.Float64 < 0.99 {
				result = "LOSS"
			}
			fmt.Printf("  Trade #%d: invested=$%.2f price=%.3f result=%.1f(%s) shares=%.2f recovered=$%.2f gain=$%+.2f\n",
				t.ID, t.SizeUSDC, t.Price, t.Result.Float64, result, shares, recovered, gain)
		}

		return nil
	},
}

func init() {
	runCmd.Flags().Bool("paper", false, "Simulate orders locally, no real capital at risk")
	runCmd.Flags().String("config", "config.yml", "Path to config.yml")

	statusCmd.Flags().String("db", "data/vagustrader.db", "Path to SQLite database")

	marketsCmd.Flags().Float64("min-vol", 10000, "Minimum volume in USD")
	marketsCmd.Flags().Int("limit", 20, "Max number of markets to return")

	topTradersCmd.Flags().Float64("min-pnl", 10000, "Minimum all-time P&L in USD")
	topTradersCmd.Flags().Int("limit", 20, "Max number of traders to return")

	dashboardCmd.Flags().Int("port", 8080, "HTTP port to listen on")
	dashboardCmd.Flags().String("db", "data/vagustrader.db", "Path to SQLite database")

	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(marketsCmd)
	rootCmd.AddCommand(topTradersCmd)
	rootCmd.AddCommand(dashboardCmd)
	rootCmd.AddCommand(deriveCredsCmd)
	rootCmd.AddCommand(simulateSettleCmd)

	simulateSettleCmd.Flags().String("db", "data/vagustrader.db", "Path to SQLite database")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
