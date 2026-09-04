package cfg

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Settings struct {
	PrivateKey          string
	ClobAPIKey          string
	ClobAPISecret       string
	ClobAPIPassphrase   string
	ClobAddress         string
	ClobDepositWallet   string
	RPCURL              string
	GammaAPIURL         string
	DataAPIURL          string
	ClobHost            string
	ChainID             int
	TelegramBotToken    string
	TelegramChannelID   string
}

func LoadSettings() Settings {
	return Settings{
		PrivateKey:        os.Getenv("PRIVATE_KEY"),
		ClobAPIKey:        os.Getenv("CLOB_API_KEY"),
		ClobAPISecret:     os.Getenv("CLOB_API_SECRET"),
		ClobAPIPassphrase: os.Getenv("CLOB_API_PASSPHRASE"),
		ClobAddress:         os.Getenv("CLOB_ADDRESS"),
		ClobDepositWallet:   os.Getenv("CLOB_DEPOSIT_WALLET"),
		RPCURL:            envOr("RPC_URL", "https://polygon-bor.publicnode.com"),
		GammaAPIURL:       envOr("GAMMA_API_URL", "https://gamma-api.polymarket.com"),
		DataAPIURL:        envOr("DATA_API_URL", "https://data-api.polymarket.com"),
		ClobHost:          envOr("CLOB_HOST", "https://clob.polymarket.com"),
		ChainID:           envInt("CHAIN_ID", 137),
		TelegramBotToken:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChannelID: os.Getenv("TELEGRAM_CHANNEL_ID"),
	}
}

type RiskConfig struct {
	MaxPositionUSDC        float64 `yaml:"max_position_usdc"`
	MaxOpenPositions       int     `yaml:"max_open_positions"`
	MaxOpenPositionsGlobal int     `yaml:"max_open_positions_global"`
	DailyLossLimitUSDC     float64 `yaml:"daily_loss_limit_usdc"`
}

type SizeTier struct {
	MinPrice float64 `yaml:"min_price"`
	MaxPrice float64 `yaml:"max_price"`
	SizeUSDC float64 `yaml:"size_usdc"`
}

type CopyTradingConfig struct {
	Enabled             bool        `yaml:"enabled"`
	PollIntervalSeconds int         `yaml:"poll_interval_seconds"`
	MinPnL              float64     `yaml:"min_pnl"`
	MinVolumeUSD        float64     `yaml:"min_volume_usd"`
	PriceGuard          float64     `yaml:"price_guard"`
	MaxCopyPerTrader    int         `yaml:"max_copy_per_trader"`
	MaxDaysToResolution float64     `yaml:"max_days_to_resolution"`
	MinEntryPrice       float64     `yaml:"min_entry_price"`
	MaxEntryPrice       float64     `yaml:"max_entry_price"`
	AllowedSports       []string    `yaml:"allowed_sports"`
	BlockedSports       []string    `yaml:"blocked_sports"`
	SizeTiers           []SizeTier  `yaml:"size_tiers"`
	MinTraderWinRate    float64     `yaml:"min_trader_win_rate"`
}

type StrategiesConfig struct {
	CopyTrading      CopyTradingConfig      `yaml:"copy_trading"`
	Settlement       SettlementConfig       `yaml:"settlement"`
	UnderHunter      UnderHunterConfig      `yaml:"under_hunter"`
	WhaleConsensus   WhaleConsensusConfig   `yaml:"whale_consensus"`
	Arbitrage        ArbitrageConfig        `yaml:"arbitrage"`
	Contrarian       ContrarianConfig       `yaml:"contrarian"`
	ResolutionSniper ResolutionSniperConfig `yaml:"resolution_sniper"`
	MomentumRider    MomentumRiderConfig    `yaml:"momentum_rider"`
	DeepValue        DeepValueConfig        `yaml:"deep_value"`
	VolumeSurge      VolumeSurgeConfig      `yaml:"volume_surge"`
}

type SettlementConfig struct {
	Enabled          bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"interval_seconds"`
}

type UnderHunterConfig struct {
	Enabled             bool     `yaml:"enabled"`
	PollIntervalSeconds int      `yaml:"poll_interval_seconds"`
	MinPrice            float64  `yaml:"min_price"`
	MaxPrice            float64  `yaml:"max_price"`
	MaxPosition         float64  `yaml:"max_position"`
	KellyFraction       float64  `yaml:"kelly_fraction"`
	AllowedSports       []string `yaml:"allowed_sports"`
}

type WhaleConsensusConfig struct {
	Enabled             bool    `yaml:"enabled"`
	PollIntervalSeconds int     `yaml:"poll_interval_seconds"`
	MinTraders          int     `yaml:"min_traders"`
	MinPnL              float64 `yaml:"min_pnl"`
	MaxPosition         float64 `yaml:"max_position"`
	KellyFraction       float64 `yaml:"kelly_fraction"`
}

type ArbitrageConfig struct {
	Enabled             bool    `yaml:"enabled"`
	PollIntervalSeconds int     `yaml:"poll_interval_seconds"`
	MinProfitUSDC       float64 `yaml:"min_profit_usdc"`
	MaxPosition         float64 `yaml:"max_position"`
}

type ContrarianConfig struct {
	Enabled               bool    `yaml:"enabled"`
	PollIntervalSeconds   int     `yaml:"poll_interval_seconds"`
	PriceChangeThreshold  float64 `yaml:"price_change_threshold"`
	LookbackMinutes       int     `yaml:"lookback_minutes"`
	MaxPosition           float64 `yaml:"max_position"`
	KellyFraction         float64 `yaml:"kelly_fraction"`
}

type ResolutionSniperConfig struct {
	Enabled             bool    `yaml:"enabled"`
	PollIntervalSeconds int     `yaml:"poll_interval_seconds"`
	MinPrice            float64 `yaml:"min_price"`
	MaxPrice            float64 `yaml:"max_price"`
	MaxDaysToEnd        int     `yaml:"max_days_to_end"`
	MinVolume           float64 `yaml:"min_volume"`
	MaxPosition         float64 `yaml:"max_position"`
	KellyFraction       float64 `yaml:"kelly_fraction"`
}

type MomentumRiderConfig struct {
	Enabled               bool    `yaml:"enabled"`
	PollIntervalSeconds   int     `yaml:"poll_interval_seconds"`
	PriceChangeThreshold  float64 `yaml:"price_change_threshold"`
	VolumeSpikeMultiplier float64 `yaml:"volume_spike_multiplier"`
	LookbackMinutes       int     `yaml:"lookback_minutes"`
	MaxPosition           float64 `yaml:"max_position"`
	KellyFraction         float64 `yaml:"kelly_fraction"`
}

type DeepValueConfig struct {
	Enabled             bool    `yaml:"enabled"`
	PollIntervalSeconds int     `yaml:"poll_interval_seconds"`
	MaxPrice            float64 `yaml:"max_price"`
	MinVolume           float64 `yaml:"min_volume"`
	MaxPosition         float64 `yaml:"max_position"`
	KellyFraction       float64 `yaml:"kelly_fraction"`
}

type VolumeSurgeConfig struct {
	Enabled              bool    `yaml:"enabled"`
	PollIntervalSeconds  int     `yaml:"poll_interval_seconds"`
	SurgeMultiplier      float64 `yaml:"surge_multiplier"`
	MinBaselineVolume    float64 `yaml:"min_baseline_volume"`
	MinPriceMove         float64 `yaml:"min_price_move"`
	LookbackMinutes      int     `yaml:"lookback_minutes"`
	MaxPosition          float64 `yaml:"max_position"`
	KellyFraction        float64 `yaml:"kelly_fraction"`
}

type MarketsConfig struct {
	MinLiquidityUSDC float64 `yaml:"min_liquidity_usdc"`
}

type AppConfig struct {
	Risk       RiskConfig       `yaml:"risk"`
	Strategies StrategiesConfig `yaml:"strategies"`
	Markets    MarketsConfig    `yaml:"markets"`
}

func LoadAppConfig(path string) AppConfig {
	defaults := AppConfig{
		Risk: RiskConfig{
			MaxPositionUSDC:        50.0,
			MaxOpenPositions:       10,
			MaxOpenPositionsGlobal: 100,
			DailyLossLimitUSDC:     200.0,
		},
		Strategies: StrategiesConfig{
			CopyTrading: CopyTradingConfig{
				Enabled:             true,
				PollIntervalSeconds: 300,
				MinPnL:              10000,
				MinVolumeUSD:        10000,
				PriceGuard:          0.05,
				MaxCopyPerTrader:    3,
				MaxDaysToResolution: 0,
				MinEntryPrice:       0.15,
				MaxEntryPrice:       0.80,
				MinTraderWinRate:    0.0,
			},
			Settlement: SettlementConfig{
				Enabled:          true,
				IntervalSeconds: 300,
			},
			UnderHunter: UnderHunterConfig{
				Enabled:             false,
				PollIntervalSeconds: 120,
				MinPrice:            0.30,
				MaxPrice:            0.55,
				MaxPosition:         10.0,
				KellyFraction:       0.25,
			},
			WhaleConsensus: WhaleConsensusConfig{
				Enabled:             false,
				PollIntervalSeconds: 180,
				MinTraders:          2,
				MinPnL:              5000,
				MaxPosition:         10.0,
				KellyFraction:       0.25,
			},
			Arbitrage: ArbitrageConfig{
				Enabled:             false,
				PollIntervalSeconds: 60,
				MinProfitUSDC:       0.10,
				MaxPosition:         50.0,
			},
		Contrarian: ContrarianConfig{
			Enabled:              false,
			PollIntervalSeconds:  120,
			PriceChangeThreshold: 0.10,
			LookbackMinutes:      60,
			MaxPosition:          5.0,
			KellyFraction:        0.15,
		},
		ResolutionSniper: ResolutionSniperConfig{
			Enabled:             false,
			PollIntervalSeconds: 300,
			MinPrice:            0.90,
			MaxPrice:            0.97,
			MaxDaysToEnd:        7,
			MinVolume:           5000,
			MaxPosition:         20.0,
			KellyFraction:       0.10,
		},
		MomentumRider: MomentumRiderConfig{
			Enabled:               false,
			PollIntervalSeconds:   60,
			PriceChangeThreshold:  0.05,
			VolumeSpikeMultiplier: 2.0,
			LookbackMinutes:       30,
			MaxPosition:           5.0,
			KellyFraction:         0.15,
		},
		DeepValue: DeepValueConfig{
			Enabled:             false,
			PollIntervalSeconds: 120,
			MaxPrice:            0.15,
			MinVolume:           10000,
			MaxPosition:         8.0,
			KellyFraction:       0.20,
		},
		VolumeSurge: VolumeSurgeConfig{
			Enabled:             false,
			PollIntervalSeconds: 120,
			SurgeMultiplier:     3.0,
			MinBaselineVolume:   5000,
			MinPriceMove:        0.03,
			LookbackMinutes:     60,
			MaxPosition:         6.0,
			KellyFraction:       0.15,
		},
		},
		Markets: MarketsConfig{
			MinLiquidityUSDC: 5000,
		},
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return defaults
	}

	var cfg AppConfig
	cfg = defaults
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return defaults
	}

	return cfg
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
