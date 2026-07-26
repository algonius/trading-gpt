package htx

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestParseKLinesSortsAndMapsHTXFields(t *testing.T) {
	f, err := os.Open("testdata/klines_btcusdt_1m.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	klines, err := ParseKLines(f, "btc-usdt", types.Interval1m)
	if err != nil {
		t.Fatalf("ParseKLines returned error: %v", err)
	}
	if len(klines) != 3 {
		t.Fatalf("len(klines) = %d, want 3", len(klines))
	}

	first := klines[0]
	if first.Exchange.String() != CanonicalExchange {
		t.Fatalf("exchange = %q, want %q", first.Exchange, CanonicalExchange)
	}
	if first.Symbol != "BTCUSDT" {
		t.Fatalf("symbol = %q, want BTCUSDT", first.Symbol)
	}
	if first.Interval != types.Interval1m {
		t.Fatalf("interval = %q, want %q", first.Interval, types.Interval1m)
	}
	if got := first.StartTime.Time().Format(time.RFC3339); got != "2026-07-26T00:00:00Z" {
		t.Fatalf("start time = %s, want 2026-07-26T00:00:00Z", got)
	}
	if got := first.EndTime.Time().Format(time.RFC3339Nano); got != "2026-07-26T00:00:59.999Z" {
		t.Fatalf("end time = %s, want 2026-07-26T00:00:59.999Z", got)
	}
	if first.Open.String() != "67800.12" {
		t.Fatalf("open = %s, want 67800.12", first.Open.String())
	}
	if first.Close.String() != "67820.34" {
		t.Fatalf("close = %s, want 67820.34", first.Close.String())
	}
	if first.Low.String() != "67790" {
		t.Fatalf("low = %s, want 67790", first.Low.String())
	}
	if first.High.String() != "67840.5" {
		t.Fatalf("high = %s, want 67840.5", first.High.String())
	}
	if first.Volume.String() != "12.3456" {
		t.Fatalf("base volume = %s, want 12.3456", first.Volume.String())
	}
	if first.QuoteVolume.String() != "837654.321" {
		t.Fatalf("quote volume = %s, want 837654.321", first.QuoteVolume.String())
	}
	if first.NumberOfTrades != 42 {
		t.Fatalf("trade count = %d, want 42", first.NumberOfTrades)
	}
	if !first.Closed {
		t.Fatal("fixture klines should be marked closed")
	}
}

func TestParseTickerMapsHTXDetailMerged(t *testing.T) {
	f, err := os.Open("testdata/ticker_btcusdt.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	ticker, err := ParseTicker(f, "BTCUSDT")
	if err != nil {
		t.Fatalf("ParseTicker returned error: %v", err)
	}

	if got := ticker.Time.Format(time.RFC3339Nano); got != "2026-07-26T00:03:00.123Z" {
		t.Fatalf("ticker time = %s, want 2026-07-26T00:03:00.123Z", got)
	}
	if ticker.Open.String() != "67800.12" {
		t.Fatalf("open = %s, want 67800.12", ticker.Open.String())
	}
	if ticker.Last.String() != "67888.88" {
		t.Fatalf("last = %s, want 67888.88", ticker.Last.String())
	}
	if ticker.Buy.String() != "67888.87" {
		t.Fatalf("buy = %s, want 67888.87", ticker.Buy.String())
	}
	if ticker.Sell.String() != "67888.89" {
		t.Fatalf("sell = %s, want 67888.89", ticker.Sell.String())
	}
	if ticker.Volume.String() != "34.5678" {
		t.Fatalf("volume = %s, want 34.5678", ticker.Volume.String())
	}
}

func TestReplayMarketDataDeterministicQueries(t *testing.T) {
	replay := newTestReplayMarketData(t)
	ctx := context.Background()

	markets, err := replay.QueryMarkets(ctx)
	if err != nil {
		t.Fatalf("QueryMarkets returned error: %v", err)
	}
	if !markets.Has("BTCUSDT") {
		t.Fatal("BTCUSDT market missing")
	}
	if market := markets["BTCUSDT"]; market.PricePrecision != 2 || market.VolumePrecision != 6 {
		t.Fatalf("BTCUSDT precision = %d/%d, want 2/6", market.PricePrecision, market.VolumePrecision)
	}
	if market := markets["BTCUSDT"]; market.TickSize.String() != "0.01" || market.StepSize.String() != "0.000001" {
		t.Fatalf("BTCUSDT tick/step = %s/%s, want 0.01/0.000001", market.TickSize.String(), market.StepSize.String())
	}

	start := time.Date(2026, 7, 26, 0, 1, 0, 0, time.UTC)
	klines, err := replay.QueryKLines(ctx, "btc-usdt", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("QueryKLines returned error: %v", err)
	}
	if len(klines) != 1 {
		t.Fatalf("len(klines) = %d, want 1", len(klines))
	}
	if got := klines[0].StartTime.Time().Format(time.RFC3339); got != "2026-07-26T00:01:00Z" {
		t.Fatalf("replayed start time = %s, want 2026-07-26T00:01:00Z", got)
	}

	again, err := replay.QueryKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("second QueryKLines returned error: %v", err)
	}
	if again[0].Close.String() != klines[0].Close.String() {
		t.Fatalf("deterministic replay close = %s, want %s", again[0].Close.String(), klines[0].Close.String())
	}

	ticker, err := replay.QueryTicker(ctx, "btc-usdt")
	if err != nil {
		t.Fatalf("QueryTicker returned error: %v", err)
	}
	if ticker.Last.String() != "67888.88" {
		t.Fatalf("ticker last = %s, want 67888.88", ticker.Last.String())
	}

	tickers, err := replay.QueryTickers(ctx)
	if err != nil {
		t.Fatalf("QueryTickers returned error: %v", err)
	}
	if got := tickers["BTCUSDT"].Last.String(); got != "67888.88" {
		t.Fatalf("all tickers BTCUSDT last = %s, want 67888.88", got)
	}
}

func TestReplayMarketDataRejectsUnsafeAndUnknownInputs(t *testing.T) {
	markets := testMarkets(t)

	if _, err := NewReplayMarketData(Config{AllowAuthenticated: true}, markets); err == nil {
		t.Fatal("expected authenticated replay config to fail")
	}
	if _, err := NewReplayMarketData(Config{
		PublicMarketData: PublicMarketConfig{Enabled: true},
	}, markets); err == nil {
		t.Fatal("expected public market data enablement to fail")
	}

	replay := newTestReplayMarketData(t)
	if _, err := replay.QueryTicker(context.Background(), "ETHUSDT"); err == nil {
		t.Fatal("expected unknown ticker to fail")
	}
	if _, err := replay.QueryKLines(context.Background(), "BTCUSDT", types.Interval5m, types.KLineQueryOptions{}); err == nil {
		t.Fatal("expected unloaded interval to fail")
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := ParseKLines(strings.NewReader(`{"status":"error","data":[]}`), "BTCUSDT", types.Interval1m); err == nil {
		t.Fatal("expected HTX kline error status to fail")
	}
	if _, err := ParseKLines(strings.NewReader(`{"status":"ok","data":[{"id":0}]}`), "BTCUSDT", types.Interval1m); err == nil {
		t.Fatal("expected invalid kline id to fail")
	}
	if _, err := ParseTicker(strings.NewReader(`{"status":"error","tick":{}}`), "BTCUSDT"); err == nil {
		t.Fatal("expected HTX ticker error status to fail")
	}
	if _, err := ParseTicker(strings.NewReader(`{"status":"ok","ts":1785024180123,"tick":{"bid":[],"ask":[1]}}`), "BTCUSDT"); err == nil {
		t.Fatal("expected missing bid to fail")
	}
	if _, err := ParseTicker(strings.NewReader(`{"status":"ok","tick":{"bid":[1],"ask":[2]}}`), "BTCUSDT"); err == nil {
		t.Fatal("expected missing ticker timestamp to fail")
	}
}

func newTestReplayMarketData(t *testing.T) *ReplayMarketData {
	t.Helper()

	klineFile, err := os.Open("testdata/klines_btcusdt_1m.json")
	if err != nil {
		t.Fatal(err)
	}
	defer klineFile.Close()

	tickerFile, err := os.Open("testdata/ticker_btcusdt.json")
	if err != nil {
		t.Fatal(err)
	}
	defer tickerFile.Close()

	replay, err := LoadReplayMarketData(
		Config{Mode: ModeReplay},
		testMarkets(t),
		map[string]map[types.Interval]io.Reader{
			"btcusdt": {
				types.Interval1m: klineFile,
			},
		},
		map[string]io.Reader{
			"btcusdt": tickerFile,
		},
	)
	if err != nil {
		t.Fatalf("LoadReplayMarketData returned error: %v", err)
	}
	return replay
}

func testMarkets(t *testing.T) types.MarketMap {
	t.Helper()

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
	return markets
}
