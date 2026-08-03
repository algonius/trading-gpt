package htx

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

const (
	PaperLifecycleEvidenceRunRecordVersion = 1
	paperLifecycleEvidenceRunRecordType    = "htx-paper-lifecycle-run-evidence"
)

type PaperLifecycleEvidenceSource struct {
	Mode    string `json:"mode"`
	Source  string `json:"source"`
	Dataset string `json:"dataset"`
}

type PaperLifecycleEvidenceRunInput struct {
	Source  string `json:"source"`
	Dataset string `json:"dataset"`
	Run     string `json:"run"`
}

type PaperLifecycleRecordedRun struct {
	Version    int                            `json:"version"`
	RecordType string                         `json:"record_type"`
	Exchange   string                         `json:"exchange"`
	Mode       string                         `json:"mode"`
	Source     string                         `json:"source"`
	Dataset    string                         `json:"dataset"`
	RunID      string                         `json:"run_id"`
	RunInput   PaperLifecycleEvidenceRunInput `json:"run_input"`
	Evidence   PaperLifecycleEvidence         `json:"evidence"`
}

type PaperLifecycleCumulativeEvidence struct {
	Version  int                                     `json:"version"`
	Exchange string                                  `json:"exchange"`
	Mode     string                                  `json:"mode"`
	Source   string                                  `json:"source"`
	Dataset  string                                  `json:"dataset"`
	RunCount int                                     `json:"run_count"`
	Runs     []PaperLifecycleRecordedRun             `json:"runs"`
	Summary  PaperLifecycleCumulativeEvidenceSummary `json:"summary"`
}

type PaperLifecycleCumulativeEvidenceSummary struct {
	OrderCount          int                           `json:"order_count"`
	TradeCount          int                           `json:"trade_count"`
	LedgerEntryCount    int                           `json:"ledger_entry_count"`
	ClosedTradeCount    int                           `json:"closed_trade_count"`
	OpenTradeCount      int                           `json:"open_trade_count"`
	GrossTurnover       []PaperEvidenceCurrencyAmount `json:"gross_turnover"`
	GrossBuyTurnover    []PaperEvidenceCurrencyAmount `json:"gross_buy_turnover"`
	GrossSellTurnover   []PaperEvidenceCurrencyAmount `json:"gross_sell_turnover"`
	TotalFees           []PaperEvidenceCurrencyAmount `json:"total_fees"`
	EntryFees           []PaperEvidenceCurrencyAmount `json:"entry_fees"`
	ExitFees            []PaperEvidenceCurrencyAmount `json:"exit_fees"`
	GrossClosedPnL      []PaperEvidenceCurrencyAmount `json:"gross_closed_pnl"`
	NetClosedPnL        []PaperEvidenceCurrencyAmount `json:"net_closed_pnl"`
	ReconciliationDrift []PaperEvidenceBalance        `json:"reconciliation_drift"`
}

type PaperLifecycleEvidenceRecorder struct {
	path    string
	source  PaperLifecycleEvidenceSource
	lock    *sync.Mutex
	newSink func(*os.File) (PaperLifecycleEvidenceJSONLSink, error)
}

func NewPaperLifecycleEvidenceRecorder(path string, source PaperLifecycleEvidenceSource) (*PaperLifecycleEvidenceRecorder, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("HTX paper lifecycle evidence recorder path is empty")
	}
	canonical, err := canonicalPaperLifecycleEvidenceFilePath(path)
	if err != nil {
		return nil, err
	}
	source, err = normalizePaperLifecycleEvidenceSource(source)
	if err != nil {
		return nil, err
	}

	return &PaperLifecycleEvidenceRecorder{
		path:   canonical,
		source: source,
		lock:   paperLifecycleEvidenceFileStoreLock(canonical),
		newSink: func(file *os.File) (PaperLifecycleEvidenceJSONLSink, error) {
			return newPaperLifecycleEvidenceFileSink(file)
		},
	}, nil
}

func (r *PaperLifecycleEvidenceRecorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

func (r *PaperLifecycleEvidenceRecorder) Source() PaperLifecycleEvidenceSource {
	if r == nil {
		return PaperLifecycleEvidenceSource{}
	}
	return r.source
}

func (r *PaperLifecycleEvidenceRecorder) Record(session *PaperLifecycleSession, input PaperLifecycleEvidenceRunInput) (bool, error) {
	if r == nil {
		return false, fmt.Errorf("HTX paper lifecycle evidence recorder is nil")
	}

	r.lock.Lock()
	defer r.lock.Unlock()

	records, err := r.readRunsLocked()
	if err != nil {
		return false, err
	}
	evidence, err := ExportPaperLifecycleEvidence(session)
	if err != nil {
		return false, err
	}
	record, err := r.recordForEvidence(evidence, input)
	if err != nil {
		return false, err
	}
	line, err := marshalPaperLifecycleRecordedRunJSONLRecord(record)
	if err != nil {
		return false, err
	}
	for _, existing := range records {
		if existing.RunID != record.RunID {
			continue
		}
		equivalent, err := paperLifecycleRecordedRunsDuplicateEquivalent(existing, record)
		if err != nil {
			return false, err
		}
		if equivalent {
			return false, nil
		}
		return false, fmt.Errorf("HTX paper lifecycle evidence run %s already exists with different evidence", record.RunID)
	}

	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer file.Close()

	sink, err := r.newSink(file)
	if err != nil {
		return false, err
	}
	if err := appendPaperLifecycleEvidenceJSONLRecord(sink, line); err != nil {
		return false, err
	}
	if err := file.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *PaperLifecycleEvidenceRecorder) ReadCumulative() (PaperLifecycleCumulativeEvidence, error) {
	if r == nil {
		return PaperLifecycleCumulativeEvidence{}, fmt.Errorf("HTX paper lifecycle evidence recorder is nil")
	}

	r.lock.Lock()
	defer r.lock.Unlock()

	records, err := r.readRunsLocked()
	if err != nil {
		return PaperLifecycleCumulativeEvidence{}, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].RunID < records[j].RunID })
	summary, err := aggregatePaperLifecycleRecordedRuns(records)
	if err != nil {
		return PaperLifecycleCumulativeEvidence{}, err
	}
	return PaperLifecycleCumulativeEvidence{
		Version:  PaperLifecycleEvidenceRunRecordVersion,
		Exchange: CanonicalExchange,
		Mode:     r.source.Mode,
		Source:   r.source.Source,
		Dataset:  r.source.Dataset,
		RunCount: len(records),
		Runs:     records,
		Summary:  summary,
	}, nil
}

func (r *PaperLifecycleEvidenceRecorder) recordForEvidence(evidence PaperLifecycleEvidence, input PaperLifecycleEvidenceRunInput) (PaperLifecycleRecordedRun, error) {
	if evidence.Exchange != CanonicalExchange {
		return PaperLifecycleRecordedRun{}, fmt.Errorf("HTX paper lifecycle evidence recorder rejects exchange %q", evidence.Exchange)
	}
	if evidence.Mode != r.source.Mode {
		return PaperLifecycleRecordedRun{}, fmt.Errorf("HTX paper lifecycle evidence recorder mode %q does not match source mode %q", evidence.Mode, r.source.Mode)
	}
	input, err := normalizePaperLifecycleEvidenceRunInput(input, r.source)
	if err != nil {
		return PaperLifecycleRecordedRun{}, err
	}
	runID, err := paperLifecycleEvidenceRunID(input)
	if err != nil {
		return PaperLifecycleRecordedRun{}, err
	}
	return PaperLifecycleRecordedRun{
		Version:    PaperLifecycleEvidenceRunRecordVersion,
		RecordType: paperLifecycleEvidenceRunRecordType,
		Exchange:   CanonicalExchange,
		Mode:       r.source.Mode,
		Source:     r.source.Source,
		Dataset:    r.source.Dataset,
		RunID:      runID,
		RunInput:   input,
		Evidence:   evidence,
	}, nil
}

func (r *PaperLifecycleEvidenceRecorder) readRunsLocked() ([]PaperLifecycleRecordedRun, error) {
	file, err := os.Open(r.path)
	if os.IsNotExist(err) {
		return []PaperLifecycleRecordedRun{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if err := validatePaperLifecycleEvidenceFileBoundary(file); err != nil {
		return nil, err
	}

	records := make([]PaperLifecycleRecordedRun, 0)
	seen := make(map[string][]byte)
	reader := bufio.NewReader(file)
	for {
		line, err := readPaperLifecycleEvidenceJSONLRecord(reader, MaxPaperLifecycleEvidenceJSONLRecordBytes)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		record, err := decodePaperLifecycleRecordedRunJSONLRecord(line)
		if err != nil {
			return nil, err
		}
		if err := r.validateRecordScope(record); err != nil {
			return nil, err
		}
		canonical, err := marshalPaperLifecycleRecordedRunJSONLRecord(record)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(line, canonical) {
			return nil, fmt.Errorf("HTX paper lifecycle evidence recorder found non-canonical record %s", record.RunID)
		}
		equivalence, err := paperLifecycleRecordedRunDuplicateEquivalenceBytes(record)
		if err != nil {
			return nil, err
		}
		if existing, ok := seen[record.RunID]; ok {
			if bytes.Equal(existing, equivalence) {
				continue
			}
			return nil, fmt.Errorf("HTX paper lifecycle evidence recorder found conflicting duplicate run %s", record.RunID)
		}
		seen[record.RunID] = equivalence
		records = append(records, record)
	}
	return records, nil
}

func (r *PaperLifecycleEvidenceRecorder) validateRecordScope(record PaperLifecycleRecordedRun) error {
	if record.Exchange != CanonicalExchange {
		return fmt.Errorf("HTX paper lifecycle evidence recorder rejects exchange %q", record.Exchange)
	}
	if record.Mode != r.source.Mode {
		return fmt.Errorf("HTX paper lifecycle evidence recorder record mode %q does not match source mode %q", record.Mode, r.source.Mode)
	}
	if record.Source != r.source.Source || record.Dataset != r.source.Dataset {
		return fmt.Errorf("HTX paper lifecycle evidence recorder record source %q/%q does not match %q/%q", record.Source, record.Dataset, r.source.Source, r.source.Dataset)
	}
	return nil
}

func marshalPaperLifecycleRecordedRunJSONLRecord(record PaperLifecycleRecordedRun) ([]byte, error) {
	if err := validatePaperLifecycleRecordedRun(record); err != nil {
		return nil, err
	}
	line, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(line)+1 > MaxPaperLifecycleEvidenceJSONLRecordBytes {
		return nil, fmt.Errorf("HTX paper lifecycle evidence recorder JSONL record exceeds %d bytes", MaxPaperLifecycleEvidenceJSONLRecordBytes)
	}
	return append(line, '\n'), nil
}

func decodePaperLifecycleRecordedRunJSONLRecord(line []byte) (PaperLifecycleRecordedRun, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()

	var record PaperLifecycleRecordedRun
	if err := decoder.Decode(&record); err != nil {
		return PaperLifecycleRecordedRun{}, err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return PaperLifecycleRecordedRun{}, fmt.Errorf("HTX paper lifecycle evidence recorder JSONL record contains multiple JSON values")
		}
		return PaperLifecycleRecordedRun{}, err
	}
	if err := validatePaperLifecycleRecordedRun(record); err != nil {
		return PaperLifecycleRecordedRun{}, err
	}
	return record, nil
}

func paperLifecycleRecordedRunsDuplicateEquivalent(a PaperLifecycleRecordedRun, b PaperLifecycleRecordedRun) (bool, error) {
	aLine, err := paperLifecycleRecordedRunDuplicateEquivalenceBytes(a)
	if err != nil {
		return false, err
	}
	bLine, err := paperLifecycleRecordedRunDuplicateEquivalenceBytes(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(aLine, bLine), nil
}

func paperLifecycleRecordedRunDuplicateEquivalenceBytes(record PaperLifecycleRecordedRun) ([]byte, error) {
	if err := validatePaperLifecycleRecordedRun(record); err != nil {
		return nil, err
	}
	record.Evidence.ExportedAt = ""
	line, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(line)+1 > MaxPaperLifecycleEvidenceJSONLRecordBytes {
		return nil, fmt.Errorf("HTX paper lifecycle evidence recorder JSONL record exceeds %d bytes", MaxPaperLifecycleEvidenceJSONLRecordBytes)
	}
	return line, nil
}

func validatePaperLifecycleRecordedRun(record PaperLifecycleRecordedRun) error {
	if record.Version != PaperLifecycleEvidenceRunRecordVersion {
		return fmt.Errorf("HTX paper lifecycle evidence recorder version %d is unsupported", record.Version)
	}
	if record.RecordType != paperLifecycleEvidenceRunRecordType {
		return fmt.Errorf("HTX paper lifecycle evidence recorder record type %q is unsupported", record.RecordType)
	}
	if record.Exchange != CanonicalExchange {
		return fmt.Errorf("HTX paper lifecycle evidence recorder exchange %q is unsupported", record.Exchange)
	}
	source, err := normalizePaperLifecycleEvidenceSource(PaperLifecycleEvidenceSource{
		Mode:    record.Mode,
		Source:  record.Source,
		Dataset: record.Dataset,
	})
	if err != nil {
		return err
	}
	input, err := normalizePaperLifecycleEvidenceRunInput(record.RunInput, source)
	if err != nil {
		return err
	}
	if input != record.RunInput {
		return fmt.Errorf("HTX paper lifecycle evidence recorder run input is not canonical")
	}
	runID, err := paperLifecycleEvidenceRunID(input)
	if err != nil {
		return err
	}
	if record.RunID != runID {
		return fmt.Errorf("HTX paper lifecycle evidence recorder run_id %q does not match input", record.RunID)
	}
	if err := validatePaperLifecycleEvidence(record.Evidence); err != nil {
		return err
	}
	if record.Evidence.Exchange != record.Exchange || record.Evidence.Mode != record.Mode {
		return fmt.Errorf("HTX paper lifecycle evidence recorder evidence identity does not match record identity")
	}
	return nil
}

func normalizePaperLifecycleEvidenceSource(source PaperLifecycleEvidenceSource) (PaperLifecycleEvidenceSource, error) {
	source.Mode = strings.TrimSpace(source.Mode)
	source.Source = strings.TrimSpace(source.Source)
	source.Dataset = strings.TrimSpace(source.Dataset)
	switch Mode(source.Mode) {
	case ModePaper, ModeReplay:
	default:
		return PaperLifecycleEvidenceSource{}, fmt.Errorf("HTX paper lifecycle evidence recorder mode %q is unsupported", source.Mode)
	}
	if source.Source == "" {
		return PaperLifecycleEvidenceSource{}, fmt.Errorf("HTX paper lifecycle evidence recorder source is empty")
	}
	if source.Dataset == "" {
		return PaperLifecycleEvidenceSource{}, fmt.Errorf("HTX paper lifecycle evidence recorder dataset is empty")
	}
	return source, nil
}

func normalizePaperLifecycleEvidenceRunInput(input PaperLifecycleEvidenceRunInput, source PaperLifecycleEvidenceSource) (PaperLifecycleEvidenceRunInput, error) {
	input.Source = strings.TrimSpace(input.Source)
	input.Dataset = strings.TrimSpace(input.Dataset)
	input.Run = strings.TrimSpace(input.Run)
	if input.Source != source.Source || input.Dataset != source.Dataset {
		return PaperLifecycleEvidenceRunInput{}, fmt.Errorf("HTX paper lifecycle evidence recorder run source %q/%q does not match %q/%q", input.Source, input.Dataset, source.Source, source.Dataset)
	}
	if input.Run == "" {
		return PaperLifecycleEvidenceRunInput{}, fmt.Errorf("HTX paper lifecycle evidence recorder run is empty")
	}
	return input, nil
}

func paperLifecycleEvidenceRunID(input PaperLifecycleEvidenceRunInput) (string, error) {
	line, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(line)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func aggregatePaperLifecycleRecordedRuns(records []PaperLifecycleRecordedRun) (PaperLifecycleCumulativeEvidenceSummary, error) {
	grossTurnover := make(map[string]fixedpoint.Value)
	grossBuyTurnover := make(map[string]fixedpoint.Value)
	grossSellTurnover := make(map[string]fixedpoint.Value)
	totalFees := make(map[string]fixedpoint.Value)
	entryFees := make(map[string]fixedpoint.Value)
	exitFees := make(map[string]fixedpoint.Value)
	grossClosedPnL := make(map[string]fixedpoint.Value)
	netClosedPnL := make(map[string]fixedpoint.Value)
	driftAvailable := make(map[string]fixedpoint.Value)
	driftLocked := make(map[string]fixedpoint.Value)

	summary := PaperLifecycleCumulativeEvidenceSummary{}
	for _, record := range records {
		evidence := record.Evidence
		summary.OrderCount += len(evidence.Orders)
		summary.TradeCount += len(evidence.Trades)
		summary.LedgerEntryCount += len(evidence.Ledger)
		summary.ClosedTradeCount += len(evidence.ClosedTrades)
		summary.OpenTradeCount += paperLifecycleEvidenceOpenTradeCount(evidence)

		for _, trade := range evidence.Trades {
			quoteCurrency := paperLifecycleEvidenceQuoteCurrency(evidence, trade.Symbol)
			gross, err := paperEvidenceAmountValue("trade.gross_notional", trade.GrossNotional)
			if err != nil {
				return PaperLifecycleCumulativeEvidenceSummary{}, err
			}
			grossTurnover[quoteCurrency] = grossTurnover[quoteCurrency].Add(gross)
			switch types.SideType(trade.Side) {
			case types.SideTypeBuy:
				grossBuyTurnover[quoteCurrency] = grossBuyTurnover[quoteCurrency].Add(gross)
			case types.SideTypeSell:
				grossSellTurnover[quoteCurrency] = grossSellTurnover[quoteCurrency].Add(gross)
			}
			feeCurrency := strings.ToUpper(strings.TrimSpace(trade.FeeCurrency))
			if feeCurrency != "" {
				fee, err := paperEvidenceAmountValue("trade.fee", trade.Fee)
				if err != nil {
					return PaperLifecycleCumulativeEvidenceSummary{}, err
				}
				totalFees[feeCurrency] = totalFees[feeCurrency].Add(fee)
			}
		}
		for _, closed := range evidence.ClosedTrades {
			quoteCurrency := paperLifecycleEvidenceQuoteCurrency(evidence, closed.Symbol)
			entryFeeCurrency := strings.ToUpper(strings.TrimSpace(closed.EntryFeeCurrency))
			if entryFeeCurrency != "" {
				entryFee, err := paperEvidenceAmountValue("closed_trade.entry_fee", closed.EntryFee)
				if err != nil {
					return PaperLifecycleCumulativeEvidenceSummary{}, err
				}
				entryFees[entryFeeCurrency] = entryFees[entryFeeCurrency].Add(entryFee)
			}
			exitFeeCurrency := strings.ToUpper(strings.TrimSpace(closed.ExitFeeCurrency))
			if exitFeeCurrency != "" {
				exitFee, err := paperEvidenceAmountValue("closed_trade.exit_fee", closed.ExitFee)
				if err != nil {
					return PaperLifecycleCumulativeEvidenceSummary{}, err
				}
				exitFees[exitFeeCurrency] = exitFees[exitFeeCurrency].Add(exitFee)
			}
			gross, err := paperEvidenceAmountValue("closed_trade.gross_pnl", closed.GrossPnL)
			if err != nil {
				return PaperLifecycleCumulativeEvidenceSummary{}, err
			}
			net, err := paperEvidenceAmountValue("closed_trade.net_pnl", closed.NetPnL)
			if err != nil {
				return PaperLifecycleCumulativeEvidenceSummary{}, err
			}
			grossClosedPnL[quoteCurrency] = grossClosedPnL[quoteCurrency].Add(gross)
			netClosedPnL[quoteCurrency] = netClosedPnL[quoteCurrency].Add(net)
		}
		for _, drift := range evidence.Reconciliation.Drift {
			currency := strings.ToUpper(strings.TrimSpace(drift.Currency))
			if currency == "" {
				return PaperLifecycleCumulativeEvidenceSummary{}, fmt.Errorf("HTX paper lifecycle evidence recorder drift currency is empty")
			}
			available, err := paperEvidenceAmountValue("reconciliation.drift.available", drift.Available)
			if err != nil {
				return PaperLifecycleCumulativeEvidenceSummary{}, err
			}
			locked, err := paperEvidenceAmountValue("reconciliation.drift.locked", drift.Locked)
			if err != nil {
				return PaperLifecycleCumulativeEvidenceSummary{}, err
			}
			driftAvailable[currency] = driftAvailable[currency].Add(available)
			driftLocked[currency] = driftLocked[currency].Add(locked)
		}
	}

	summary.GrossTurnover = sortedPaperEvidenceCurrencyAmounts(grossTurnover)
	summary.GrossBuyTurnover = sortedPaperEvidenceCurrencyAmounts(grossBuyTurnover)
	summary.GrossSellTurnover = sortedPaperEvidenceCurrencyAmounts(grossSellTurnover)
	summary.TotalFees = sortedPaperEvidenceCurrencyAmounts(totalFees)
	summary.EntryFees = sortedPaperEvidenceCurrencyAmounts(entryFees)
	summary.ExitFees = sortedPaperEvidenceCurrencyAmounts(exitFees)
	summary.GrossClosedPnL = sortedPaperEvidenceCurrencyAmounts(grossClosedPnL)
	summary.NetClosedPnL = sortedPaperEvidenceCurrencyAmounts(netClosedPnL)
	summary.ReconciliationDrift = sortedPaperEvidenceDriftBalances(driftAvailable, driftLocked)
	return summary, nil
}

func paperLifecycleEvidenceOpenTradeCount(evidence PaperLifecycleEvidence) int {
	closedOrderIDs := make(map[uint64]struct{}, len(evidence.ClosedTrades)*2)
	for _, closed := range evidence.ClosedTrades {
		closedOrderIDs[closed.EntryOrderID] = struct{}{}
		closedOrderIDs[closed.ExitOrderID] = struct{}{}
	}
	open := 0
	for _, trade := range evidence.Trades {
		if _, ok := closedOrderIDs[trade.OrderID]; !ok {
			open++
		}
	}
	return open
}

func paperLifecycleEvidenceQuoteCurrency(evidence PaperLifecycleEvidence, symbol string) string {
	symbol = NormalizeSymbol(symbol)
	for _, closed := range evidence.ClosedTrades {
		if NormalizeSymbol(closed.Symbol) != symbol {
			continue
		}
		if quote := strings.ToUpper(strings.TrimSpace(closed.ExitFeeCurrency)); quote != "" {
			return quote
		}
		if quote := strings.ToUpper(strings.TrimSpace(closed.EntryFeeCurrency)); quote != "" {
			return quote
		}
	}
	for _, amount := range evidence.Summary.GrossTurnover {
		currency := strings.ToUpper(strings.TrimSpace(amount.Currency))
		if currency != "" && strings.HasSuffix(symbol, currency) {
			return currency
		}
	}
	return paperEvidenceQuoteCurrency(symbol, nil)
}

func paperEvidenceAmountValue(field string, value string) (fixedpoint.Value, error) {
	amount, err := fixedpoint.NewFromString(strings.TrimSpace(value))
	if err != nil {
		return fixedpoint.Zero, fmt.Errorf("HTX paper lifecycle evidence recorder %s is invalid: %w", field, err)
	}
	return amount, nil
}

func sortedPaperEvidenceDriftBalances(available map[string]fixedpoint.Value, locked map[string]fixedpoint.Value) []PaperEvidenceBalance {
	currencies := make(map[string]struct{}, len(available)+len(locked))
	for currency := range available {
		currencies[currency] = struct{}{}
	}
	for currency := range locked {
		currencies[currency] = struct{}{}
	}

	keys := make([]string, 0, len(currencies))
	for currency := range currencies {
		if available[currency].Sign() != 0 || locked[currency].Sign() != 0 {
			keys = append(keys, currency)
		}
	}
	sort.Strings(keys)

	out := make([]PaperEvidenceBalance, 0, len(keys))
	for _, currency := range keys {
		total := available[currency].Add(locked[currency])
		out = append(out, PaperEvidenceBalance{
			Currency:  currency,
			Available: available[currency].String(),
			Locked:    locked[currency].String(),
			Total:     total.String(),
		})
	}
	return out
}
