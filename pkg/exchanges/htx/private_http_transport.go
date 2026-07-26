package htx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PrivateHTTPTransport struct {
	httpClient       *http.Client
	maxResponseBytes int64
}

type PrivateHTTPTransportOption func(*PrivateHTTPTransport)

func WithPrivateHTTPClient(httpClient *http.Client) PrivateHTTPTransportOption {
	return func(t *PrivateHTTPTransport) {
		if httpClient != nil {
			t.httpClient = httpClient
		}
	}
}

func WithPrivateHTTPRoundTripper(roundTripper http.RoundTripper) PrivateHTTPTransportOption {
	return func(t *PrivateHTTPTransport) {
		if roundTripper != nil {
			t.httpClient.Transport = roundTripper
		}
	}
}

func WithPrivateHTTPTimeout(timeout time.Duration) PrivateHTTPTransportOption {
	return func(t *PrivateHTTPTransport) {
		if timeout > 0 {
			t.httpClient.Timeout = timeout
		}
	}
}

func WithPrivateHTTPMaxResponseBytes(limit int64) PrivateHTTPTransportOption {
	return func(t *PrivateHTTPTransport) {
		if limit > 0 {
			t.maxResponseBytes = limit
		}
	}
}

func NewPrivateHTTPTransport(options ...PrivateHTTPTransportOption) (*PrivateHTTPTransport, error) {
	transport := &PrivateHTTPTransport{
		httpClient: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
		maxResponseBytes: DefaultPrivateMaxResponseBytes,
	}

	for _, option := range options {
		option(transport)
	}
	if transport.httpClient == nil {
		return nil, fmt.Errorf("HTX private HTTP transport client is nil")
	}
	if transport.httpClient.Timeout <= 0 {
		transport.httpClient.Timeout = DefaultHTTPTimeout
	}
	transport.httpClient.Jar = nil
	transport.httpClient.CheckRedirect = preservePrivateHTTPRedirectResponse
	if transport.maxResponseBytes <= 0 {
		transport.maxResponseBytes = DefaultPrivateMaxResponseBytes
	}
	return transport, nil
}

func (t *PrivateHTTPTransport) RoundTrip(ctx context.Context, privateReq PrivateRequest) (PrivateResponse, error) {
	if err := t.ready(); err != nil {
		return PrivateResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return PrivateResponse{}, err
	}

	method := strings.ToUpper(strings.TrimSpace(privateReq.Method))
	if method == "" {
		return PrivateResponse{}, fmt.Errorf("HTX private HTTP request method is empty")
	}

	endpoint := strings.TrimSpace(privateReq.Endpoint)
	if endpoint == "" {
		return PrivateResponse{}, fmt.Errorf("HTX private HTTP request endpoint is empty")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return PrivateResponse{}, fmt.Errorf("invalid HTX private HTTP request endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return PrivateResponse{}, fmt.Errorf("invalid HTX private HTTP request endpoint scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return PrivateResponse{}, fmt.Errorf("invalid HTX private HTTP request endpoint host")
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, parsed.String(), bytes.NewReader(privateReq.Body))
	if err != nil {
		return PrivateResponse{}, err
	}
	httpReq.Header = privateReq.Header.Clone()

	httpResp, err := t.httpClient.Do(httpReq)
	if err != nil {
		return PrivateResponse{}, err
	}
	defer httpResp.Body.Close()

	body, err := readPrivateHTTPBounded(httpResp.Body, t.maxResponseBytes)
	if err != nil {
		return PrivateResponse{}, err
	}
	return PrivateResponse{
		StatusCode: httpResp.StatusCode,
		Body:       body,
	}, nil
}

func (t *PrivateHTTPTransport) ready() error {
	if t == nil {
		return fmt.Errorf("HTX private HTTP transport is nil")
	}
	if t.httpClient == nil {
		return fmt.Errorf("HTX private HTTP transport client is nil")
	}
	if t.maxResponseBytes <= 0 {
		return fmt.Errorf("HTX private HTTP response byte limit must be positive")
	}
	return nil
}

func readPrivateHTTPBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("HTX private HTTP response byte limit must be positive")
	}

	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("HTX private HTTP response exceeds %d byte limit", limit)
	}
	return body, nil
}

func preservePrivateHTTPRedirectResponse(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}
