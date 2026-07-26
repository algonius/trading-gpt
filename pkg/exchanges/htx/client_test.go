package htx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestPublicClientQueriesFixturesWithGET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if len(r.Cookies()) > 0 || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected credential-bearing request headers/cookies")
		}

		switch r.URL.Path {
		case SymbolsPath:
			writeFixture(t, w, "testdata/symbols.json")
		case KLinesPath:
			assertQuery(t, r.URL.Query(), map[string]string{
				"symbol": "btcusdt",
				"period": "1min",
				"size":   "2",
				"from":   "1785024000",
				"to":     "1785024120",
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

	client := newTestPublicClient(t, server.URL)
	ctx := context.Background()

	markets, err := client.QueryMarkets(ctx)
	if err != nil {
		t.Fatalf("QueryMarkets returned error: %v", err)
	}
	if !markets.Has("BTCUSDT") {
		t.Fatal("BTCUSDT market missing")
	}

	start := time.Unix(1785024000, 0).UTC()
	end := time.Unix(1785024120, 0).UTC()
	klines, err := client.QueryKLines(ctx, "btc-usdt", types.Interval1m, types.KLineQueryOptions{
		Limit:     2,
		StartTime: &start,
		EndTime:   &end,
	})
	if err != nil {
		t.Fatalf("QueryKLines returned error: %v", err)
	}
	if len(klines) != 3 {
		t.Fatalf("len(klines) = %d, want fixture len 3", len(klines))
	}
	if got := klines[0].Close.String(); got != "67820.34" {
		t.Fatalf("first close = %s, want 67820.34", got)
	}

	ticker, err := client.QueryTicker(ctx, "BTCUSDT")
	if err != nil {
		t.Fatalf("QueryTicker returned error: %v", err)
	}
	if got := ticker.Last.String(); got != "67888.88" {
		t.Fatalf("ticker last = %s, want 67888.88", got)
	}
}

func TestPublicClientClearsInjectedCookieJar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "" {
			t.Fatalf("cookie header = %q, want empty", got)
		}
		writeFixture(t, w, "testdata/ticker_btcusdt.json")
	}))
	defer server.Close()

	client := newTestPublicClient(t, server.URL, WithHTTPClient(&http.Client{Jar: staticCookieJar{}}))
	if client.httpClient.Jar != nil {
		t.Fatal("expected injected cookie jar to be cleared")
	}
	if _, err := client.QueryTicker(context.Background(), "BTCUSDT"); err != nil {
		t.Fatalf("QueryTicker returned error: %v", err)
	}
}

func TestPublicClientRejectsDisabledAndUnsafeConfig(t *testing.T) {
	if _, err := NewPublicClient(Config{}); err == nil {
		t.Fatal("expected disabled public market data to fail")
	}

	if _, err := NewPublicClient(Config{
		AllowAuthenticated: true,
		PublicMarketData: PublicMarketConfig{
			Enabled: true,
		},
	}); err == nil {
		t.Fatal("expected authenticated config to fail")
	}

	if _, err := NewPublicClient(Config{
		PublicMarketData: PublicMarketConfig{
			Enabled:     true,
			RESTBaseURL: "ftp://example.test",
		},
	}); err == nil {
		t.Fatal("expected non-http base URL to fail")
	}
}

type staticCookieJar struct{}

func (staticCookieJar) SetCookies(*url.URL, []*http.Cookie) {}

func (staticCookieJar) Cookies(*url.URL) []*http.Cookie {
	return []*http.Cookie{{Name: "session", Value: "must-not-send"}}
}

func TestPublicClientContextCancellation(t *testing.T) {
	client := newTestPublicClient(t, "https://example.test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.QueryTicker(ctx, "BTCUSDT"); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryTicker error = %v, want context.Canceled", err)
	}
}

func TestPublicClientNilReceiver(t *testing.T) {
	var client *PublicClient
	if _, err := client.QueryTicker(context.Background(), "BTCUSDT"); err == nil || !strings.Contains(err.Error(), "client is nil") {
		t.Fatalf("QueryTicker error = %v, want nil client error", err)
	}
}

func TestPublicClientNon2xxAndHTXStatusErrors(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()

		client := newTestPublicClient(t, server.URL)
		_, err := client.QueryTicker(context.Background(), "BTCUSDT")
		if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
			t.Fatalf("QueryTicker error = %v, want HTTP 503", err)
		}
	})

	t.Run("HTX error status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"error","tick":{}}`))
		}))
		defer server.Close()

		client := newTestPublicClient(t, server.URL)
		_, err := client.QueryTicker(context.Background(), "BTCUSDT")
		if err == nil || !strings.Contains(err.Error(), `HTX ticker response status "error"`) {
			t.Fatalf("QueryTicker error = %v, want HTX error status", err)
		}
	})
}

func TestPublicClientResponseSizeBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","tick":` + strings.Repeat(" ", 64) + `{}}`))
	}))
	defer server.Close()

	client := newTestPublicClient(t, server.URL, WithMaxResponseBytes(32))
	_, err := client.QueryTicker(context.Background(), "BTCUSDT")
	if err == nil || !strings.Contains(err.Error(), "response exceeds 32 byte limit") {
		t.Fatalf("QueryTicker error = %v, want response size error", err)
	}
}

func TestPublicClientLargeSymbolsUsesEndpointBound(t *testing.T) {
	body := largeSymbolsResponse(t, DefaultMaxResponseBytes+400*1024)
	if int64(len(body)) <= DefaultMaxResponseBytes {
		t.Fatalf("large symbols body = %d bytes, want > %d", len(body), DefaultMaxResponseBytes)
	}
	if int64(len(body)) >= DefaultSymbolsMaxResponseBytes {
		t.Fatalf("large symbols body = %d bytes, want < %d", len(body), DefaultSymbolsMaxResponseBytes)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != SymbolsPath {
			t.Fatalf("path = %s, want %s", r.URL.Path, SymbolsPath)
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := newTestPublicClient(t, server.URL)
	markets, err := client.QueryMarkets(context.Background())
	if err != nil {
		t.Fatalf("QueryMarkets returned error for %d byte symbols body: %v", len(body), err)
	}
	if !markets.Has("BTCUSDT") {
		t.Fatal("BTCUSDT market missing")
	}
}

func TestPublicClientLargeSymbolsStillBounded(t *testing.T) {
	body := []byte(`{"status":"ok","data":[` + strings.Repeat(" ", int(DefaultSymbolsMaxResponseBytes)+1) + `]}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := newTestPublicClient(t, server.URL)
	_, err := client.QueryMarkets(context.Background())
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("response exceeds %d byte limit", DefaultSymbolsMaxResponseBytes)) {
		t.Fatalf("QueryMarkets error = %v, want symbols response size error", err)
	}
}

func TestPublicClientCustomResponseBoundAppliesToSymbols(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","data":` + strings.Repeat(" ", 64) + `[]}`))
	}))
	defer server.Close()

	client := newTestPublicClient(t, server.URL, WithMaxResponseBytes(32))
	_, err := client.QueryMarkets(context.Background())
	if err == nil || !strings.Contains(err.Error(), "response exceeds 32 byte limit") {
		t.Fatalf("QueryMarkets error = %v, want custom response size error", err)
	}
}

func TestPublicClientTimeoutIsBounded(t *testing.T) {
	httpClient := &http.Client{}
	client := newTestPublicClient(t, "https://example.test", WithHTTPClient(httpClient))
	if client.httpClient.Timeout != DefaultHTTPTimeout {
		t.Fatalf("timeout = %s, want %s", client.httpClient.Timeout, DefaultHTTPTimeout)
	}

	client = newTestPublicClient(t, "https://example.test", WithHTTPTimeout(time.Second))
	if client.httpClient.Timeout != time.Second {
		t.Fatalf("timeout = %s, want 1s", client.httpClient.Timeout)
	}
}

func newTestPublicClient(t *testing.T, baseURL string, options ...PublicClientOption) *PublicClient {
	t.Helper()

	client, err := NewPublicClient(Config{
		PublicMarketData: PublicMarketConfig{
			Enabled:     true,
			RESTBaseURL: baseURL,
			SymbolsPath: SymbolsPath,
			KLinesPath:  KLinesPath,
			TickerPath:  TickerPath,
		},
	}, options...)
	if err != nil {
		t.Fatalf("NewPublicClient returned error: %v", err)
	}
	return client
}

func largeSymbolsResponse(t *testing.T, minBytes int64) []byte {
	t.Helper()

	var b strings.Builder
	b.Grow(int(minBytes) + 1024)
	b.WriteString(`{"status":"ok","data":[`)
	writeSymbolSpec(&b, "btcusdt", "btc")

	for i := 0; int64(b.Len()) <= minBytes; i++ {
		b.WriteByte(',')
		writeSymbolSpec(&b, fmt.Sprintf("x%06dusdt", i), fmt.Sprintf("x%06d", i))
	}

	b.WriteString(`]}`)
	return []byte(b.String())
}

func writeSymbolSpec(b *strings.Builder, symbol string, base string) {
	fmt.Fprintf(
		b,
		`{"symbol":%q,"state":"online","base-currency":%q,"quote-currency":"usdt","price-precision":2,"amount-precision":6,"value-precision":8,"min-order-amt":"0.0001","min-order-value":"5"}`,
		symbol,
		base,
	)
}

func writeFixture(t *testing.T, w http.ResponseWriter, path string) {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func assertQuery(t *testing.T, values url.Values, expected map[string]string) {
	t.Helper()

	if len(values) != len(expected) {
		t.Fatalf("query len = %d, want %d: %s", len(values), len(expected), values.Encode())
	}
	for key, value := range expected {
		if got := values.Get(key); got != value {
			t.Fatalf("query %s = %q, want %q", key, got, value)
		}
	}
}
