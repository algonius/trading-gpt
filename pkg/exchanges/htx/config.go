package htx

import (
	"fmt"
	"strings"
)

const (
	CanonicalExchange = "htx"
	LegacyHuobiAlias  = "huobi"

	DefaultRESTBaseURL = "https://api.huobi.pro"

	SymbolsPath = "/v1/common/symbols"
	KLinesPath  = "/market/history/kline"
	TickerPath  = "/market/detail/merged"
)

type Mode string

const (
	ModePaper  Mode = "paper"
	ModeReplay Mode = "replay"
)

type Config struct {
	Exchange           string             `json:"exchange" yaml:"exchange"`
	Mode               Mode               `json:"mode" yaml:"mode"`
	PublicMarketData   PublicMarketConfig `json:"public_market_data" yaml:"public_market_data"`
	FixtureSymbolsPath string             `json:"fixture_symbols_path" yaml:"fixture_symbols_path"`
	Symbols            []string           `json:"symbols" yaml:"symbols"`

	AllowAuthenticated bool `json:"allow_authenticated" yaml:"allow_authenticated"`
}

type PublicMarketConfig struct {
	Enabled     bool   `json:"enabled" yaml:"enabled"`
	RESTBaseURL string `json:"rest_base_url" yaml:"rest_base_url"`
	SymbolsPath string `json:"symbols_path" yaml:"symbols_path"`
	KLinesPath  string `json:"klines_path" yaml:"klines_path"`
	TickerPath  string `json:"ticker_path" yaml:"ticker_path"`
}

func NormalizeExchangeAlias(exchange string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(exchange)) {
	case "", CanonicalExchange, LegacyHuobiAlias, "huobipro", "huobi-pro":
		return CanonicalExchange, nil
	default:
		return "", fmt.Errorf("unsupported HTX exchange alias %q", exchange)
	}
}

func NormalizeConfig(cfg Config) (Config, error) {
	exchange, err := NormalizeExchangeAlias(cfg.Exchange)
	if err != nil {
		return Config{}, err
	}
	cfg.Exchange = exchange

	if cfg.Mode == "" {
		cfg.Mode = ModePaper
	}

	switch cfg.Mode {
	case ModePaper, ModeReplay:
	default:
		return Config{}, fmt.Errorf("unsupported HTX mode %q: only paper/replay modes are allowed", cfg.Mode)
	}

	if cfg.AllowAuthenticated {
		return Config{}, fmt.Errorf("HTX authenticated access is disabled for the paper-safe KR4 slice")
	}

	if cfg.PublicMarketData.RESTBaseURL == "" {
		cfg.PublicMarketData.RESTBaseURL = DefaultRESTBaseURL
	}
	if cfg.PublicMarketData.SymbolsPath == "" {
		cfg.PublicMarketData.SymbolsPath = SymbolsPath
	}
	if cfg.PublicMarketData.KLinesPath == "" {
		cfg.PublicMarketData.KLinesPath = KLinesPath
	}
	if cfg.PublicMarketData.TickerPath == "" {
		cfg.PublicMarketData.TickerPath = TickerPath
	}

	for i, symbol := range cfg.Symbols {
		cfg.Symbols[i] = NormalizeSymbol(symbol)
	}

	return cfg, nil
}

func NormalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(symbol), "-", ""))
}
