package htx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

type KLinesResponse struct {
	Status  string       `json:"status"`
	Channel string       `json:"ch"`
	Data    []KLineEntry `json:"data"`
}

type KLineEntry struct {
	ID         int64            `json:"id"`
	Open       fixedpoint.Value `json:"open"`
	Close      fixedpoint.Value `json:"close"`
	Low        fixedpoint.Value `json:"low"`
	High       fixedpoint.Value `json:"high"`
	Amount     fixedpoint.Value `json:"amount"`
	Volume     fixedpoint.Value `json:"vol"`
	TradeCount uint64           `json:"count"`
}

type TickerResponse struct {
	Status    string      `json:"status"`
	Channel   string      `json:"ch"`
	Timestamp int64       `json:"ts"`
	Tick      TickerEntry `json:"tick"`
}

type TickerEntry struct {
	ID         int64              `json:"id"`
	Open       fixedpoint.Value   `json:"open"`
	Close      fixedpoint.Value   `json:"close"`
	Low        fixedpoint.Value   `json:"low"`
	High       fixedpoint.Value   `json:"high"`
	Amount     fixedpoint.Value   `json:"amount"`
	Volume     fixedpoint.Value   `json:"vol"`
	TradeCount uint64             `json:"count"`
	Bid        []fixedpoint.Value `json:"bid"`
	Ask        []fixedpoint.Value `json:"ask"`
}

type ReplayMarketData struct {
	markets types.MarketMap
	klines  map[string]map[types.Interval][]types.KLine
	tickers map[string]types.Ticker
}

func NewReplayMarketData(cfg Config, markets types.MarketMap) (*ReplayMarketData, error) {
	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.PublicMarketData.Enabled {
		return nil, fmt.Errorf("HTX replay market data is fixture-only: public market data must be disabled")
	}
	if cfg.Mode != ModePaper && cfg.Mode != ModeReplay {
		return nil, fmt.Errorf("unsupported HTX replay mode %q", cfg.Mode)
	}
	if len(markets) == 0 {
		return nil, fmt.Errorf("HTX replay market data requires at least one fixture market")
	}

	return &ReplayMarketData{
		markets: cloneMarketMap(markets),
		klines:  make(map[string]map[types.Interval][]types.KLine),
		tickers: make(map[string]types.Ticker),
	}, nil
}

func ParseKLines(r io.Reader, symbol string, interval types.Interval) ([]types.KLine, error) {
	var resp KLinesResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Status != "" && resp.Status != "ok" {
		return nil, fmt.Errorf("HTX klines response status %q", resp.Status)
	}

	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("HTX klines symbol is empty")
	}
	if interval == "" {
		return nil, fmt.Errorf("HTX klines interval is empty")
	}

	klines := make([]types.KLine, 0, len(resp.Data))
	for _, entry := range resp.Data {
		kline, err := entry.KLine(symbol, interval)
		if err != nil {
			return nil, err
		}
		klines = append(klines, kline)
	}

	sort.SliceStable(klines, func(i, j int) bool {
		return klines[i].StartTime.Before(klines[j].StartTime.Time())
	})

	return klines, nil
}

func (e KLineEntry) KLine(symbol string, interval types.Interval) (types.KLine, error) {
	if e.ID <= 0 {
		return types.KLine{}, fmt.Errorf("HTX kline %q has invalid id %d", symbol, e.ID)
	}
	if interval == "" {
		return types.KLine{}, fmt.Errorf("HTX kline %q interval is empty", symbol)
	}

	start := time.Unix(e.ID, 0).UTC()
	end := start.Add(interval.Duration()).Add(-time.Millisecond)

	return types.KLine{
		Exchange:       types.ExchangeName(CanonicalExchange),
		Symbol:         NormalizeSymbol(symbol),
		StartTime:      types.Time(start),
		EndTime:        types.Time(end),
		Interval:       interval,
		Open:           e.Open,
		Close:          e.Close,
		High:           e.High,
		Low:            e.Low,
		Volume:         e.Amount,
		QuoteVolume:    e.Volume,
		NumberOfTrades: e.TradeCount,
		Closed:         true,
	}, nil
}

func ParseTicker(r io.Reader, symbol string) (types.Ticker, error) {
	var resp TickerResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return types.Ticker{}, err
	}
	if resp.Status != "" && resp.Status != "ok" {
		return types.Ticker{}, fmt.Errorf("HTX ticker response status %q", resp.Status)
	}
	return resp.Tick.Ticker(resp.Timestamp, NormalizeSymbol(symbol))
}

func (e TickerEntry) Ticker(timestampMS int64, symbol string) (types.Ticker, error) {
	if symbol == "" {
		return types.Ticker{}, fmt.Errorf("HTX ticker symbol is empty")
	}
	if len(e.Bid) == 0 || len(e.Ask) == 0 {
		return types.Ticker{}, fmt.Errorf("HTX ticker %q requires bid and ask prices", symbol)
	}
	if timestampMS <= 0 && e.ID <= 0 {
		return types.Ticker{}, fmt.Errorf("HTX ticker %q has no timestamp", symbol)
	}

	timestamp := time.Unix(e.ID, 0).UTC()
	if timestampMS > 0 {
		timestamp = time.Unix(0, timestampMS*int64(time.Millisecond)).UTC()
	}

	return types.Ticker{
		Time:   timestamp,
		Volume: e.Amount,
		Last:   e.Close,
		Open:   e.Open,
		High:   e.High,
		Low:    e.Low,
		Buy:    e.Bid[0],
		Sell:   e.Ask[0],
	}, nil
}

func (r *ReplayMarketData) AddKLines(symbol string, interval types.Interval, klines []types.KLine) error {
	if r == nil {
		return fmt.Errorf("HTX replay market data is nil")
	}
	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return fmt.Errorf("HTX replay kline symbol is empty")
	}
	if interval == "" {
		return fmt.Errorf("HTX replay kline interval is empty")
	}
	if _, ok := r.markets[symbol]; !ok {
		return fmt.Errorf("HTX replay market %q is not configured", symbol)
	}

	copied := make([]types.KLine, len(klines))
	copy(copied, klines)
	sort.SliceStable(copied, func(i, j int) bool {
		return copied[i].StartTime.Before(copied[j].StartTime.Time())
	})

	if r.klines[symbol] == nil {
		r.klines[symbol] = make(map[types.Interval][]types.KLine)
	}
	r.klines[symbol][interval] = copied
	return nil
}

func (r *ReplayMarketData) SetTicker(symbol string, ticker types.Ticker) error {
	if r == nil {
		return fmt.Errorf("HTX replay market data is nil")
	}
	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return fmt.Errorf("HTX replay ticker symbol is empty")
	}
	if _, ok := r.markets[symbol]; !ok {
		return fmt.Errorf("HTX replay market %q is not configured", symbol)
	}

	r.tickers[symbol] = ticker
	return nil
}

func (r *ReplayMarketData) QueryMarkets(context.Context) (types.MarketMap, error) {
	if r == nil {
		return nil, fmt.Errorf("HTX replay market data is nil")
	}
	return cloneMarketMap(r.markets), nil
}

func (r *ReplayMarketData) QueryKLines(ctx context.Context, symbol string, interval types.Interval, options types.KLineQueryOptions) ([]types.KLine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("HTX replay market data is nil")
	}

	symbol = NormalizeSymbol(symbol)
	byInterval, ok := r.klines[symbol]
	if !ok {
		return nil, fmt.Errorf("HTX replay klines for market %q are not loaded", symbol)
	}
	klines, ok := byInterval[interval]
	if !ok {
		return nil, fmt.Errorf("HTX replay klines for market %q interval %q are not loaded", symbol, interval)
	}

	filtered := make([]types.KLine, 0, len(klines))
	for _, kline := range klines {
		if options.StartTime != nil && kline.StartTime.Before(*options.StartTime) {
			continue
		}
		if options.EndTime != nil && kline.StartTime.After(*options.EndTime) {
			continue
		}
		filtered = append(filtered, kline)
	}

	if options.Limit > 0 && len(filtered) > options.Limit {
		filtered = filtered[:options.Limit]
	}

	return filtered, nil
}

func (r *ReplayMarketData) QueryTicker(ctx context.Context, symbol string) (*types.Ticker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("HTX replay market data is nil")
	}

	symbol = NormalizeSymbol(symbol)
	ticker, ok := r.tickers[symbol]
	if !ok {
		return nil, fmt.Errorf("HTX replay ticker for market %q is not loaded", symbol)
	}
	return &ticker, nil
}

func (r *ReplayMarketData) QueryTickers(ctx context.Context, symbols ...string) (map[string]types.Ticker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("HTX replay market data is nil")
	}

	if len(symbols) == 0 {
		out := make(map[string]types.Ticker, len(r.tickers))
		for symbol, ticker := range r.tickers {
			out[symbol] = ticker
		}
		return out, nil
	}

	out := make(map[string]types.Ticker, len(symbols))
	for _, symbol := range symbols {
		normalized := NormalizeSymbol(symbol)
		ticker, ok := r.tickers[normalized]
		if !ok {
			return nil, fmt.Errorf("HTX replay ticker for market %q is not loaded", normalized)
		}
		out[normalized] = ticker
	}
	return out, nil
}

func LoadReplayMarketData(cfg Config, markets types.MarketMap, klineFixtures map[string]map[types.Interval]io.Reader, tickerFixtures map[string]io.Reader) (*ReplayMarketData, error) {
	replay, err := NewReplayMarketData(cfg, markets)
	if err != nil {
		return nil, err
	}

	for symbol, byInterval := range klineFixtures {
		for interval, reader := range byInterval {
			klines, err := ParseKLines(reader, symbol, interval)
			if err != nil {
				return nil, err
			}
			if err := replay.AddKLines(symbol, interval, klines); err != nil {
				return nil, err
			}
		}
	}

	for symbol, reader := range tickerFixtures {
		ticker, err := ParseTicker(reader, symbol)
		if err != nil {
			return nil, err
		}
		if err := replay.SetTicker(symbol, ticker); err != nil {
			return nil, err
		}
	}

	return replay, nil
}

func cloneMarketMap(markets types.MarketMap) types.MarketMap {
	cloned := types.MarketMap{}
	for symbol, market := range markets {
		cloned[strings.ToUpper(symbol)] = market
	}
	return cloned
}
