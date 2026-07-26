package htx

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/c9s/bbgo/pkg/types"
)

type PrivateSession struct {
	cfg       Config
	accountID string
	client    *PrivateClient
}

type PrivateSessionOption func(*privateSessionOptions)

type privateSessionOptions struct {
	baseURL              string
	transport            PrivateTransport
	httpTransportOptions []PrivateHTTPTransportOption
	privateClientOptions []PrivateClientOption
}

func WithPrivateSessionBaseURL(baseURL string) PrivateSessionOption {
	return func(o *privateSessionOptions) {
		o.baseURL = strings.TrimSpace(baseURL)
	}
}

func WithPrivateSessionTransport(transport PrivateTransport) PrivateSessionOption {
	return func(o *privateSessionOptions) {
		if transport != nil {
			o.transport = transport
		}
	}
}

func WithPrivateSessionHTTPTransportOptions(options ...PrivateHTTPTransportOption) PrivateSessionOption {
	return func(o *privateSessionOptions) {
		o.httpTransportOptions = append(o.httpTransportOptions, options...)
	}
}

func WithPrivateSessionClientOptions(options ...PrivateClientOption) PrivateSessionOption {
	return func(o *privateSessionOptions) {
		o.privateClientOptions = append(o.privateClientOptions, options...)
	}
}

func NewPrivateSession(ctx context.Context, cfg Config, accountID string, credentials PrivateCredentials, options ...PrivateSessionOption) (*PrivateSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Mode != ModePaper {
		return nil, fmt.Errorf("HTX private session requires paper mode; got %q", cfg.Mode)
	}

	accountID, err = cleanPrivatePathID("account id", accountID)
	if err != nil {
		return nil, err
	}

	opts := privateSessionOptions{}
	for _, option := range options {
		option(&opts)
	}
	if opts.baseURL == "" {
		return nil, fmt.Errorf("HTX private session base URL is empty")
	}
	if err := validatePrivateSessionFixtureBaseURL(opts.baseURL); err != nil {
		return nil, err
	}

	transport := opts.transport
	if transport == nil {
		transport, err = NewPrivateHTTPTransport(opts.httpTransportOptions...)
		if err != nil {
			return nil, err
		}
	}

	client, err := NewPrivateClient(opts.baseURL, credentials, transport, opts.privateClientOptions...)
	if err != nil {
		return nil, err
	}

	return &PrivateSession{
		cfg:       cfg,
		accountID: accountID,
		client:    client,
	}, nil
}

func (s *PrivateSession) Config() Config {
	if s == nil {
		return Config{}
	}
	return s.cfg
}

func (s *PrivateSession) AccountID() string {
	if s == nil {
		return ""
	}
	return s.accountID
}

func (s *PrivateSession) QueryAccount(ctx context.Context) (*types.Account, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.client.QueryAccount(ctx, s.accountID)
}

func (s *PrivateSession) QueryAccountBalances(ctx context.Context) (types.BalanceMap, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.client.QueryAccountBalances(ctx, s.accountID)
}

func (s *PrivateSession) SubmitOrder(ctx context.Context, order types.SubmitOrder) (OrderAck, error) {
	if err := s.ready(); err != nil {
		return OrderAck{}, err
	}
	if err := ctx.Err(); err != nil {
		return OrderAck{}, err
	}
	return s.client.SubmitOrder(ctx, s.accountID, order)
}

func (s *PrivateSession) CancelOrder(ctx context.Context, orderID string) (OrderAck, error) {
	if err := s.ready(); err != nil {
		return OrderAck{}, err
	}
	if err := ctx.Err(); err != nil {
		return OrderAck{}, err
	}
	return s.client.CancelOrder(ctx, orderID)
}

func (s *PrivateSession) QueryOrder(ctx context.Context, orderID string) (types.Order, error) {
	if err := s.ready(); err != nil {
		return types.Order{}, err
	}
	if err := ctx.Err(); err != nil {
		return types.Order{}, err
	}
	return s.client.QueryOrder(ctx, orderID)
}

func (s *PrivateSession) QueryOrderTrades(ctx context.Context, orderID string) ([]types.Trade, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.client.QueryOrderTrades(ctx, orderID)
}

func (s *PrivateSession) ready() error {
	if s == nil {
		return fmt.Errorf("HTX private session is nil")
	}
	if s.accountID == "" {
		return fmt.Errorf("HTX private session account id is empty")
	}
	if s.client == nil {
		return fmt.Errorf("HTX private session client is not initialized")
	}
	return nil
}

func validatePrivateSessionFixtureBaseURL(baseURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return fmt.Errorf("invalid HTX private session fixture base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid HTX private session fixture base URL scheme %q", parsed.Scheme)
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "" {
		return fmt.Errorf("invalid HTX private session fixture base URL host")
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}

	return fmt.Errorf("HTX private session fixture base URL host %q is not loopback", host)
}
