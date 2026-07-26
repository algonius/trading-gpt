package htx

import (
	"testing"
)

func TestNormalizeExchangeAlias(t *testing.T) {
	tests := map[string]string{
		"":          CanonicalExchange,
		"htx":       CanonicalExchange,
		"HTX":       CanonicalExchange,
		"huobi":     CanonicalExchange,
		"HuoBi":     CanonicalExchange,
		"huobipro":  CanonicalExchange,
		"huobi-pro": CanonicalExchange,
	}

	for input, expected := range tests {
		actual, err := NormalizeExchangeAlias(input)
		if err != nil {
			t.Fatalf("NormalizeExchangeAlias(%q) returned error: %v", input, err)
		}
		if actual != expected {
			t.Fatalf("NormalizeExchangeAlias(%q) = %q, want %q", input, actual, expected)
		}
	}

	if _, err := NormalizeExchangeAlias("okex"); err == nil {
		t.Fatal("expected unsupported exchange alias to fail")
	}
}

func TestNormalizeConfigDefaultsPaperSafe(t *testing.T) {
	cfg, err := NormalizeConfig(Config{
		Exchange: LegacyHuobiAlias,
		Symbols:  []string{"btc-usdt", " ethusdt "},
	})
	if err != nil {
		t.Fatalf("NormalizeConfig returned error: %v", err)
	}

	if cfg.Exchange != CanonicalExchange {
		t.Fatalf("exchange = %q, want %q", cfg.Exchange, CanonicalExchange)
	}
	if cfg.Mode != ModePaper {
		t.Fatalf("mode = %q, want %q", cfg.Mode, ModePaper)
	}
	if cfg.PublicMarketData.Enabled {
		t.Fatal("public market data should be disabled by default in the paper-safe slice")
	}
	if cfg.PublicMarketData.RESTBaseURL != DefaultRESTBaseURL {
		t.Fatalf("base url = %q, want %q", cfg.PublicMarketData.RESTBaseURL, DefaultRESTBaseURL)
	}
	if cfg.PublicMarketData.SymbolsPath != SymbolsPath {
		t.Fatalf("symbols path = %q, want %q", cfg.PublicMarketData.SymbolsPath, SymbolsPath)
	}
	if cfg.PublicMarketData.KLinesPath != KLinesPath {
		t.Fatalf("klines path = %q, want %q", cfg.PublicMarketData.KLinesPath, KLinesPath)
	}
	if cfg.PublicMarketData.TickerPath != TickerPath {
		t.Fatalf("ticker path = %q, want %q", cfg.PublicMarketData.TickerPath, TickerPath)
	}
	if got := cfg.Symbols[0]; got != "BTCUSDT" {
		t.Fatalf("symbol[0] = %q, want BTCUSDT", got)
	}
	if got := cfg.Symbols[1]; got != "ETHUSDT" {
		t.Fatalf("symbol[1] = %q, want ETHUSDT", got)
	}
}

func TestNormalizeConfigRejectsUnsafeModes(t *testing.T) {
	if _, err := NormalizeConfig(Config{Mode: "live"}); err == nil {
		t.Fatal("expected live mode to fail")
	}

	if _, err := NormalizeConfig(Config{AllowAuthenticated: true}); err == nil {
		t.Fatal("expected authenticated access to fail")
	}
}
