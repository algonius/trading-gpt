package htx

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

const (
	DefaultHTTPTimeout             = 10 * time.Second
	DefaultMaxResponseBytes        = int64(1 << 20)
	DefaultSymbolsMaxResponseBytes = int64(2 << 20)
)

type PublicClient struct {
	baseURL                 *url.URL
	paths                   PublicMarketConfig
	httpClient              *http.Client
	maxResponseBytes        int64
	maxSymbolsResponseBytes int64
}

type PublicClientOption func(*PublicClient)

func WithHTTPClient(httpClient *http.Client) PublicClientOption {
	return func(c *PublicClient) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

func WithHTTPTransport(transport http.RoundTripper) PublicClientOption {
	return func(c *PublicClient) {
		if transport != nil {
			c.httpClient.Transport = transport
		}
	}
}

func WithHTTPTimeout(timeout time.Duration) PublicClientOption {
	return func(c *PublicClient) {
		if timeout > 0 {
			c.httpClient.Timeout = timeout
		}
	}
}

func WithMaxResponseBytes(limit int64) PublicClientOption {
	return func(c *PublicClient) {
		if limit > 0 {
			c.maxResponseBytes = limit
			c.maxSymbolsResponseBytes = limit
		}
	}
}

func WithMaxSymbolsResponseBytes(limit int64) PublicClientOption {
	return func(c *PublicClient) {
		if limit > 0 {
			c.maxSymbolsResponseBytes = limit
		}
	}
}

func NewPublicClient(cfg Config, options ...PublicClientOption) (*PublicClient, error) {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if !cfg.PublicMarketData.Enabled {
		return nil, fmt.Errorf("HTX public market data client requires public_market_data.enabled=true")
	}

	baseURL, err := url.Parse(cfg.PublicMarketData.RESTBaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid HTX public REST base URL: %w", err)
	}
	if baseURL.Scheme != "http" && baseURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid HTX public REST base URL scheme %q", baseURL.Scheme)
	}
	if baseURL.Host == "" {
		return nil, fmt.Errorf("invalid HTX public REST base URL host")
	}

	client := &PublicClient{
		baseURL: baseURL,
		paths:   cfg.PublicMarketData,
		httpClient: &http.Client{
			Timeout: DefaultHTTPTimeout,
		},
		maxResponseBytes:        DefaultMaxResponseBytes,
		maxSymbolsResponseBytes: DefaultSymbolsMaxResponseBytes,
	}

	for _, option := range options {
		option(client)
	}
	if client.httpClient.Timeout <= 0 {
		client.httpClient.Timeout = DefaultHTTPTimeout
	}
	client.httpClient.Jar = nil
	if client.maxResponseBytes <= 0 {
		client.maxResponseBytes = DefaultMaxResponseBytes
	}
	if client.maxSymbolsResponseBytes <= 0 {
		client.maxSymbolsResponseBytes = DefaultSymbolsMaxResponseBytes
	}

	return client, nil
}

func (c *PublicClient) QuerySymbolSpecs(ctx context.Context) ([]SymbolSpec, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	body, err := c.get(ctx, c.paths.SymbolsPath, nil, c.maxSymbolsResponseBytes)
	if err != nil {
		return nil, err
	}
	return ParseSymbols(bytes.NewReader(body))
}

func (c *PublicClient) QueryMarkets(ctx context.Context) (types.MarketMap, error) {
	specs, err := c.QuerySymbolSpecs(ctx)
	if err != nil {
		return nil, err
	}
	return OnlineMarkets(specs)
}

func (c *PublicClient) QueryKLines(ctx context.Context, symbol string, interval types.Interval, options types.KLineQueryOptions) ([]types.KLine, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("HTX public kline symbol is empty")
	}
	if interval == "" {
		return nil, fmt.Errorf("HTX public kline interval is empty")
	}

	query := url.Values{}
	query.Set("symbol", strings.ToLower(symbol))
	query.Set("period", HTXPeriod(interval))
	if options.Limit > 0 {
		query.Set("size", strconv.Itoa(options.Limit))
	}
	if options.StartTime != nil {
		query.Set("from", strconv.FormatInt(options.StartTime.Unix(), 10))
	}
	if options.EndTime != nil {
		query.Set("to", strconv.FormatInt(options.EndTime.Unix(), 10))
	}

	body, err := c.get(ctx, c.paths.KLinesPath, query, c.maxResponseBytes)
	if err != nil {
		return nil, err
	}
	return ParseKLines(bytes.NewReader(body), symbol, interval)
}

func (c *PublicClient) QueryTicker(ctx context.Context, symbol string) (*types.Ticker, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("HTX public ticker symbol is empty")
	}

	query := url.Values{}
	query.Set("symbol", strings.ToLower(symbol))

	body, err := c.get(ctx, c.paths.TickerPath, query, c.maxResponseBytes)
	if err != nil {
		return nil, err
	}
	ticker, err := ParseTicker(bytes.NewReader(body), symbol)
	if err != nil {
		return nil, err
	}
	return &ticker, nil
}

func HTXPeriod(interval types.Interval) string {
	switch interval {
	case types.Interval1m:
		return "1min"
	case types.Interval5m:
		return "5min"
	case types.Interval15m:
		return "15min"
	case types.Interval30m:
		return "30min"
	case types.Interval1h:
		return "60min"
	case types.Interval4h:
		return "4hour"
	case types.Interval1d:
		return "1day"
	case types.Interval1w:
		return "1week"
	case types.Interval1mo:
		return "1mon"
	default:
		return interval.String()
	}
}

func (c *PublicClient) get(ctx context.Context, path string, query url.Values, limit int64) ([]byte, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	endpoint, err := c.endpoint(path, query)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := readBounded(resp.Body, limit)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("HTX public GET %s returned HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
}

func (c *PublicClient) ready() error {
	if c == nil {
		return fmt.Errorf("HTX public market data client is nil")
	}
	if c.baseURL == nil || c.httpClient == nil {
		return fmt.Errorf("HTX public market data client is not initialized")
	}
	return nil
}

func (c *PublicClient) endpoint(path string, query url.Values) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("HTX public endpoint path is empty")
	}

	u := *c.baseURL
	if strings.HasPrefix(path, "/") {
		u.Path = path
	} else {
		u.Path = strings.TrimRight(u.Path, "/") + "/" + path
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("HTX public response byte limit must be positive")
	}

	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("HTX public response exceeds %d byte limit", limit)
	}
	return body, nil
}
