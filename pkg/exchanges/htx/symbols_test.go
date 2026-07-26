package htx

import (
	"os"
	"testing"
)

func TestParseSymbolsAndBuildOnlineMarkets(t *testing.T) {
	f, err := os.Open("testdata/symbols.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	specs, err := ParseSymbols(f)
	if err != nil {
		t.Fatalf("ParseSymbols returned error: %v", err)
	}

	markets, err := OnlineMarkets(specs)
	if err != nil {
		t.Fatalf("OnlineMarkets returned error: %v", err)
	}

	if markets.Has("SUSPENDEDUSDT") {
		t.Fatal("suspended symbol should not be present in online market map")
	}

	btc, ok := markets["BTCUSDT"]
	if !ok {
		t.Fatal("BTCUSDT market missing")
	}

	if btc.Exchange.String() != CanonicalExchange {
		t.Fatalf("exchange = %q, want %q", btc.Exchange, CanonicalExchange)
	}
	if btc.LocalSymbol != "btcusdt" {
		t.Fatalf("local symbol = %q, want btcusdt", btc.LocalSymbol)
	}
	if btc.BaseCurrency != "BTC" || btc.QuoteCurrency != "USDT" {
		t.Fatalf("base/quote = %s/%s, want BTC/USDT", btc.BaseCurrency, btc.QuoteCurrency)
	}
	if btc.PricePrecision != 2 {
		t.Fatalf("price precision = %d, want 2", btc.PricePrecision)
	}
	if btc.VolumePrecision != 6 {
		t.Fatalf("volume precision = %d, want 6", btc.VolumePrecision)
	}
	if btc.TickSize.String() != "0.01" {
		t.Fatalf("tick size = %s, want 0.01", btc.TickSize.String())
	}
	if btc.StepSize.String() != "0.000001" {
		t.Fatalf("step size = %s, want 0.000001", btc.StepSize.String())
	}
	if btc.MinQuantity.String() != "0.0001" {
		t.Fatalf("min quantity = %s, want 0.0001", btc.MinQuantity.String())
	}
	if btc.MinNotional.String() != "5" {
		t.Fatalf("min notional = %s, want 5", btc.MinNotional.String())
	}
}
