package htx

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

type SymbolsResponse struct {
	Status string       `json:"status"`
	Data   []SymbolSpec `json:"data"`
}

type SymbolSpec struct {
	Symbol          string           `json:"symbol"`
	State           string           `json:"state"`
	BaseCurrency    string           `json:"base-currency"`
	QuoteCurrency   string           `json:"quote-currency"`
	PricePrecision  int              `json:"price-precision"`
	AmountPrecision int              `json:"amount-precision"`
	ValuePrecision  int              `json:"value-precision"`
	MinOrderAmount  fixedpoint.Value `json:"min-order-amt"`
	MinOrderValue   fixedpoint.Value `json:"min-order-value"`
}

func ParseSymbols(r io.Reader) ([]SymbolSpec, error) {
	var resp SymbolsResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Status != "" && resp.Status != "ok" {
		return nil, fmt.Errorf("HTX symbols response status %q", resp.Status)
	}
	return resp.Data, nil
}

func OnlineMarkets(specs []SymbolSpec) (types.MarketMap, error) {
	markets := types.MarketMap{}
	for _, spec := range specs {
		if !strings.EqualFold(spec.State, "online") {
			continue
		}
		market, err := spec.Market()
		if err != nil {
			return nil, err
		}
		markets.Add(market)
	}
	return markets, nil
}

func (s SymbolSpec) Market() (types.Market, error) {
	base := strings.ToUpper(strings.TrimSpace(s.BaseCurrency))
	quote := strings.ToUpper(strings.TrimSpace(s.QuoteCurrency))
	if base == "" || quote == "" {
		return types.Market{}, fmt.Errorf("HTX symbol %q has empty base or quote currency", s.Symbol)
	}

	symbol := NormalizeSymbol(base + quote)
	localSymbol := strings.ToLower(strings.TrimSpace(s.Symbol))
	if localSymbol == "" {
		localSymbol = strings.ToLower(base + quote)
	}

	if s.PricePrecision < 0 || s.AmountPrecision < 0 {
		return types.Market{}, fmt.Errorf("HTX symbol %q has negative precision", s.Symbol)
	}

	return types.Market{
		Exchange:        types.ExchangeName(CanonicalExchange),
		Symbol:          symbol,
		LocalSymbol:     localSymbol,
		PricePrecision:  s.PricePrecision,
		VolumePrecision: s.AmountPrecision,
		QuoteCurrency:   quote,
		BaseCurrency:    base,
		MinNotional:     s.MinOrderValue,
		MinQuantity:     s.MinOrderAmount,
		StepSize:        precisionUnit(s.AmountPrecision),
		TickSize:        precisionUnit(s.PricePrecision),
	}, nil
}

func precisionUnit(precision int) fixedpoint.Value {
	if precision <= 0 {
		return fixedpoint.One
	}
	return fixedpoint.MustNewFromString("0." + strings.Repeat("0", precision-1) + "1")
}
