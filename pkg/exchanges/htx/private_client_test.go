package htx

import (
	"context"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestPrivateClientBuildsSignedSubmitOrderWithInjectedTransport(t *testing.T) {
	transport := &fakePrivateTransport{
		responses: []fakePrivateResult{
			{response: privateJSONResponse(200, `{"status":"ok","data":"59378"}`)},
		},
	}
	client := newTestPrivateClient(t, transport)

	order := types.SubmitOrder{
		ClientOrderID: "client-1",
		Symbol:        "btc-usdt",
		Side:          types.SideTypeBuy,
		Type:          types.OrderTypeLimit,
		Quantity:      fixedpoint.MustNewFromString("0.1"),
		Price:         fixedpoint.MustNewFromString("67800.12"),
	}
	ack, err := client.SubmitOrder(context.Background(), "100009", order)
	if err != nil {
		t.Fatalf("SubmitOrder returned error: %v", err)
	}
	if ack.OrderID != "59378" {
		t.Fatalf("submit ack order id = %q, want 59378", ack.OrderID)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("private transport requests = %d, want 1", len(transport.requests))
	}

	req := transport.requests[0]
	if req.Method != "POST" || req.Host != "api.huobi.pro" || req.Path != PrivateOrderPlacePath {
		t.Fatalf("request method/host/path = %s/%s/%s", req.Method, req.Host, req.Path)
	}

	expectedBody := `{"account-id":"100009","symbol":"btcusdt","type":"buy-limit","amount":"0.1","price":"67800.12","client-order-id":"client-1"}`
	if string(req.Body) != expectedBody {
		t.Fatalf("submit body = %s, want %s", string(req.Body), expectedBody)
	}

	assertPrivateAuthQuery(t, req.Query)
	expectedPayload := strings.Join([]string{
		"POST",
		"api.huobi.pro",
		PrivateOrderPlacePath,
		"AccessKeyId=fixture-access-key&SignatureMethod=HmacSHA256&SignatureVersion=2&Timestamp=2026-07-26T01%3A02%3A03",
	}, "\n")
	if req.SigningPayload != expectedPayload {
		t.Fatalf("signing payload = %q, want %q", req.SigningPayload, expectedPayload)
	}
	if strings.Contains(req.SigningPayload, "67800.12") || strings.Contains(req.SigningPayload, dummySecretKey) {
		t.Fatalf("signing payload must not contain POST body or secret: %s", req.SigningPayload)
	}
	if strings.Contains(req.Endpoint, dummySecretKey) {
		t.Fatalf("endpoint leaked dummy secret: %s", req.Endpoint)
	}

	body, err := marshalSubmitOrderBody("100009", order)
	if err != nil {
		t.Fatalf("marshalSubmitOrderBody returned error: %v", err)
	}
	rebuilt, err := client.BuildPrivateRequest(PrivateActionSubmitOrder, "post", PrivateOrderPlacePath, nil, body)
	if err != nil {
		t.Fatalf("BuildPrivateRequest returned error: %v", err)
	}
	if req.Signature != rebuilt.Signature || req.Query.Encode() != rebuilt.Query.Encode() || !reflect.DeepEqual(req.Body, rebuilt.Body) {
		t.Fatalf("private request is not deterministic:\nfirst=%#v\nsecond=%#v", req, rebuilt)
	}
}

func TestPrivateClientParsesAccountBalances(t *testing.T) {
	transport := &fakePrivateTransport{
		responses: []fakePrivateResult{
			{response: privateJSONResponse(200, `{
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
			}`)},
		},
	}
	client := newTestPrivateClient(t, transport)

	account, err := client.QueryAccount(context.Background(), "100009")
	if err != nil {
		t.Fatalf("QueryAccount returned error: %v", err)
	}
	balances := account.Balances()
	if balances["USDT"].Available.String() != "100.5" || balances["USDT"].Locked.String() != "2" {
		t.Fatalf("USDT balance = %#v, want 100.5 available and 2 locked", balances["USDT"])
	}
	if balances["BTC"].Available.String() != "0.1" || balances["BTC"].Locked.Sign() != 0 {
		t.Fatalf("BTC balance = %#v, want 0.1 available and zero locked", balances["BTC"])
	}

	req := transport.requests[0]
	if req.Method != "GET" || req.Path != "/v1/account/accounts/100009/balance" || len(req.Body) != 0 {
		t.Fatalf("account request = %s %s body=%q", req.Method, req.Path, string(req.Body))
	}
	assertPrivateAuthQuery(t, req.Query)
}

func TestPrivateClientOrderLifecycleUsesInjectedTransport(t *testing.T) {
	transport := &fakePrivateTransport{
		responses: []fakePrivateResult{
			{response: privateJSONResponse(200, `{"status":"ok","data":"59379"}`)},
			{response: privateJSONResponse(200, `{"status":"ok","data":"59379"}`)},
			{response: privateJSONResponse(200, string(readPrivateFixture(t, "testdata/order_filled.json")))},
			{response: privateJSONResponse(200, string(readPrivateFixture(t, "testdata/order_match_results.json")))},
		},
	}
	client := newTestPrivateClient(t, transport)
	ctx := context.Background()

	submitAck, err := client.SubmitOrder(ctx, "100009", types.SubmitOrder{
		Symbol:   "BTCUSDT",
		Side:     types.SideTypeSell,
		Type:     types.OrderTypeLimit,
		Quantity: fixedpoint.MustNewFromString("0.05"),
		Price:    fixedpoint.MustNewFromString("67810"),
	})
	if err != nil {
		t.Fatalf("SubmitOrder returned error: %v", err)
	}
	if submitAck.OrderID != "59379" {
		t.Fatalf("submit ack = %q, want 59379", submitAck.OrderID)
	}

	cancelAck, err := client.CancelOrder(ctx, "59379")
	if err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}
	if cancelAck.OrderID != "59379" {
		t.Fatalf("cancel ack = %q, want 59379", cancelAck.OrderID)
	}

	order, err := client.QueryOrder(ctx, "59379")
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if order.OrderID != 59379 || order.Status != types.OrderStatusFilled || order.Symbol != "BTCUSDT" {
		t.Fatalf("order = %#v, want filled BTCUSDT order 59379", order)
	}

	trades, err := client.QueryOrderTrades(ctx, "59379")
	if err != nil {
		t.Fatalf("QueryOrderTrades returned error: %v", err)
	}
	if len(trades) != 2 || trades[0].OrderID != 59379 || trades[1].ID != 99002 {
		t.Fatalf("trades = %#v, want two fixture matches for order 59379", trades)
	}

	expectedPaths := []string{
		PrivateOrderPlacePath,
		"/v1/order/orders/59379/submitcancel",
		"/v1/order/orders/59379",
		"/v1/order/orders/59379/matchresults",
	}
	if len(transport.requests) != len(expectedPaths) {
		t.Fatalf("transport requests = %d, want %d", len(transport.requests), len(expectedPaths))
	}
	for i, expected := range expectedPaths {
		if transport.requests[i].Path != expected {
			t.Fatalf("request %d path = %s, want %s", i, transport.requests[i].Path, expected)
		}
	}
}

func TestPrivateRetryClassificationAndBoundedPolicy(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		err        error
		expected   PrivateRetryClass
	}{
		{name: "transport error", err: errors.New("temporary dial failure"), expected: PrivateRetryTransient},
		{name: "rate limited status", statusCode: 429, expected: PrivateRetryRateLimited},
		{name: "server error", statusCode: 503, expected: PrivateRetryTransient},
		{name: "client error", statusCode: 400, body: `{"status":"error","err-code":"bad-request"}`, expected: PrivateRetryNone},
		{name: "HTX rate body", statusCode: 200, body: `{"status":"error","err-code":"api-ratelimit","err-msg":"too many requests"}`, expected: PrivateRetryRateLimited},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPrivateRetry(tc.statusCode, []byte(tc.body), tc.err)
			if got != tc.expected {
				t.Fatalf("ClassifyPrivateRetry = %s, want %s", got, tc.expected)
			}
		})
	}

	t.Run("retries until success", func(t *testing.T) {
		transport := &fakePrivateTransport{
			responses: []fakePrivateResult{
				{response: privateJSONResponse(429, `{"status":"error","err-code":"api-ratelimit"}`)},
				{response: privateJSONResponse(503, `temporarily unavailable`)},
				{response: privateJSONResponse(200, string(readPrivateFixture(t, "testdata/order_filled.json")))},
			},
		}
		client := newTestPrivateClient(t, transport, WithPrivateRetryPolicy(PrivateRetryPolicy{MaxAttempts: 3}))

		order, err := client.QueryOrder(context.Background(), "59379")
		if err != nil {
			t.Fatalf("QueryOrder returned error after retryable responses: %v", err)
		}
		if order.OrderID != 59379 {
			t.Fatalf("order id = %d, want 59379", order.OrderID)
		}
		if len(transport.requests) != 3 {
			t.Fatalf("transport attempts = %d, want 3", len(transport.requests))
		}
	})

	t.Run("attempts are capped", func(t *testing.T) {
		transport := &fakePrivateTransport{}
		for i := 0; i < 10; i++ {
			transport.responses = append(transport.responses, fakePrivateResult{response: privateJSONResponse(503, `temporarily unavailable`)})
		}
		client := newTestPrivateClient(t, transport, WithPrivateRetryPolicy(PrivateRetryPolicy{MaxAttempts: 99}))

		_, err := client.QueryOrder(context.Background(), "59379")
		if err == nil || !strings.Contains(err.Error(), "HTTP 503") {
			t.Fatalf("QueryOrder error = %v, want final HTTP 503", err)
		}
		if len(transport.requests) != MaxPrivateRetryAttempts {
			t.Fatalf("transport attempts = %d, want capped %d", len(transport.requests), MaxPrivateRetryAttempts)
		}
	})
}

func TestPrivateClientFailsClosed(t *testing.T) {
	t.Run("transport absent", func(t *testing.T) {
		_, err := NewPrivateClient(DefaultRESTBaseURL, PrivateCredentials{AccessKeyID: dummyAccessKey, SecretKey: dummySecretKey}, nil)
		if err == nil || !strings.Contains(err.Error(), "transport is absent") {
			t.Fatalf("NewPrivateClient error = %v, want transport absent failure", err)
		}
	})

	t.Run("blank credentials", func(t *testing.T) {
		_, err := NewPrivateClient(DefaultRESTBaseURL, PrivateCredentials{}, &fakePrivateTransport{})
		if err == nil || !strings.Contains(err.Error(), "access key id is empty") {
			t.Fatalf("NewPrivateClient error = %v, want blank credential failure", err)
		}
	})

	t.Run("unsupported action and path", func(t *testing.T) {
		transport := &fakePrivateTransport{}
		client := newTestPrivateClient(t, transport)
		_, err := client.BuildPrivateRequest(PrivateAction("unsupported"), "POST", "/v1/private/unsupported", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "unsupported HTX private action/path") {
			t.Fatalf("BuildPrivateRequest error = %v, want unsupported action/path failure", err)
		}
		if len(transport.requests) != 0 {
			t.Fatalf("transport requests = %d, want 0 for unsupported action", len(transport.requests))
		}
	})

	t.Run("non-2xx", func(t *testing.T) {
		transport := &fakePrivateTransport{
			responses: []fakePrivateResult{
				{response: privateJSONResponse(400, `{"status":"error","err-code":"bad-request"}`)},
			},
		}
		client := newTestPrivateClient(t, transport)
		_, err := client.QueryOrder(context.Background(), "59379")
		if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
			t.Fatalf("QueryOrder error = %v, want HTTP 400 failure", err)
		}
	})

	t.Run("oversized response", func(t *testing.T) {
		transport := &fakePrivateTransport{
			responses: []fakePrivateResult{
				{response: privateJSONResponse(200, `{"status":"ok","data":`+strings.Repeat(" ", 64)+`{}}`)},
			},
		}
		client := newTestPrivateClient(t, transport, WithPrivateMaxResponseBytes(32))
		_, err := client.QueryOrder(context.Background(), "59379")
		if err == nil || !strings.Contains(err.Error(), "response exceeds 32 byte limit") {
			t.Fatalf("QueryOrder error = %v, want response size failure", err)
		}
	})

	t.Run("HTX error status", func(t *testing.T) {
		transport := &fakePrivateTransport{
			responses: []fakePrivateResult{
				{response: privateJSONResponse(200, `{"status":"error","err-code":"base-record-invalid","err-msg":"invalid order"}`)},
			},
		}
		client := newTestPrivateClient(t, transport)
		_, err := client.QueryOrder(context.Background(), "59379")
		if err == nil || !strings.Contains(err.Error(), `code "base-record-invalid"`) {
			t.Fatalf("QueryOrder error = %v, want HTX error status failure", err)
		}
	})

	t.Run("transport error redacts credentials", func(t *testing.T) {
		transport := &fakePrivateTransport{
			responses: []fakePrivateResult{
				{err: errors.New("failed with " + dummyAccessKey + " and " + dummySecretKey)},
			},
		}
		client := newTestPrivateClient(t, transport)
		_, err := client.QueryOrder(context.Background(), "59379")
		if err == nil {
			t.Fatal("expected transport error")
		}
		if strings.Contains(err.Error(), dummyAccessKey) || strings.Contains(err.Error(), dummySecretKey) {
			t.Fatalf("private error leaked dummy credentials: %s", err)
		}
	})
}

func TestParseAccountBalanceResponseRejectsUnsupportedEntries(t *testing.T) {
	_, err := ParseAccountBalanceResponse(strings.NewReader(`{
		"status":"ok",
		"data":{"list":[{"currency":"usdt","type":"loan","balance":"1"}]}
	}`))
	if err == nil || !strings.Contains(err.Error(), "unsupported HTX account balance type") {
		t.Fatalf("ParseAccountBalanceResponse error = %v, want unsupported balance type failure", err)
	}
}

type fakePrivateResult struct {
	response PrivateResponse
	err      error
}

type fakePrivateTransport struct {
	requests  []PrivateRequest
	responses []fakePrivateResult
}

func (f *fakePrivateTransport) RoundTrip(ctx context.Context, req PrivateRequest) (PrivateResponse, error) {
	if err := ctx.Err(); err != nil {
		return PrivateResponse{}, err
	}
	f.requests = append(f.requests, req.clone())
	if len(f.responses) == 0 {
		return PrivateResponse{}, errors.New("unexpected HTX private request")
	}
	result := f.responses[0]
	f.responses = f.responses[1:]
	return result.response, result.err
}

func newTestPrivateClient(t *testing.T, transport PrivateTransport, options ...PrivateClientOption) *PrivateClient {
	t.Helper()

	allOptions := []PrivateClientOption{
		WithPrivateClock(func() time.Time {
			return time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC)
		}),
	}
	allOptions = append(allOptions, options...)
	client, err := NewPrivateClient(DefaultRESTBaseURL, PrivateCredentials{AccessKeyID: dummyAccessKey, SecretKey: dummySecretKey}, transport, allOptions...)
	if err != nil {
		t.Fatalf("NewPrivateClient returned error: %v", err)
	}
	return client
}

func privateJSONResponse(statusCode int, body string) PrivateResponse {
	return PrivateResponse{StatusCode: statusCode, Body: []byte(body)}
}

func readPrivateFixture(t *testing.T, path string) []byte {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertPrivateAuthQuery(t *testing.T, values url.Values) {
	t.Helper()

	expected := map[string]string{
		"AccessKeyId":      dummyAccessKey,
		"SignatureMethod":  signatureMethod,
		"SignatureVersion": signatureVersion,
		"Timestamp":        "2026-07-26T01:02:03",
	}
	for key, value := range expected {
		if got := values.Get(key); got != value {
			t.Fatalf("auth query %s = %q, want %q", key, got, value)
		}
	}
	if values.Get(signatureKey) == "" {
		t.Fatalf("auth query missing %s: %s", signatureKey, values.Encode())
	}
	if strings.Contains(values.Encode(), dummySecretKey) {
		t.Fatalf("auth query leaked dummy secret: %s", values.Encode())
	}
}
