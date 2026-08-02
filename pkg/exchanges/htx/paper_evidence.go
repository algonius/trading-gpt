package htx

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

const PaperLifecycleEvidenceVersion = 1

type PaperLifecycleEvidence struct {
	Version        int                           `json:"version"`
	Exchange       string                        `json:"exchange"`
	Mode           string                        `json:"mode"`
	ExportedAt     string                        `json:"exported_at"`
	FeeRate        string                        `json:"fee_rate"`
	Summary        PaperLifecycleEvidenceSummary `json:"summary"`
	Balances       []PaperEvidenceBalance        `json:"balances"`
	Orders         []PaperEvidenceOrder          `json:"orders"`
	Trades         []PaperEvidenceTrade          `json:"trades"`
	Ledger         []PaperEvidenceLedgerEntry    `json:"ledger"`
	ClosedTrades   []PaperEvidenceClosedTrade    `json:"closed_trades"`
	Reconciliation PaperEvidenceReconciliation   `json:"reconciliation"`
}

type PaperLifecycleEvidenceSummary struct {
	OrderCount        int                           `json:"order_count"`
	TradeCount        int                           `json:"trade_count"`
	LedgerEntryCount  int                           `json:"ledger_entry_count"`
	ClosedTradeCount  int                           `json:"closed_trade_count"`
	GrossTurnover     []PaperEvidenceCurrencyAmount `json:"gross_turnover"`
	GrossBuyTurnover  []PaperEvidenceCurrencyAmount `json:"gross_buy_turnover"`
	GrossSellTurnover []PaperEvidenceCurrencyAmount `json:"gross_sell_turnover"`
	Fees              []PaperEvidenceCurrencyAmount `json:"fees"`
	GrossClosedPnL    []PaperEvidenceCurrencyAmount `json:"gross_closed_pnl"`
	NetClosedPnL      []PaperEvidenceCurrencyAmount `json:"net_closed_pnl"`
}

type PaperEvidenceCurrencyAmount struct {
	Currency string `json:"currency"`
	Amount   string `json:"amount"`
}

type PaperEvidenceBalance struct {
	Currency  string `json:"currency"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
	Total     string `json:"total"`
}

type PaperEvidenceOrder struct {
	OrderID          uint64 `json:"order_id"`
	ClientOrderID    string `json:"client_order_id"`
	Symbol           string `json:"symbol"`
	Side             string `json:"side"`
	Type             string `json:"type"`
	Status           string `json:"status"`
	OriginalStatus   string `json:"original_status"`
	Price            string `json:"price"`
	Quantity         string `json:"quantity"`
	ExecutedQuantity string `json:"executed_quantity"`
	GrossNotional    string `json:"gross_notional"`
	IsWorking        bool   `json:"is_working"`
	CreationTime     string `json:"creation_time"`
	UpdateTime       string `json:"update_time"`
}

type PaperEvidenceTrade struct {
	TradeID        uint64 `json:"trade_id"`
	OrderID        uint64 `json:"order_id"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	Price          string `json:"price"`
	Quantity       string `json:"quantity"`
	GrossNotional  string `json:"gross_notional"`
	Fee            string `json:"fee"`
	FeeCurrency    string `json:"fee_currency"`
	NetBaseChange  string `json:"net_base_change"`
	NetQuoteChange string `json:"net_quote_change"`
	Time           string `json:"time"`
}

type PaperEvidenceLedgerEntry struct {
	Sequence       uint64               `json:"sequence"`
	Time           string               `json:"time"`
	Kind           string               `json:"kind"`
	OrderID        uint64               `json:"order_id"`
	TradeID        uint64               `json:"trade_id"`
	Currency       string               `json:"currency"`
	AvailableDelta string               `json:"available_delta"`
	LockedDelta    string               `json:"locked_delta"`
	Balance        PaperEvidenceBalance `json:"balance"`
}

type PaperEvidenceClosedTrade struct {
	ID            uint64 `json:"id"`
	Symbol        string `json:"symbol"`
	EntryOrderID  uint64 `json:"entry_order_id"`
	ExitOrderID   uint64 `json:"exit_order_id"`
	Quantity      string `json:"quantity"`
	EntryNotional string `json:"entry_notional"`
	ExitNotional  string `json:"exit_notional"`
	GrossPnL      string `json:"gross_pnl"`
	ExitFee       string `json:"exit_fee"`
	FeeCurrency   string `json:"fee_currency"`
	NetPnL        string `json:"net_pnl"`
	ClosedAt      string `json:"closed_at"`
}

type PaperEvidenceReconciliation struct {
	Starting []PaperEvidenceBalance `json:"starting"`
	Expected []PaperEvidenceBalance `json:"expected"`
	Actual   []PaperEvidenceBalance `json:"actual"`
	Drift    []PaperEvidenceBalance `json:"drift"`
}

func ExportPaperLifecycleEvidence(session *PaperLifecycleSession) (PaperLifecycleEvidence, error) {
	if session == nil {
		return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence session is nil")
	}
	if session.cfg.Mode != ModePaper && session.cfg.Mode != ModeReplay {
		return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence requires paper/replay mode; got %q", session.cfg.Mode)
	}
	if session.cfg.AllowAuthenticated {
		return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence rejects authenticated config")
	}
	if len(session.orders) == 0 {
		return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence requires at least one order")
	}

	reconciliation, err := session.ReconcileLedger()
	if err != nil {
		return PaperLifecycleEvidence{}, err
	}
	if len(reconciliation.Drift) != 0 {
		return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence drift is non-zero")
	}

	balances := sortedPaperEvidenceBalances(session.balances)
	for _, balance := range balances {
		if balance.Locked != "0" {
			return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence balance %s still has locked funds", balance.Currency)
		}
	}

	orders := sortedPaperEvidenceOrders(session.orders)
	for _, order := range orders {
		if order.IsWorking || !types.OrderStatus(order.Status).Closed() {
			return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence order %d is incomplete", order.OrderID)
		}
	}
	if len(session.closed) == 0 {
		return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence requires at least one closed trade")
	}

	ledger, err := sortedPaperEvidenceLedger(session.ledger)
	if err != nil {
		return PaperLifecycleEvidence{}, err
	}
	trades := sortedPaperEvidenceTrades(session.trades, session.markets)
	closedTrades := sortedPaperEvidenceClosedTrades(session.closed)
	summary := paperEvidenceSummary(orders, trades, ledger, closedTrades, session.markets)

	return PaperLifecycleEvidence{
		Version:      PaperLifecycleEvidenceVersion,
		Exchange:     CanonicalExchange,
		Mode:         string(session.cfg.Mode),
		ExportedAt:   formatPaperEvidenceTime(session.now().UTC()),
		FeeRate:      session.feeRate.String(),
		Summary:      summary,
		Balances:     balances,
		Orders:       orders,
		Trades:       trades,
		Ledger:       ledger,
		ClosedTrades: closedTrades,
		Reconciliation: PaperEvidenceReconciliation{
			Starting: sortedPaperEvidenceBalances(reconciliation.Starting),
			Expected: sortedPaperEvidenceBalances(reconciliation.Expected),
			Actual:   sortedPaperEvidenceBalances(reconciliation.Actual),
			Drift:    sortedPaperEvidenceBalances(reconciliation.Drift),
		},
	}, nil
}

func MarshalPaperLifecycleEvidenceJSON(evidence PaperLifecycleEvidence) ([]byte, error) {
	return json.Marshal(evidence)
}

func AppendPaperLifecycleEvidenceJSONL(w io.Writer, session *PaperLifecycleSession) error {
	if w == nil {
		return fmt.Errorf("HTX paper lifecycle evidence writer is nil")
	}

	evidence, err := ExportPaperLifecycleEvidence(session)
	if err != nil {
		return err
	}
	line, err := MarshalPaperLifecycleEvidenceJSON(evidence)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	n, err := w.Write(line)
	if err != nil {
		return err
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return nil
}

func ReadPaperLifecycleEvidenceJSONL(r io.Reader) ([]PaperLifecycleEvidence, error) {
	if r == nil {
		return nil, fmt.Errorf("HTX paper lifecycle evidence reader is nil")
	}

	scanner := bufio.NewScanner(r)
	records := make([]PaperLifecycleEvidence, 0)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var evidence PaperLifecycleEvidence
		if err := json.Unmarshal(line, &evidence); err != nil {
			return nil, err
		}
		records = append(records, evidence)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func sortedPaperEvidenceBalances(balances types.BalanceMap) []PaperEvidenceBalance {
	currencies := make([]string, 0, len(balances))
	for currency := range balances {
		currencies = append(currencies, strings.ToUpper(strings.TrimSpace(currency)))
	}
	sort.Strings(currencies)

	out := make([]PaperEvidenceBalance, 0, len(currencies))
	for _, currency := range currencies {
		balance := balances[currency]
		if balance.Currency == "" {
			balance.Currency = currency
		}
		out = append(out, paperEvidenceBalance(balance))
	}
	return out
}

func sortedPaperEvidenceOrders(orders map[uint64]types.Order) []PaperEvidenceOrder {
	ids := make([]uint64, 0, len(orders))
	for id := range orders {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	out := make([]PaperEvidenceOrder, 0, len(ids))
	for _, id := range ids {
		order := orders[id]
		out = append(out, PaperEvidenceOrder{
			OrderID:          order.OrderID,
			ClientOrderID:    order.ClientOrderID,
			Symbol:           NormalizeSymbol(order.Symbol),
			Side:             string(order.Side),
			Type:             string(order.Type),
			Status:           string(order.Status),
			OriginalStatus:   order.OriginalStatus,
			Price:            order.Price.String(),
			Quantity:         order.Quantity.String(),
			ExecutedQuantity: order.ExecutedQuantity.String(),
			GrossNotional:    order.Price.Mul(order.Quantity).String(),
			IsWorking:        order.IsWorking,
			CreationTime:     formatPaperEvidenceTime(order.CreationTime.Time()),
			UpdateTime:       formatPaperEvidenceTime(order.UpdateTime.Time()),
		})
	}
	return out
}

func sortedPaperEvidenceTrades(tradesByOrder map[uint64][]types.Trade, markets types.MarketMap) []PaperEvidenceTrade {
	orderIDs := make([]uint64, 0, len(tradesByOrder))
	for orderID := range tradesByOrder {
		orderIDs = append(orderIDs, orderID)
	}
	sort.Slice(orderIDs, func(i, j int) bool { return orderIDs[i] < orderIDs[j] })

	out := make([]PaperEvidenceTrade, 0)
	for _, orderID := range orderIDs {
		trades := append([]types.Trade(nil), tradesByOrder[orderID]...)
		sort.Slice(trades, func(i, j int) bool {
			if trades[i].ID == trades[j].ID {
				return trades[i].OrderID < trades[j].OrderID
			}
			return trades[i].ID < trades[j].ID
		})
		for _, trade := range trades {
			out = append(out, paperEvidenceTrade(trade, markets))
		}
	}
	return out
}

func sortedPaperEvidenceLedger(entries []PaperLedgerEntry) ([]PaperEvidenceLedgerEntry, error) {
	copied := append([]PaperLedgerEntry(nil), entries...)
	sort.Slice(copied, func(i, j int) bool { return copied[i].Sequence < copied[j].Sequence })

	out := make([]PaperEvidenceLedgerEntry, 0, len(copied))
	for i, entry := range copied {
		expected := uint64(i + 1)
		if entry.Sequence != expected {
			return nil, fmt.Errorf("HTX paper lifecycle evidence ledger sequence %d is not contiguous at %d", entry.Sequence, expected)
		}
		out = append(out, PaperEvidenceLedgerEntry{
			Sequence:       entry.Sequence,
			Time:           formatPaperEvidenceTime(entry.Time),
			Kind:           entry.Kind,
			OrderID:        entry.OrderID,
			TradeID:        entry.TradeID,
			Currency:       strings.ToUpper(strings.TrimSpace(entry.Currency)),
			AvailableDelta: entry.AvailableDelta.String(),
			LockedDelta:    entry.LockedDelta.String(),
			Balance:        paperEvidenceBalance(entry.Balance),
		})
	}
	return out, nil
}

func sortedPaperEvidenceClosedTrades(closedTrades []PaperClosedTrade) []PaperEvidenceClosedTrade {
	copied := append([]PaperClosedTrade(nil), closedTrades...)
	sort.Slice(copied, func(i, j int) bool { return copied[i].ID < copied[j].ID })

	out := make([]PaperEvidenceClosedTrade, 0, len(copied))
	for _, trade := range copied {
		grossPnL := trade.ExitNotional.Sub(trade.EntryNotional)
		out = append(out, PaperEvidenceClosedTrade{
			ID:            trade.ID,
			Symbol:        NormalizeSymbol(trade.Symbol),
			EntryOrderID:  trade.EntryOrderID,
			ExitOrderID:   trade.ExitOrderID,
			Quantity:      trade.Quantity.String(),
			EntryNotional: trade.EntryNotional.String(),
			ExitNotional:  trade.ExitNotional.String(),
			GrossPnL:      grossPnL.String(),
			ExitFee:       trade.ExitFee.String(),
			FeeCurrency:   strings.ToUpper(strings.TrimSpace(trade.FeeCurrency)),
			NetPnL:        trade.RealizedPnL.String(),
			ClosedAt:      formatPaperEvidenceTime(trade.ClosedAt),
		})
	}
	return out
}

func paperEvidenceSummary(orders []PaperEvidenceOrder, trades []PaperEvidenceTrade, ledger []PaperEvidenceLedgerEntry, closedTrades []PaperEvidenceClosedTrade, markets types.MarketMap) PaperLifecycleEvidenceSummary {
	grossTurnover := make(map[string]fixedpoint.Value)
	grossBuyTurnover := make(map[string]fixedpoint.Value)
	grossSellTurnover := make(map[string]fixedpoint.Value)
	fees := make(map[string]fixedpoint.Value)
	grossClosedPnL := make(map[string]fixedpoint.Value)
	netClosedPnL := make(map[string]fixedpoint.Value)

	for _, trade := range trades {
		quoteCurrency := paperEvidenceQuoteCurrency(trade.Symbol, markets)
		gross := fixedpoint.MustNewFromString(trade.GrossNotional)
		grossTurnover[quoteCurrency] = grossTurnover[quoteCurrency].Add(gross)
		switch types.SideType(trade.Side) {
		case types.SideTypeBuy:
			grossBuyTurnover[quoteCurrency] = grossBuyTurnover[quoteCurrency].Add(gross)
		case types.SideTypeSell:
			grossSellTurnover[quoteCurrency] = grossSellTurnover[quoteCurrency].Add(gross)
		}
		feeCurrency := strings.ToUpper(strings.TrimSpace(trade.FeeCurrency))
		if feeCurrency != "" {
			fees[feeCurrency] = fees[feeCurrency].Add(fixedpoint.MustNewFromString(trade.Fee))
		}
	}

	for _, closed := range closedTrades {
		quoteCurrency := paperEvidenceQuoteCurrency(closed.Symbol, markets)
		grossClosedPnL[quoteCurrency] = grossClosedPnL[quoteCurrency].Add(fixedpoint.MustNewFromString(closed.GrossPnL))
		netClosedPnL[quoteCurrency] = netClosedPnL[quoteCurrency].Add(fixedpoint.MustNewFromString(closed.NetPnL))
	}

	return PaperLifecycleEvidenceSummary{
		OrderCount:        len(orders),
		TradeCount:        len(trades),
		LedgerEntryCount:  len(ledger),
		ClosedTradeCount:  len(closedTrades),
		GrossTurnover:     sortedPaperEvidenceCurrencyAmounts(grossTurnover),
		GrossBuyTurnover:  sortedPaperEvidenceCurrencyAmounts(grossBuyTurnover),
		GrossSellTurnover: sortedPaperEvidenceCurrencyAmounts(grossSellTurnover),
		Fees:              sortedPaperEvidenceCurrencyAmounts(fees),
		GrossClosedPnL:    sortedPaperEvidenceCurrencyAmounts(grossClosedPnL),
		NetClosedPnL:      sortedPaperEvidenceCurrencyAmounts(netClosedPnL),
	}
}

func sortedPaperEvidenceCurrencyAmounts(values map[string]fixedpoint.Value) []PaperEvidenceCurrencyAmount {
	currencies := make([]string, 0, len(values))
	for currency := range values {
		currencies = append(currencies, strings.ToUpper(strings.TrimSpace(currency)))
	}
	sort.Strings(currencies)

	out := make([]PaperEvidenceCurrencyAmount, 0, len(currencies))
	for _, currency := range currencies {
		out = append(out, PaperEvidenceCurrencyAmount{
			Currency: currency,
			Amount:   values[currency].String(),
		})
	}
	return out
}

func paperEvidenceTrade(trade types.Trade, markets types.MarketMap) PaperEvidenceTrade {
	market := markets[NormalizeSymbol(trade.Symbol)]
	baseDelta := trade.Quantity
	quoteDelta := trade.QuoteQuantity.Neg()
	if trade.Side == types.SideTypeSell {
		baseDelta = trade.Quantity.Neg()
		quoteDelta = trade.QuoteQuantity
	}

	feeCurrency := strings.ToUpper(strings.TrimSpace(trade.FeeCurrency))
	if feeCurrency == strings.ToUpper(strings.TrimSpace(market.BaseCurrency)) {
		baseDelta = baseDelta.Sub(trade.Fee)
	}
	if feeCurrency == strings.ToUpper(strings.TrimSpace(market.QuoteCurrency)) {
		quoteDelta = quoteDelta.Sub(trade.Fee)
	}

	return PaperEvidenceTrade{
		TradeID:        trade.ID,
		OrderID:        trade.OrderID,
		Symbol:         NormalizeSymbol(trade.Symbol),
		Side:           string(trade.Side),
		Price:          trade.Price.String(),
		Quantity:       trade.Quantity.String(),
		GrossNotional:  trade.QuoteQuantity.String(),
		Fee:            trade.Fee.String(),
		FeeCurrency:    feeCurrency,
		NetBaseChange:  baseDelta.String(),
		NetQuoteChange: quoteDelta.String(),
		Time:           formatPaperEvidenceTime(trade.Time.Time()),
	}
}

func paperEvidenceBalance(balance types.Balance) PaperEvidenceBalance {
	currency := strings.ToUpper(strings.TrimSpace(balance.Currency))
	return PaperEvidenceBalance{
		Currency:  currency,
		Available: balance.Available.String(),
		Locked:    balance.Locked.String(),
		Total:     balance.Total().String(),
	}
}

func paperEvidenceQuoteCurrency(symbol string, markets types.MarketMap) string {
	market := markets[NormalizeSymbol(symbol)]
	quote := strings.ToUpper(strings.TrimSpace(market.QuoteCurrency))
	if quote == "" {
		quote = "QUOTE"
	}
	return quote
}

func formatPaperEvidenceTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
