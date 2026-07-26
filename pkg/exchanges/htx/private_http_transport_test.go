package htx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrivateHTTPTransportPropagatesMethodBodyAndHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != PrivateOrderPlacePath || r.URL.Query().Get("fixture") != "yes" {
			t.Fatalf("URL = %s, want %s?fixture=yes", r.URL.String(), PrivateOrderPlacePath)
		}
		if got := r.Header.Get("X-HTX-Test"); got != "header-propagated" {
			t.Fatalf("X-HTX-Test = %q, want header-propagated", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization header = %q, want empty", got)
		}
		if got := r.Header.Get("Cookie"); got != "" {
			t.Fatalf("Cookie header = %q, want empty", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"fixture":"body"}` {
			t.Fatalf("body = %s, want fixture JSON body", string(body))
		}

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"ok","data":"accepted"}`))
	}))
	defer server.Close()

	transport := newTestPrivateHTTPTransport(t)
	resp, err := transport.RoundTrip(context.Background(), PrivateRequest{
		Method:   "post",
		Endpoint: server.URL + PrivateOrderPlacePath + "?fixture=yes",
		Header: http.Header{
			"Content-Type": {"application/json"},
			"X-HTX-Test":   {"header-propagated"},
		},
		Body: []byte(`{"fixture":"body"}`),
	})
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if resp.StatusCode != http.StatusAccepted || string(resp.Body) != `{"status":"ok","data":"accepted"}` {
		t.Fatalf("response = %d %s, want preserved 202 body", resp.StatusCode, string(resp.Body))
	}
}

func TestPrivateHTTPTransportClearsCookieJar(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "" {
			t.Fatalf("Cookie header = %q, want empty", got)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	httpClient := &http.Client{Jar: privateHTTPStaticCookieJar{}}
	transport, err := NewPrivateHTTPTransport(WithPrivateHTTPClient(httpClient))
	if err != nil {
		t.Fatalf("NewPrivateHTTPTransport returned error: %v", err)
	}
	if transport.httpClient.Jar != nil {
		t.Fatal("expected injected cookie jar to be cleared")
	}

	if _, err := transport.RoundTrip(context.Background(), PrivateRequest{Method: "GET", Endpoint: server.URL}); err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
}

func TestPrivateHTTPTransportContextCancellation(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	transport := newTestPrivateHTTPTransport(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := transport.RoundTrip(ctx, PrivateRequest{Method: "GET", Endpoint: server.URL})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RoundTrip error = %v, want context.Canceled", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("server hits = %d, want 0 for pre-canceled context", hits.Load())
	}
}

func TestPrivateHTTPTransportTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	transport := newTestPrivateHTTPTransport(t, WithPrivateHTTPTimeout(25*time.Millisecond))
	start := time.Now()
	_, err := transport.RoundTrip(context.Background(), PrivateRequest{Method: "GET", Endpoint: server.URL})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("RoundTrip error = %v, want timeout", err)
	}
	if elapsed := time.Since(start); elapsed >= 150*time.Millisecond {
		t.Fatalf("timeout elapsed = %s, want bounded by client timeout", elapsed)
	}
}

func TestPrivateHTTPTransportPreservesNon2xxResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`temporarily unavailable`))
	}))
	defer server.Close()

	transport := newTestPrivateHTTPTransport(t)
	resp, err := transport.RoundTrip(context.Background(), PrivateRequest{Method: "GET", Endpoint: server.URL})
	if err != nil {
		t.Fatalf("RoundTrip returned error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || string(resp.Body) != "temporarily unavailable" {
		t.Fatalf("response = %d %q, want preserved 503 body", resp.StatusCode, string(resp.Body))
	}
}

func TestPrivateHTTPTransportStreamsResponseSizeBound(t *testing.T) {
	body := &countingReadCloser{remaining: 1 << 20}
	transport := newTestPrivateHTTPTransport(
		t,
		WithPrivateHTTPRoundTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       body,
				Header:     make(http.Header),
			}, nil
		})),
		WithPrivateHTTPMaxResponseBytes(32),
	)

	_, err := transport.RoundTrip(context.Background(), PrivateRequest{Method: "GET", Endpoint: "https://fixture.invalid/private"})
	if err == nil || !strings.Contains(err.Error(), "response exceeds 32 byte limit") {
		t.Fatalf("RoundTrip error = %v, want streaming response size error", err)
	}
	if body.read != 33 {
		t.Fatalf("body read bytes = %d, want limit+1 streaming read", body.read)
	}
	if !body.closed {
		t.Fatal("response body was not closed")
	}
}

func TestPrivateHTTPTransportDoesNotBroadenMutationRetries(t *testing.T) {
	failures := []struct {
		name         string
		statusCode   int
		body         string
		transportErr error
		wantErr      string
	}{
		{
			name:         "transport error",
			transportErr: errors.New("temporary private HTTP failure"),
			wantErr:      "transport failed",
		},
		{
			name:       "HTTP 429",
			statusCode: http.StatusTooManyRequests,
			body:       `{"status":"error","err-code":"api-ratelimit"}`,
			wantErr:    "HTTP 429",
		},
		{
			name:       "HTTP 503",
			statusCode: http.StatusServiceUnavailable,
			body:       `temporarily unavailable`,
			wantErr:    "HTTP 503",
		},
	}
	for _, tc := range []struct {
		name string
		call func(context.Context, *PrivateClient) error
	}{
		{
			name: "submit order",
			call: func(ctx context.Context, client *PrivateClient) error {
				_, err := client.SubmitOrder(ctx, "100009", testPrivateSubmitOrder())
				return err
			},
		},
		{
			name: "cancel order",
			call: func(ctx context.Context, client *PrivateClient) error {
				_, err := client.CancelOrder(ctx, "59379")
				return err
			},
		},
	} {
		for _, failure := range failures {
			t.Run(tc.name+" "+failure.name, func(t *testing.T) {
				var hits atomic.Int32
				baseURL := "https://fixture.invalid"
				var cleanup func()
				transport := newTestPrivateHTTPTransport(t, WithPrivateHTTPRoundTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					hits.Add(1)
					return nil, failure.transportErr
				})))
				if failure.transportErr == nil {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						hits.Add(1)
						w.WriteHeader(failure.statusCode)
						_, _ = w.Write([]byte(failure.body))
					}))
					baseURL = server.URL
					cleanup = server.Close
					transport = newTestPrivateHTTPTransport(t)
				}
				if cleanup != nil {
					defer cleanup()
				}

				client, err := NewPrivateClient(
					baseURL,
					PrivateCredentials{AccessKeyID: dummyAccessKey, SecretKey: dummySecretKey},
					transport,
					WithPrivateClock(func() time.Time {
						return time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC)
					}),
					WithPrivateRetryPolicy(PrivateRetryPolicy{MaxAttempts: 3}),
				)
				if err != nil {
					t.Fatalf("NewPrivateClient returned error: %v", err)
				}

				err = tc.call(context.Background(), client)
				if err == nil || !strings.Contains(err.Error(), failure.wantErr) {
					t.Fatalf("%s error = %v, want %q without retry", tc.name, err, failure.wantErr)
				}
				if hits.Load() != 1 {
					t.Fatalf("%s %s HTTP attempts = %d, want exactly 1", tc.name, failure.name, hits.Load())
				}
			})
		}
	}
}

type privateHTTPStaticCookieJar struct{}

func (privateHTTPStaticCookieJar) SetCookies(*url.URL, []*http.Cookie) {}

func (privateHTTPStaticCookieJar) Cookies(*url.URL) []*http.Cookie {
	return []*http.Cookie{{Name: "session", Value: "must-not-send"}}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type countingReadCloser struct {
	remaining int64
	read      int64
	closed    bool
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > c.remaining {
		n = c.remaining
	}
	for i := range p[:n] {
		p[i] = 'x'
	}
	c.remaining -= n
	c.read += n
	return int(n), nil
}

func (c *countingReadCloser) Close() error {
	c.closed = true
	return nil
}

func newTestPrivateHTTPTransport(t *testing.T, options ...PrivateHTTPTransportOption) *PrivateHTTPTransport {
	t.Helper()

	transport, err := NewPrivateHTTPTransport(options...)
	if err != nil {
		t.Fatalf("NewPrivateHTTPTransport returned error: %v", err)
	}
	return transport
}
