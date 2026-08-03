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

const (
	PaperLifecycleEvidenceVersion = 1

	MaxPaperLifecycleEvidenceJSONLRecordBytes = 1 << 20
)

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
	EntryFees         []PaperEvidenceCurrencyAmount `json:"entry_fees"`
	ExitFees          []PaperEvidenceCurrencyAmount `json:"exit_fees"`
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
	ID               uint64 `json:"id"`
	Symbol           string `json:"symbol"`
	EntryOrderID     uint64 `json:"entry_order_id"`
	ExitOrderID      uint64 `json:"exit_order_id"`
	Quantity         string `json:"quantity"`
	EntryNotional    string `json:"entry_notional"`
	EntryFee         string `json:"entry_fee"`
	EntryFeeCurrency string `json:"entry_fee_currency"`
	ExitNotional     string `json:"exit_notional"`
	GrossPnL         string `json:"gross_pnl"`
	ExitFee          string `json:"exit_fee"`
	ExitFeeCurrency  string `json:"exit_fee_currency"`
	NetPnL           string `json:"net_pnl"`
	ClosedAt         string `json:"closed_at"`
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

type PaperLifecycleEvidenceJSONLSink interface {
	io.Writer
	Len() int
	Truncate(n int)
}

func AppendPaperLifecycleEvidenceJSONL(sink PaperLifecycleEvidenceJSONLSink, session *PaperLifecycleSession) error {
	if sink == nil {
		return fmt.Errorf("HTX paper lifecycle evidence sink is nil")
	}

	evidence, err := ExportPaperLifecycleEvidence(session)
	if err != nil {
		return err
	}
	line, err := marshalPaperLifecycleEvidenceJSONLRecord(evidence)
	if err != nil {
		return err
	}
	return appendPaperLifecycleEvidenceJSONLRecord(sink, line)
}

func marshalPaperLifecycleEvidenceJSONLRecord(evidence PaperLifecycleEvidence) ([]byte, error) {
	line, err := MarshalPaperLifecycleEvidenceJSON(evidence)
	if err != nil {
		return nil, err
	}
	if len(line)+1 > MaxPaperLifecycleEvidenceJSONLRecordBytes {
		return nil, fmt.Errorf("HTX paper lifecycle evidence JSONL record exceeds %d bytes", MaxPaperLifecycleEvidenceJSONLRecordBytes)
	}
	return append(line, '\n'), nil
}

func appendPaperLifecycleEvidenceJSONLRecord(sink PaperLifecycleEvidenceJSONLSink, line []byte) error {
	start := sink.Len()
	n, err := sink.Write(line)
	if err != nil {
		sink.Truncate(start)
		return err
	}
	if n != len(line) {
		sink.Truncate(start)
		return io.ErrShortWrite
	}
	return nil
}

func ReadPaperLifecycleEvidenceJSONL(r io.Reader) ([]PaperLifecycleEvidence, error) {
	if r == nil {
		return nil, fmt.Errorf("HTX paper lifecycle evidence reader is nil")
	}

	reader := bufio.NewReader(r)
	records := make([]PaperLifecycleEvidence, 0)
	for {
		line, err := readPaperLifecycleEvidenceJSONLRecord(reader, MaxPaperLifecycleEvidenceJSONLRecordBytes)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		evidence, err := decodePaperLifecycleEvidenceRecord(line)
		if err != nil {
			return nil, err
		}
		records = append(records, evidence)
	}
	return records, nil
}

func readPaperLifecycleEvidenceJSONLRecord(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			line = append(line, chunk...)
			if len(line) > maxBytes {
				return nil, fmt.Errorf("HTX paper lifecycle evidence JSONL record exceeds %d bytes", maxBytes)
			}
		}
		switch err {
		case nil:
			return line, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		default:
			return nil, err
		}
	}
}

func decodePaperLifecycleEvidenceRecord(line []byte) (PaperLifecycleEvidence, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()

	var evidence PaperLifecycleEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return PaperLifecycleEvidence{}, err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return PaperLifecycleEvidence{}, fmt.Errorf("HTX paper lifecycle evidence JSONL record contains multiple JSON values")
		}
		return PaperLifecycleEvidence{}, err
	}
	if err := validatePaperLifecycleEvidence(evidence); err != nil {
		return PaperLifecycleEvidence{}, err
	}
	return evidence, nil
}

func validatePaperLifecycleEvidence(evidence PaperLifecycleEvidence) error {
	if evidence.Version != PaperLifecycleEvidenceVersion {
		return fmt.Errorf("HTX paper lifecycle evidence version %d is unsupported", evidence.Version)
	}
	if strings.TrimSpace(evidence.Exchange) != CanonicalExchange {
		return fmt.Errorf("HTX paper lifecycle evidence exchange %q is unsupported", evidence.Exchange)
	}
	switch Mode(strings.TrimSpace(evidence.Mode)) {
	case ModePaper, ModeReplay:
	default:
		return fmt.Errorf("HTX paper lifecycle evidence mode %q is unsupported", evidence.Mode)
	}
	if err := validatePaperEvidenceTime("exported_at", evidence.ExportedAt); err != nil {
		return err
	}
	if err := validatePaperEvidenceAmount("fee_rate", evidence.FeeRate); err != nil {
		return err
	}
	if evidence.Summary.OrderCount != len(evidence.Orders) {
		return fmt.Errorf("HTX paper lifecycle evidence summary order_count %d does not match %d orders", evidence.Summary.OrderCount, len(evidence.Orders))
	}
	if evidence.Summary.TradeCount != len(evidence.Trades) {
		return fmt.Errorf("HTX paper lifecycle evidence summary trade_count %d does not match %d trades", evidence.Summary.TradeCount, len(evidence.Trades))
	}
	if evidence.Summary.LedgerEntryCount != len(evidence.Ledger) {
		return fmt.Errorf("HTX paper lifecycle evidence summary ledger_entry_count %d does not match %d ledger entries", evidence.Summary.LedgerEntryCount, len(evidence.Ledger))
	}
	if evidence.Summary.ClosedTradeCount != len(evidence.ClosedTrades) {
		return fmt.Errorf("HTX paper lifecycle evidence summary closed_trade_count %d does not match %d closed trades", evidence.Summary.ClosedTradeCount, len(evidence.ClosedTrades))
	}

	if err := validatePaperEvidenceCurrencyAmounts("summary.gross_turnover", evidence.Summary.GrossTurnover); err != nil {
		return err
	}
	if err := validatePaperEvidenceCurrencyAmounts("summary.gross_buy_turnover", evidence.Summary.GrossBuyTurnover); err != nil {
		return err
	}
	if err := validatePaperEvidenceCurrencyAmounts("summary.gross_sell_turnover", evidence.Summary.GrossSellTurnover); err != nil {
		return err
	}
	if err := validatePaperEvidenceCurrencyAmounts("summary.fees", evidence.Summary.Fees); err != nil {
		return err
	}
	if err := validatePaperEvidenceCurrencyAmounts("summary.entry_fees", evidence.Summary.EntryFees); err != nil {
		return err
	}
	if err := validatePaperEvidenceCurrencyAmounts("summary.exit_fees", evidence.Summary.ExitFees); err != nil {
		return err
	}
	if err := validatePaperEvidenceCurrencyAmounts("summary.gross_closed_pnl", evidence.Summary.GrossClosedPnL); err != nil {
		return err
	}
	if err := validatePaperEvidenceCurrencyAmounts("summary.net_closed_pnl", evidence.Summary.NetClosedPnL); err != nil {
		return err
	}

	if len(evidence.Orders) == 0 {
		return fmt.Errorf("HTX paper lifecycle evidence requires at least one order")
	}
	if len(evidence.ClosedTrades) == 0 {
		return fmt.Errorf("HTX paper lifecycle evidence requires at least one closed trade")
	}

	if err := validatePaperEvidenceBalances("balances", evidence.Balances); err != nil {
		return err
	}
	for i, order := range evidence.Orders {
		if order.OrderID == 0 {
			return fmt.Errorf("HTX paper lifecycle evidence orders[%d].order_id is required", i)
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("orders[%d].client_order_id", i), order.ClientOrderID); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("orders[%d].symbol", i), order.Symbol); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("orders[%d].side", i), order.Side); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("orders[%d].type", i), order.Type); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("orders[%d].status", i), order.Status); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("orders[%d].original_status", i), order.OriginalStatus); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("orders[%d].price", i), order.Price); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("orders[%d].quantity", i), order.Quantity); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("orders[%d].executed_quantity", i), order.ExecutedQuantity); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("orders[%d].gross_notional", i), order.GrossNotional); err != nil {
			return err
		}
		if err := validatePaperEvidenceTime(fmt.Sprintf("orders[%d].creation_time", i), order.CreationTime); err != nil {
			return err
		}
		if err := validatePaperEvidenceTime(fmt.Sprintf("orders[%d].update_time", i), order.UpdateTime); err != nil {
			return err
		}
	}
	for i, trade := range evidence.Trades {
		if trade.TradeID == 0 {
			return fmt.Errorf("HTX paper lifecycle evidence trades[%d].trade_id is required", i)
		}
		if trade.OrderID == 0 {
			return fmt.Errorf("HTX paper lifecycle evidence trades[%d].order_id is required", i)
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("trades[%d].symbol", i), trade.Symbol); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("trades[%d].side", i), trade.Side); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("trades[%d].price", i), trade.Price); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("trades[%d].quantity", i), trade.Quantity); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("trades[%d].gross_notional", i), trade.GrossNotional); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("trades[%d].fee", i), trade.Fee); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("trades[%d].fee_currency", i), trade.FeeCurrency); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("trades[%d].net_base_change", i), trade.NetBaseChange); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("trades[%d].net_quote_change", i), trade.NetQuoteChange); err != nil {
			return err
		}
		if err := validatePaperEvidenceTime(fmt.Sprintf("trades[%d].time", i), trade.Time); err != nil {
			return err
		}
	}
	for i, entry := range evidence.Ledger {
		expected := uint64(i + 1)
		if entry.Sequence != expected {
			return fmt.Errorf("HTX paper lifecycle evidence ledger[%d].sequence = %d, want %d", i, entry.Sequence, expected)
		}
		if entry.OrderID == 0 {
			return fmt.Errorf("HTX paper lifecycle evidence ledger[%d].order_id is required", i)
		}
		if err := validatePaperEvidenceTime(fmt.Sprintf("ledger[%d].time", i), entry.Time); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("ledger[%d].kind", i), entry.Kind); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("ledger[%d].currency", i), entry.Currency); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("ledger[%d].available_delta", i), entry.AvailableDelta); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("ledger[%d].locked_delta", i), entry.LockedDelta); err != nil {
			return err
		}
		if err := validatePaperEvidenceBalance(fmt.Sprintf("ledger[%d].balance", i), entry.Balance); err != nil {
			return err
		}
	}
	for i, closed := range evidence.ClosedTrades {
		if closed.ID == 0 {
			return fmt.Errorf("HTX paper lifecycle evidence closed_trades[%d].id is required", i)
		}
		if closed.EntryOrderID == 0 {
			return fmt.Errorf("HTX paper lifecycle evidence closed_trades[%d].entry_order_id is required", i)
		}
		if closed.ExitOrderID == 0 {
			return fmt.Errorf("HTX paper lifecycle evidence closed_trades[%d].exit_order_id is required", i)
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("closed_trades[%d].symbol", i), closed.Symbol); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("closed_trades[%d].quantity", i), closed.Quantity); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("closed_trades[%d].entry_notional", i), closed.EntryNotional); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("closed_trades[%d].entry_fee", i), closed.EntryFee); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("closed_trades[%d].entry_fee_currency", i), closed.EntryFeeCurrency); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("closed_trades[%d].exit_notional", i), closed.ExitNotional); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("closed_trades[%d].gross_pnl", i), closed.GrossPnL); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("closed_trades[%d].exit_fee", i), closed.ExitFee); err != nil {
			return err
		}
		if err := validatePaperEvidenceNonEmpty(fmt.Sprintf("closed_trades[%d].exit_fee_currency", i), closed.ExitFeeCurrency); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(fmt.Sprintf("closed_trades[%d].net_pnl", i), closed.NetPnL); err != nil {
			return err
		}
		if err := validatePaperEvidenceTime(fmt.Sprintf("closed_trades[%d].closed_at", i), closed.ClosedAt); err != nil {
			return err
		}
	}
	if err := validatePaperEvidenceBalances("reconciliation.starting", evidence.Reconciliation.Starting); err != nil {
		return err
	}
	if err := validatePaperEvidenceBalances("reconciliation.expected", evidence.Reconciliation.Expected); err != nil {
		return err
	}
	if err := validatePaperEvidenceBalances("reconciliation.actual", evidence.Reconciliation.Actual); err != nil {
		return err
	}
	if len(evidence.Reconciliation.Drift) != 0 {
		return fmt.Errorf("HTX paper lifecycle evidence reconciliation drift must be empty")
	}
	return nil
}

func validatePaperEvidenceBalances(field string, balances []PaperEvidenceBalance) error {
	for i, balance := range balances {
		if err := validatePaperEvidenceBalance(fmt.Sprintf("%s[%d]", field, i), balance); err != nil {
			return err
		}
	}
	return nil
}

func validatePaperEvidenceBalance(field string, balance PaperEvidenceBalance) error {
	if err := validatePaperEvidenceNonEmpty(field+".currency", balance.Currency); err != nil {
		return err
	}
	if err := validatePaperEvidenceAmount(field+".available", balance.Available); err != nil {
		return err
	}
	if err := validatePaperEvidenceAmount(field+".locked", balance.Locked); err != nil {
		return err
	}
	if err := validatePaperEvidenceAmount(field+".total", balance.Total); err != nil {
		return err
	}
	return nil
}

func validatePaperEvidenceCurrencyAmounts(field string, amounts []PaperEvidenceCurrencyAmount) error {
	for i, amount := range amounts {
		prefix := fmt.Sprintf("%s[%d]", field, i)
		if err := validatePaperEvidenceNonEmpty(prefix+".currency", amount.Currency); err != nil {
			return err
		}
		if err := validatePaperEvidenceAmount(prefix+".amount", amount.Amount); err != nil {
			return err
		}
	}
	return nil
}

func validatePaperEvidenceTime(field string, value string) error {
	if err := validatePaperEvidenceNonEmpty(field, value); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("HTX paper lifecycle evidence %s is invalid: %w", field, err)
	}
	return nil
}

func validatePaperEvidenceAmount(field string, value string) error {
	if err := validatePaperEvidenceNonEmpty(field, value); err != nil {
		return err
	}
	if _, err := fixedpoint.NewFromString(strings.TrimSpace(value)); err != nil {
		return fmt.Errorf("HTX paper lifecycle evidence %s is invalid: %w", field, err)
	}
	return nil
}

func validatePaperEvidenceNonEmpty(field string, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("HTX paper lifecycle evidence %s is required", field)
	}
	return nil
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
			ID:               trade.ID,
			Symbol:           NormalizeSymbol(trade.Symbol),
			EntryOrderID:     trade.EntryOrderID,
			ExitOrderID:      trade.ExitOrderID,
			Quantity:         trade.Quantity.String(),
			EntryNotional:    trade.EntryNotional.String(),
			EntryFee:         trade.EntryFee.String(),
			EntryFeeCurrency: strings.ToUpper(strings.TrimSpace(trade.EntryFeeCurrency)),
			ExitNotional:     trade.ExitNotional.String(),
			GrossPnL:         grossPnL.String(),
			ExitFee:          trade.ExitFee.String(),
			ExitFeeCurrency:  strings.ToUpper(strings.TrimSpace(trade.ExitFeeCurrency)),
			NetPnL:           trade.RealizedPnL.String(),
			ClosedAt:         formatPaperEvidenceTime(trade.ClosedAt),
		})
	}
	return out
}

func paperEvidenceSummary(orders []PaperEvidenceOrder, trades []PaperEvidenceTrade, ledger []PaperEvidenceLedgerEntry, closedTrades []PaperEvidenceClosedTrade, markets types.MarketMap) PaperLifecycleEvidenceSummary {
	grossTurnover := make(map[string]fixedpoint.Value)
	grossBuyTurnover := make(map[string]fixedpoint.Value)
	grossSellTurnover := make(map[string]fixedpoint.Value)
	fees := make(map[string]fixedpoint.Value)
	entryFees := make(map[string]fixedpoint.Value)
	exitFees := make(map[string]fixedpoint.Value)
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
		entryFeeCurrency := strings.ToUpper(strings.TrimSpace(closed.EntryFeeCurrency))
		exitFeeCurrency := strings.ToUpper(strings.TrimSpace(closed.ExitFeeCurrency))
		if entryFeeCurrency != "" {
			entryFees[entryFeeCurrency] = entryFees[entryFeeCurrency].Add(fixedpoint.MustNewFromString(closed.EntryFee))
		}
		if exitFeeCurrency != "" {
			exitFees[exitFeeCurrency] = exitFees[exitFeeCurrency].Add(fixedpoint.MustNewFromString(closed.ExitFee))
		}
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
		EntryFees:         sortedPaperEvidenceCurrencyAmounts(entryFees),
		ExitFees:          sortedPaperEvidenceCurrencyAmounts(exitFees),
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
