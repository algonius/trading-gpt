package htx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/types"
)

func TestMarketDataSessionReplayFixtureRunsAreDeterministic(t *testing.T) {
	first := newTestReplaySession(t)
	second := newTestReplaySession(t)

	start := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	options := types.KLineQueryOptions{
		StartTime: &start,
		Limit:     3,
	}

	firstSnapshot := readReplaySnapshot(t, first, options)
	secondSnapshot := readReplaySnapshot(t, second, options)
	if !reflect.DeepEqual(firstSnapshot, secondSnapshot) {
		t.Fatalf("fixture replay snapshots differ:\nfirst: %#v\nsecond: %#v", firstSnapshot, secondSnapshot)
	}
}

func TestMarketDataSessionPaperUsesPublicClientBoundary(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if len(r.Cookies()) > 0 || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected credential-bearing request")
		}

		requests = append(requests, r.URL.RequestURI())
		switch r.URL.Path {
		case SymbolsPath:
			writeFixture(t, w, "testdata/symbols.json")
		case KLinesPath:
			assertQuery(t, r.URL.Query(), map[string]string{
				"symbol": "btcusdt",
				"period": "1min",
				"size":   "2",
			})
			writeFixture(t, w, "testdata/klines_btcusdt_1m.json")
		case TickerPath:
			assertQuery(t, r.URL.Query(), map[string]string{
				"symbol": "btcusdt",
			})
			writeFixture(t, w, "testdata/ticker_btcusdt.json")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	session, err := NewMarketDataSession(context.Background(), Config{
		Mode: ModePaper,
		PublicMarketData: PublicMarketConfig{
			Enabled:     true,
			RESTBaseURL: server.URL,
			SymbolsPath: SymbolsPath,
			KLinesPath:  KLinesPath,
			TickerPath:  TickerPath,
		},
	})
	if err != nil {
		t.Fatalf("NewMarketDataSession returned error: %v", err)
	}

	markets, err := session.QueryMarkets(context.Background())
	if err != nil {
		t.Fatalf("QueryMarkets returned error: %v", err)
	}
	if !markets.Has("BTCUSDT") {
		t.Fatal("BTCUSDT market missing")
	}

	store, err := session.NewMarketDataStore(context.Background(), "BTCUSDT", types.Interval1m, types.KLineQueryOptions{Limit: 2})
	if err != nil {
		t.Fatalf("NewMarketDataStore returned error: %v", err)
	}
	window, ok := store.KLinesOfInterval(types.Interval1m)
	if !ok {
		t.Fatal("seeded store missing 1m kline window")
	}
	if window.Len() != 3 {
		t.Fatalf("seeded kline window len = %d, want 3", window.Len())
	}

	ticker, err := session.QueryTicker(context.Background(), "btc-usdt")
	if err != nil {
		t.Fatalf("QueryTicker returned error: %v", err)
	}
	if ticker.Last.String() != "67888.88" {
		t.Fatalf("ticker last = %s, want 67888.88", ticker.Last.String())
	}

	if len(requests) != 3 {
		t.Fatalf("request count = %d, want 3: %v", len(requests), requests)
	}
}

func TestMarketDataSessionFailsClosed(t *testing.T) {
	t.Run("paper public data off", func(t *testing.T) {
		_, err := NewMarketDataSession(context.Background(), Config{Mode: ModePaper})
		if err == nil || !strings.Contains(err.Error(), "public_market_data.enabled=true") {
			t.Fatalf("NewMarketDataSession error = %v, want public data off failure", err)
		}
	})

	t.Run("replay public data on", func(t *testing.T) {
		_, err := NewMarketDataSession(context.Background(), Config{
			Mode:             ModeReplay,
			PublicMarketData: PublicMarketConfig{Enabled: true},
		}, WithReplayMarketData(newTestReplayMarketData(t)))
		if err == nil || !strings.Contains(err.Error(), "public_market_data.enabled=false") {
			t.Fatalf("NewMarketDataSession error = %v, want replay public data failure", err)
		}
	})

	t.Run("replay missing source", func(t *testing.T) {
		_, err := NewMarketDataSession(context.Background(), Config{Mode: ModeReplay})
		if err == nil || !strings.Contains(err.Error(), "requires replay market data") {
			t.Fatalf("NewMarketDataSession error = %v, want missing replay source failure", err)
		}
	})

	t.Run("unsupported live mode", func(t *testing.T) {
		_, err := NewMarketDataSession(context.Background(), Config{
			Mode: Mode("live"),
			PublicMarketData: PublicMarketConfig{
				Enabled: true,
			},
		})
		if err == nil {
			t.Fatal("expected live mode to fail")
		}
	})

	t.Run("authenticated mode", func(t *testing.T) {
		_, err := NewMarketDataSession(context.Background(), Config{
			AllowAuthenticated: true,
			PublicMarketData: PublicMarketConfig{
				Enabled: true,
			},
		})
		if err == nil {
			t.Fatal("expected authenticated mode to fail")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := NewMarketDataSession(ctx, Config{
			Mode: ModePaper,
			PublicMarketData: PublicMarketConfig{
				Enabled: true,
			},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("NewMarketDataSession error = %v, want context.Canceled", err)
		}
	})
}

func TestMarketDataSessionQueryAndStoreFailures(t *testing.T) {
	session := newTestReplaySession(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := session.QueryMarkets(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryMarkets error = %v, want context.Canceled", err)
	}
	if _, err := session.QueryTicker(ctx, "BTCUSDT"); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryTicker error = %v, want context.Canceled", err)
	}
	if _, err := session.QueryKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryKLines error = %v, want context.Canceled", err)
	}
	if err := session.SeedMarketDataStore(ctx, bbgo.NewMarketDataStore("BTCUSDT"), "BTCUSDT", types.Interval1m, types.KLineQueryOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("SeedMarketDataStore error = %v, want context.Canceled", err)
	}

	if err := session.SeedMarketDataStore(context.Background(), nil, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{}); err == nil {
		t.Fatal("expected nil market data store to fail")
	}
	if err := session.SeedMarketDataStore(context.Background(), bbgo.NewMarketDataStore("ETHUSDT"), "BTCUSDT", types.Interval1m, types.KLineQueryOptions{}); err == nil {
		t.Fatal("expected mismatched market data store symbol to fail")
	}

	var nilSession *MarketDataSession
	if _, err := nilSession.QueryMarkets(context.Background()); err == nil {
		t.Fatal("expected nil market data session to fail")
	}
}

func newTestReplaySession(t *testing.T) *MarketDataSession {
	t.Helper()

	session, err := NewMarketDataSession(
		context.Background(),
		Config{Mode: ModeReplay},
		WithReplayMarketData(newTestReplayMarketData(t)),
	)
	if err != nil {
		t.Fatalf("NewMarketDataSession returned error: %v", err)
	}
	return session
}

type replaySnapshot struct {
	Markets     types.MarketMap
	KLines      []types.KLine
	Ticker      types.Ticker
	StoreWindow types.KLineWindow
}

func readReplaySnapshot(t *testing.T, session *MarketDataSession, options types.KLineQueryOptions) replaySnapshot {
	t.Helper()

	ctx := context.Background()
	markets, err := session.QueryMarkets(ctx)
	if err != nil {
		t.Fatalf("QueryMarkets returned error: %v", err)
	}
	klines, err := session.QueryKLines(ctx, "btc-usdt", types.Interval1m, options)
	if err != nil {
		t.Fatalf("QueryKLines returned error: %v", err)
	}
	ticker, err := session.QueryTicker(ctx, "BTCUSDT")
	if err != nil {
		t.Fatalf("QueryTicker returned error: %v", err)
	}
	store, err := session.NewMarketDataStore(ctx, "BTCUSDT", types.Interval1m, options)
	if err != nil {
		t.Fatalf("NewMarketDataStore returned error: %v", err)
	}
	window, ok := store.KLinesOfInterval(types.Interval1m)
	if !ok {
		t.Fatal("seeded store missing 1m kline window")
	}

	return replaySnapshot{
		Markets:     markets,
		KLines:      klines,
		Ticker:      *ticker,
		StoreWindow: append(types.KLineWindow(nil), (*window)...),
	}
}
