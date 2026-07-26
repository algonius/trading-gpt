package htx

import (
	"context"
	"fmt"

	"github.com/c9s/bbgo/pkg/bbgo"
	"github.com/c9s/bbgo/pkg/types"
)

type MarketData interface {
	QueryMarkets(ctx context.Context) (types.MarketMap, error)
	QueryKLines(ctx context.Context, symbol string, interval types.Interval, options types.KLineQueryOptions) ([]types.KLine, error)
	QueryTicker(ctx context.Context, symbol string) (*types.Ticker, error)
}

type MarketDataSession struct {
	cfg    Config
	source MarketData
}

type MarketDataSessionOption func(*marketDataSessionOptions)

type marketDataSessionOptions struct {
	publicClientOptions []PublicClientOption
	replay              *ReplayMarketData
}

func WithPublicClientOptions(options ...PublicClientOption) MarketDataSessionOption {
	return func(o *marketDataSessionOptions) {
		o.publicClientOptions = append(o.publicClientOptions, options...)
	}
}

func WithReplayMarketData(replay *ReplayMarketData) MarketDataSessionOption {
	return func(o *marketDataSessionOptions) {
		o.replay = replay
	}
}

func NewMarketDataSession(ctx context.Context, cfg Config, options ...MarketDataSessionOption) (*MarketDataSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}

	opts := marketDataSessionOptions{}
	for _, option := range options {
		option(&opts)
	}

	var source MarketData
	switch cfg.Mode {
	case ModePaper:
		if !cfg.PublicMarketData.Enabled {
			return nil, fmt.Errorf("HTX paper market data session requires public_market_data.enabled=true")
		}
		client, err := NewPublicClient(cfg, opts.publicClientOptions...)
		if err != nil {
			return nil, err
		}
		source = client
	case ModeReplay:
		if cfg.PublicMarketData.Enabled {
			return nil, fmt.Errorf("HTX replay market data session requires public_market_data.enabled=false")
		}
		if opts.replay == nil {
			return nil, fmt.Errorf("HTX replay market data session requires replay market data")
		}
		source = opts.replay
	default:
		return nil, fmt.Errorf("unsupported HTX market data mode %q", cfg.Mode)
	}

	return &MarketDataSession{
		cfg:    cfg,
		source: source,
	}, nil
}

func (s *MarketDataSession) Config() Config {
	if s == nil {
		return Config{}
	}
	return s.cfg
}

func (s *MarketDataSession) QueryMarkets(ctx context.Context) (types.MarketMap, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.source.QueryMarkets(ctx)
}

func (s *MarketDataSession) QueryKLines(ctx context.Context, symbol string, interval types.Interval, options types.KLineQueryOptions) ([]types.KLine, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.source.QueryKLines(ctx, symbol, interval, options)
}

func (s *MarketDataSession) QueryTicker(ctx context.Context, symbol string) (*types.Ticker, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.source.QueryTicker(ctx, symbol)
}

func (s *MarketDataSession) NewMarketDataStore(ctx context.Context, symbol string, interval types.Interval, options types.KLineQueryOptions) (*bbgo.MarketDataStore, error) {
	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("HTX market data store symbol is empty")
	}

	store := bbgo.NewMarketDataStore(symbol)
	if err := s.SeedMarketDataStore(ctx, store, symbol, interval, options); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *MarketDataSession) SeedMarketDataStore(ctx context.Context, store *bbgo.MarketDataStore, symbol string, interval types.Interval, options types.KLineQueryOptions) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("HTX market data store is nil")
	}

	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return fmt.Errorf("HTX market data store symbol is empty")
	}
	if NormalizeSymbol(store.Symbol) != symbol {
		return fmt.Errorf("HTX market data store symbol %q does not match %q", store.Symbol, symbol)
	}
	if interval == "" {
		return fmt.Errorf("HTX market data store interval is empty")
	}

	klines, err := s.QueryKLines(ctx, symbol, interval, options)
	if err != nil {
		return err
	}
	for _, kline := range klines {
		store.AddKLine(kline)
	}
	return nil
}

func (s *MarketDataSession) ready() error {
	if s == nil {
		return fmt.Errorf("HTX market data session is nil")
	}
	if s.source == nil {
		return fmt.Errorf("HTX market data session is not initialized")
	}
	return nil
}
