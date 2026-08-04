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
	MaxPositionUSDC     float64 `yaml:"max_position_usdc"`
	MaxOpenPositions    int     `yaml:"max_open_positions"`
	DailyLossLimitUSDC  float64 `yaml:"daily_loss_limit_usdc"`
}

type CopyTradingConfig struct {
	Enabled             bool    `yaml:"enabled"`
	PollIntervalSeconds int     `yaml:"poll_interval_seconds"`
	MinPnL              float64 `yaml:"min_pnl"`
	MinVolumeUSD        float64 `yaml:"min_volume_usd"`
	PriceGuard          float64 `yaml:"price_guard"`
	MaxCopyPerTrader    int     `yaml:"max_copy_per_trader"`
	MaxDaysToResolution float64 `yaml:"max_days_to_resolution"`
}

type StrategiesConfig struct {
	CopyTrading CopyTradingConfig `yaml:"copy_trading"`
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
			MaxPositionUSDC:    50.0,
			MaxOpenPositions:   10,
			DailyLossLimitUSDC: 200.0,
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
