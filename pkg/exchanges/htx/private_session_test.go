package htx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestPrivateSessionLifecycleWithHTTPTransport(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		assertPrivateSessionRequestIsCredentialFree(t, r)
		assertPrivateAuthQuery(t, r.URL.Query())

		switch r.URL.Path {
		case "/v1/account/accounts/100009/balance":
			if r.Method != http.MethodGet {
				t.Fatalf("balance method = %s, want GET", r.Method)
			}
			_, _ = w.Write([]byte(privateSessionBalanceResponse))
		case PrivateOrderPlacePath:
			if r.Method != http.MethodPost {
				t.Fatalf("submit method = %s, want POST", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			expected := `{"account-id":"100009","symbol":"btcusdt","type":"sell-limit","amount":"0.05","price":"67810","client-order-id":"fixture-private-session"}`
			if string(body) != expected {
				t.Fatalf("submit body = %s, want %s", string(body), expected)
			}
			_, _ = w.Write([]byte(`{"status":"ok","data":"59379"}`))
		case "/v1/order/orders/59379/submitcancel":
			if r.Method != http.MethodPost {
				t.Fatalf("cancel method = %s, want POST", r.Method)
			}
			_, _ = w.Write([]byte(`{"status":"ok","data":"59379"}`))
		case "/v1/order/orders/59379":
			if r.Method != http.MethodGet {
				t.Fatalf("order method = %s, want GET", r.Method)
			}
			_, _ = w.Write(readPrivateFixture(t, "testdata/order_filled.json"))
		case "/v1/order/orders/59379/matchresults":
			if r.Method != http.MethodGet {
				t.Fatalf("match results method = %s, want GET", r.Method)
			}
			_, _ = w.Write(readPrivateFixture(t, "testdata/order_match_results.json"))
		default:
			t.Fatalf("unexpected private session path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	session := newTestPrivateSession(t, server.URL)
	if session.Config().Mode != ModePaper || session.AccountID() != "100009" {
		t.Fatalf("session config/account = %q/%q, want paper/100009", session.Config().Mode, session.AccountID())
	}

	balances, err := session.QueryAccountBalances(context.Background())
	if err != nil {
		t.Fatalf("QueryAccountBalances returned error: %v", err)
	}
	if balances["USDT"].Available.String() != "100.5" || balances["USDT"].Locked.String() != "2" {
		t.Fatalf("USDT balance = %#v, want 100.5 available and 2 locked", balances["USDT"])
	}

	submitAck, err := session.SubmitOrder(context.Background(), privateSessionSubmitOrder())
	if err != nil {
		t.Fatalf("SubmitOrder returned error: %v", err)
	}
	if submitAck.OrderID != "59379" {
		t.Fatalf("submit order id = %q, want 59379", submitAck.OrderID)
	}

	cancelAck, err := session.CancelOrder(context.Background(), "59379")
	if err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}
	if cancelAck.OrderID != "59379" {
		t.Fatalf("cancel order id = %q, want 59379", cancelAck.OrderID)
	}

	order, err := session.QueryOrder(context.Background(), "59379")
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if order.Symbol != "BTCUSDT" || order.Side != types.SideTypeSell || order.Type != types.OrderTypeLimit || order.Status != types.OrderStatusFilled {
		t.Fatalf("mapped order = %#v, want filled BTCUSDT sell limit", order)
	}

	trades, err := session.QueryOrderTrades(context.Background(), "59379")
	if err != nil {
		t.Fatalf("QueryOrderTrades returned error: %v", err)
	}
	if len(trades) != 2 || trades[0].Symbol != "BTCUSDT" || trades[0].Side != types.SideTypeSell {
		t.Fatalf("mapped trades = %#v, want BTCUSDT sell trades", trades)
	}

	expectedRequests := []string{
		"GET /v1/account/accounts/100009/balance",
		"POST /v1/order/orders/place",
		"POST /v1/order/orders/59379/submitcancel",
		"GET /v1/order/orders/59379",
		"GET /v1/order/orders/59379/matchresults",
	}
	if strings.Join(requests, "\n") != strings.Join(expectedRequests, "\n") {
		t.Fatalf("requests = %#v, want %#v", requests, expectedRequests)
	}
}

func TestPrivateSessionUsesInjectedTransport(t *testing.T) {
	transport := &fakePrivateTransport{
		responses: []fakePrivateResult{
			{response: privateJSONResponse(http.StatusOK, privateSessionBalanceResponse)},
		},
	}
	session := newTestPrivateSession(t, "https://fixture.invalid", WithPrivateSessionTransport(transport))

	balances, err := session.QueryAccountBalances(context.Background())
	if err != nil {
		t.Fatalf("QueryAccountBalances returned error: %v", err)
	}
	if balances["BTC"].Available.String() != "0.1" {
		t.Fatalf("BTC balance = %#v, want 0.1 available", balances["BTC"])
	}
	if len(transport.requests) != 1 {
		t.Fatalf("transport requests = %d, want 1", len(transport.requests))
	}
	if req := transport.requests[0]; req.Endpoint == "" || req.Path != "/v1/account/accounts/100009/balance" || req.Method != "GET" {
		t.Fatalf("transport request = %#v, want signed balance GET", req)
	}
}

func TestPrivateSessionContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewPrivateSession(ctx, Config{}, "100009", testPrivateCredentials()); !errors.Is(err, context.Canceled) {
		t.Fatalf("NewPrivateSession error = %v, want context.Canceled", err)
	}

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(privateSessionBalanceResponse))
	}))
	defer server.Close()

	session := newTestPrivateSession(t, server.URL)
	if _, err := session.QueryAccountBalances(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryAccountBalances error = %v, want context.Canceled", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server hits = %d, want 0 for canceled context", hits.Load())
	}
}

func TestPrivateSessionResponseClassification(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			name:    "non-2xx",
			status:  http.StatusServiceUnavailable,
			body:    `temporarily unavailable`,
			wantErr: "HTTP 503: temporarily unavailable",
		},
		{
			name:    "HTX error status",
			status:  http.StatusOK,
			body:    `{"status":"error","err-code":"base-record-invalid","err-msg":"invalid order"}`,
			wantErr: `code "base-record-invalid"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			session := newTestPrivateSession(t, server.URL)
			_, err := session.QueryOrder(context.Background(), "59379")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("QueryOrder error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestPrivateSessionFailsClosed(t *testing.T) {
	t.Run("replay mode", func(t *testing.T) {
		_, err := NewPrivateSession(context.Background(), Config{Mode: ModeReplay}, "100009", testPrivateCredentials())
		if err == nil || !strings.Contains(err.Error(), "requires paper mode") {
			t.Fatalf("NewPrivateSession error = %v, want paper-mode failure", err)
		}
	})

	t.Run("live mode", func(t *testing.T) {
		_, err := NewPrivateSession(context.Background(), Config{Mode: Mode("live")}, "100009", testPrivateCredentials())
		if err == nil || !strings.Contains(err.Error(), "unsupported HTX mode") {
			t.Fatalf("NewPrivateSession error = %v, want unsupported mode failure", err)
		}
	})

	t.Run("authenticated config flag", func(t *testing.T) {
		_, err := NewPrivateSession(context.Background(), Config{AllowAuthenticated: true}, "100009", testPrivateCredentials())
		if err == nil || !strings.Contains(err.Error(), "authenticated access is disabled") {
			t.Fatalf("NewPrivateSession error = %v, want authenticated config failure", err)
		}
	})

	t.Run("blank account id", func(t *testing.T) {
		_, err := NewPrivateSession(context.Background(), Config{}, " ", testPrivateCredentials())
		if err == nil || !strings.Contains(err.Error(), "account id is empty") {
			t.Fatalf("NewPrivateSession error = %v, want blank account id failure", err)
		}
	})

	t.Run("blank base URL", func(t *testing.T) {
		_, err := NewPrivateSession(context.Background(), Config{}, "100009", testPrivateCredentials())
		if err == nil || !strings.Contains(err.Error(), "base URL is empty") {
			t.Fatalf("NewPrivateSession error = %v, want blank base URL failure", err)
		}
	})

	t.Run("blank credentials", func(t *testing.T) {
		_, err := NewPrivateSession(
			context.Background(),
			Config{},
			"100009",
			PrivateCredentials{},
			WithPrivateSessionBaseURL("https://fixture.invalid"),
			WithPrivateSessionTransport(&fakePrivateTransport{}),
		)
		if err == nil || !strings.Contains(err.Error(), "access key id is empty") {
			t.Fatalf("NewPrivateSession error = %v, want blank credentials failure", err)
		}
	})

	t.Run("nil receiver", func(t *testing.T) {
		var session *PrivateSession
		_, err := session.QueryOrder(context.Background(), "59379")
		if err == nil || !strings.Contains(err.Error(), "private session is nil") {
			t.Fatalf("QueryOrder error = %v, want nil session failure", err)
		}
	})
}

const privateSessionBalanceResponse = `{
	"status":"ok",
	"data":{
		"id":100009,
		"type":"spot",
		"state":"working",
		"list":[
			{"currency":"usdt","type":"trade","balance":"100.5"},
			{"currency":"usdt","type":"frozen","balance":"2"},
			{"currency":"btc","type":"trade","balance":"0.1"}
		]
	}
}`

func newTestPrivateSession(t *testing.T, baseURL string, options ...PrivateSessionOption) *PrivateSession {
	t.Helper()

	allOptions := []PrivateSessionOption{
		WithPrivateSessionBaseURL(baseURL),
		WithPrivateSessionClientOptions(WithPrivateClock(func() time.Time {
			return time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC)
		})),
	}
	allOptions = append(allOptions, options...)

	session, err := NewPrivateSession(context.Background(), Config{}, "100009", testPrivateCredentials(), allOptions...)
	if err != nil {
		t.Fatalf("NewPrivateSession returned error: %v", err)
	}
	return session
}

func testPrivateCredentials() PrivateCredentials {
	return PrivateCredentials{AccessKeyID: dummyAccessKey, SecretKey: dummySecretKey}
}

func privateSessionSubmitOrder() types.SubmitOrder {
	return types.SubmitOrder{
		ClientOrderID: "fixture-private-session",
		Symbol:        "btc-usdt",
		Side:          types.SideTypeSell,
		Type:          types.OrderTypeLimit,
		Quantity:      fixedpoint.MustNewFromString("0.05"),
		Price:         fixedpoint.MustNewFromString("67810"),
	}
}

func assertPrivateSessionRequestIsCredentialFree(t *testing.T, r *http.Request) {
	t.Helper()

	if r.Header.Get("Authorization") != "" {
		t.Fatalf("unexpected Authorization header")
	}
	if r.Header.Get("Cookie") != "" {
		t.Fatalf("unexpected Cookie header")
	}
}
