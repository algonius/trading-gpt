package htx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestPaperLifecycleEvidenceIsDeterministicAndCanonical(t *testing.T) {
	first := completedTestPaperLifecycleSession(t)
	second := completedTestPaperLifecycleSession(t)

	firstEvidence, err := ExportPaperLifecycleEvidence(first)
	if err != nil {
		t.Fatalf("ExportPaperLifecycleEvidence(first) returned error: %v", err)
	}
	secondEvidence, err := ExportPaperLifecycleEvidence(second)
	if err != nil {
		t.Fatalf("ExportPaperLifecycleEvidence(second) returned error: %v", err)
	}

	firstBytes, err := MarshalPaperLifecycleEvidenceJSON(firstEvidence)
	if err != nil {
		t.Fatalf("MarshalPaperLifecycleEvidenceJSON(first) returned error: %v", err)
	}
	secondBytes, err := MarshalPaperLifecycleEvidenceJSON(secondEvidence)
	if err != nil {
		t.Fatalf("MarshalPaperLifecycleEvidenceJSON(second) returned error: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("evidence bytes differ:\nfirst=%s\nsecond=%s", string(firstBytes), string(secondBytes))
	}

	if firstEvidence.Version != PaperLifecycleEvidenceVersion || firstEvidence.Exchange != CanonicalExchange || firstEvidence.Mode != string(ModeReplay) {
		t.Fatalf("evidence identity = %#v", firstEvidence)
	}
	if firstEvidence.ExportedAt != "2026-07-26T00:00:00Z" {
		t.Fatalf("exported_at = %s, want deterministic injected clock", firstEvidence.ExportedAt)
	}
	if got := evidenceAmounts(firstEvidence.Summary.GrossTurnover)["USDT"]; got != "6781.1065" {
		t.Fatalf("gross turnover USDT = %s, want 6781.1065", got)
	}
	if got := evidenceAmounts(firstEvidence.Summary.GrossBuyTurnover)["USDT"]; got != "3391" {
		t.Fatalf("gross buy turnover USDT = %s, want 3391", got)
	}
	if got := evidenceAmounts(firstEvidence.Summary.GrossSellTurnover)["USDT"]; got != "3390.1065" {
		t.Fatalf("gross sell turnover USDT = %s, want 3390.1065", got)
	}
	if fees := evidenceAmounts(firstEvidence.Summary.Fees); fees["BTC"] != "0.00005" || fees["USDT"] != "3.3901065" {
		t.Fatalf("fees = %#v, want BTC/USDT fee totals", fees)
	}
	if got := evidenceAmounts(firstEvidence.Summary.EntryFees)["USDT"]; got != "3.391" {
		t.Fatalf("entry fees USDT = %s, want 3.391", got)
	}
	if got := evidenceAmounts(firstEvidence.Summary.ExitFees)["USDT"]; got != "3.3901065" {
		t.Fatalf("exit fees USDT = %s, want 3.3901065", got)
	}
	if got := evidenceAmounts(firstEvidence.Summary.GrossClosedPnL)["USDT"]; got != "2.4975" {
		t.Fatalf("gross closed pnl USDT = %s, want 2.4975", got)
	}
	if got := evidenceAmounts(firstEvidence.Summary.NetClosedPnL)["USDT"]; got != "-4.2836065" {
		t.Fatalf("net closed pnl USDT = %s, want -4.2836065", got)
	}

	if currencies := []string{firstEvidence.Balances[0].Currency, firstEvidence.Balances[1].Currency}; !reflect.DeepEqual(currencies, []string{"BTC", "USDT"}) {
		t.Fatalf("balance order = %#v, want BTC then USDT", currencies)
	}
	if orderIDs := []uint64{firstEvidence.Orders[0].OrderID, firstEvidence.Orders[1].OrderID, firstEvidence.Orders[2].OrderID}; !reflect.DeepEqual(orderIDs, []uint64{1, 2, 3}) {
		t.Fatalf("order ids = %#v, want sorted 1/2/3", orderIDs)
	}
	if tradeIDs := []uint64{firstEvidence.Trades[0].TradeID, firstEvidence.Trades[1].TradeID}; !reflect.DeepEqual(tradeIDs, []uint64{1, 2}) {
		t.Fatalf("trade ids = %#v, want sorted 1/2", tradeIDs)
	}
	if len(firstEvidence.Ledger) != 8 || firstEvidence.Ledger[0].Sequence != 1 || firstEvidence.Ledger[7].Sequence != 8 {
		t.Fatalf("ledger sequence = %#v, want contiguous 1..8", firstEvidence.Ledger)
	}
	if len(firstEvidence.ClosedTrades) != 1 {
		t.Fatalf("closed trades = %#v, want one retained net PnL row", firstEvidence.ClosedTrades)
	}
	closed := firstEvidence.ClosedTrades[0]
	if closed.EntryNotional != "3387.609" || closed.EntryFee != "3.391" || closed.EntryFeeCurrency != "USDT" ||
		closed.ExitNotional != "3390.1065" || closed.GrossPnL != "2.4975" || closed.ExitFee != "3.3901065" ||
		closed.ExitFeeCurrency != "USDT" || closed.NetPnL != "-4.2836065" {
		t.Fatalf("closed trade = %#v, want exact fee-separated PnL decomposition", closed)
	}
	if len(firstEvidence.Reconciliation.Drift) != 0 {
		t.Fatalf("drift = %#v, want zero drift", firstEvidence.Reconciliation.Drift)
	}
}

func TestPaperLifecycleEvidenceJSONLAppendAndRead(t *testing.T) {
	var buf bytes.Buffer

	if err := AppendPaperLifecycleEvidenceJSONL(&buf, completedTestPaperLifecycleSession(t)); err != nil {
		t.Fatalf("first AppendPaperLifecycleEvidenceJSONL returned error: %v", err)
	}
	if err := AppendPaperLifecycleEvidenceJSONL(&buf, completedTestPaperLifecycleSession(t)); err != nil {
		t.Fatalf("second AppendPaperLifecycleEvidenceJSONL returned error: %v", err)
	}
	if got := bytes.Count(buf.Bytes(), []byte("\n")); got != 2 {
		t.Fatalf("JSONL newline count = %d, want 2", got)
	}

	records, err := ReadPaperLifecycleEvidenceJSONL(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ReadPaperLifecycleEvidenceJSONL returned error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}
	if !reflect.DeepEqual(records[0], records[1]) {
		t.Fatalf("records differ:\nfirst=%#v\nsecond=%#v", records[0], records[1])
	}
}

func TestPaperLifecycleEvidenceFailsClosed(t *testing.T) {
	t.Run("non-zero drift", func(t *testing.T) {
		session := completedTestPaperLifecycleSession(t)
		balance := session.balances["USDT"]
		balance.Available = balance.Available.Add(fixedpoint.NewFromInt(1))
		session.balances["USDT"] = balance

		_, err := ExportPaperLifecycleEvidence(session)
		if err == nil || !strings.Contains(err.Error(), "drift is non-zero") {
			t.Fatalf("ExportPaperLifecycleEvidence error = %v, want drift failure", err)
		}
	})

	t.Run("working order", func(t *testing.T) {
		session := newTestPaperLifecycleSession(t)
		if _, err := session.SubmitOrder(context.Background(), paperLimitOrder("open", types.SideTypeBuy, "0.05", "60000")); err != nil {
			t.Fatalf("SubmitOrder returned error: %v", err)
		}

		_, err := ExportPaperLifecycleEvidence(session)
		if err == nil || !strings.Contains(err.Error(), "still has locked funds") {
			t.Fatalf("ExportPaperLifecycleEvidence error = %v, want incomplete locked-balance failure", err)
		}
	})

	t.Run("no closed trade", func(t *testing.T) {
		session := newTestPaperLifecycleSession(t)
		ack, err := session.SubmitOrder(context.Background(), paperLimitOrder("cancel-only", types.SideTypeBuy, "0.05", "60000"))
		if err != nil {
			t.Fatalf("SubmitOrder returned error: %v", err)
		}
		if _, err := session.CancelOrder(context.Background(), ack.OrderID); err != nil {
			t.Fatalf("CancelOrder returned error: %v", err)
		}

		_, err = ExportPaperLifecycleEvidence(session)
		if err == nil || !strings.Contains(err.Error(), "requires at least one closed trade") {
			t.Fatalf("ExportPaperLifecycleEvidence error = %v, want closed-trade failure", err)
		}
	})
}

func TestPaperLifecycleEvidenceAppendDoesNotMutateOnFailure(t *testing.T) {
	t.Run("failed reconciliation", func(t *testing.T) {
		var buf bytes.Buffer
		if err := AppendPaperLifecycleEvidenceJSONL(&buf, completedTestPaperLifecycleSession(t)); err != nil {
			t.Fatalf("initial append returned error: %v", err)
		}
		before := append([]byte(nil), buf.Bytes()...)

		drifted := completedTestPaperLifecycleSession(t)
		balance := drifted.balances["USDT"]
		balance.Available = balance.Available.Add(fixedpoint.NewFromInt(1))
		drifted.balances["USDT"] = balance

		err := AppendPaperLifecycleEvidenceJSONL(&buf, drifted)
		if err == nil || !strings.Contains(err.Error(), "drift is non-zero") {
			t.Fatalf("append error = %v, want drift failure", err)
		}
		if !bytes.Equal(buf.Bytes(), before) {
			t.Fatalf("buffer changed after failed reconciliation:\nbefore=%s\nafter=%s", string(before), buf.String())
		}
	})

	t.Run("partial write error rolls back", func(t *testing.T) {
		sink := &rollbackEvidenceSink{limit: -1}
		if err := AppendPaperLifecycleEvidenceJSONL(sink, completedTestPaperLifecycleSession(t)); err != nil {
			t.Fatalf("initial append returned error: %v", err)
		}
		before := append([]byte(nil), sink.Bytes()...)

		injected := errors.New("injected partial write failure")
		sink.limit = 31
		sink.err = injected
		err := AppendPaperLifecycleEvidenceJSONL(sink, completedTestPaperLifecycleSession(t))
		if !errors.Is(err, injected) {
			t.Fatalf("append error = %v, want injected partial write failure", err)
		}
		if !bytes.Equal(sink.Bytes(), before) {
			t.Fatalf("sink changed after partial write error:\nbefore=%s\nafter=%s", string(before), sink.String())
		}

		sink.limit = -1
		sink.err = nil
		if err := AppendPaperLifecycleEvidenceJSONL(sink, completedTestPaperLifecycleSession(t)); err != nil {
			t.Fatalf("retry append returned error: %v", err)
		}
		records, err := ReadPaperLifecycleEvidenceJSONL(bytes.NewReader(sink.Bytes()))
		if err != nil {
			t.Fatalf("ReadPaperLifecycleEvidenceJSONL returned error: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("records after partial-write retry = %d, want one preexisting plus one retried record", len(records))
		}
	})

	t.Run("short write rolls back", func(t *testing.T) {
		sink := &rollbackEvidenceSink{limit: -1}
		if err := AppendPaperLifecycleEvidenceJSONL(sink, completedTestPaperLifecycleSession(t)); err != nil {
			t.Fatalf("initial append returned error: %v", err)
		}
		before := append([]byte(nil), sink.Bytes()...)

		sink.limit = 47
		err := AppendPaperLifecycleEvidenceJSONL(sink, completedTestPaperLifecycleSession(t))
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("append error = %v, want short write", err)
		}
		if !bytes.Equal(sink.Bytes(), before) {
			t.Fatalf("sink changed after short write:\nbefore=%s\nafter=%s", string(before), sink.String())
		}

		sink.limit = -1
		if err := AppendPaperLifecycleEvidenceJSONL(sink, completedTestPaperLifecycleSession(t)); err != nil {
			t.Fatalf("retry append returned error: %v", err)
		}
		records, err := ReadPaperLifecycleEvidenceJSONL(bytes.NewReader(sink.Bytes()))
		if err != nil {
			t.Fatalf("ReadPaperLifecycleEvidenceJSONL returned error: %v", err)
		}
		if len(records) != 2 {
			t.Fatalf("records after short-write retry = %d, want one preexisting plus one retried record", len(records))
		}
	})
}

func TestPaperLifecycleEvidenceJSONLRejectsInvalidRecords(t *testing.T) {
	valid := mustPaperLifecycleEvidence(t)
	malformed := valid
	malformed.Orders[0].Price = "not-a-number"

	missingRequired := valid
	missingRequired.Exchange = ""

	cases := []struct {
		name string
		line []byte
		want string
	}{
		{
			name: "empty object",
			line: []byte(`{}`),
			want: "unsupported",
		},
		{
			name: "unknown version",
			line: []byte(`{"version":999}`),
			want: "unsupported",
		},
		{
			name: "unknown record field",
			line: []byte(`{"version":1,"record_type":"unexpected","exchange":"htx","mode":"replay"}`),
			want: "unknown field",
		},
		{
			name: "invalid mode",
			line: mustPaperLifecycleEvidenceJSON(t, func(e PaperLifecycleEvidence) PaperLifecycleEvidence {
				e.Mode = "live"
				return e
			}),
			want: "mode",
		},
		{
			name: "missing required field",
			line: mustMarshalPaperLifecycleEvidence(t, missingRequired),
			want: "exchange",
		},
		{
			name: "malformed required amount",
			line: mustMarshalPaperLifecycleEvidence(t, malformed),
			want: "orders[0].price",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReadPaperLifecycleEvidenceJSONL(bytes.NewReader(append(append([]byte(nil), tc.line...), '\n')))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ReadPaperLifecycleEvidenceJSONL error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPaperLifecycleEvidenceJSONLReaderAcceptsRecordOverScannerLimit(t *testing.T) {
	line := mustPaperLifecycleEvidenceJSON(t, func(e PaperLifecycleEvidence) PaperLifecycleEvidence {
		e.Orders[0].ClientOrderID = strings.Repeat("x", 70*1024)
		return e
	})
	if len(line) <= 64*1024 {
		t.Fatalf("record length = %d, want larger than scanner default token limit", len(line))
	}
	if len(line) >= MaxPaperLifecycleEvidenceJSONLRecordBytes {
		t.Fatalf("record length = %d, want below explicit record bound", len(line))
	}

	records, err := ReadPaperLifecycleEvidenceJSONL(bytes.NewReader(append(append([]byte(nil), line...), '\n')))
	if err != nil {
		t.Fatalf("ReadPaperLifecycleEvidenceJSONL returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if got := records[0].Orders[0].ClientOrderID; got != strings.Repeat("x", 70*1024) {
		t.Fatalf("large client_order_id length = %d, want retained", len(got))
	}
}

func TestPaperLifecycleEvidenceJSONLReaderRejectsOversizeRecord(t *testing.T) {
	line := mustPaperLifecycleEvidenceJSON(t, func(e PaperLifecycleEvidence) PaperLifecycleEvidence {
		e.Orders[0].ClientOrderID = strings.Repeat("x", MaxPaperLifecycleEvidenceJSONLRecordBytes)
		return e
	})
	if len(line) <= MaxPaperLifecycleEvidenceJSONLRecordBytes {
		t.Fatalf("record length = %d, want larger than explicit record bound", len(line))
	}

	_, err := ReadPaperLifecycleEvidenceJSONL(bytes.NewReader(append(append([]byte(nil), line...), '\n')))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadPaperLifecycleEvidenceJSONL error = %v, want oversize failure", err)
	}
}

func completedTestPaperLifecycleSession(t *testing.T) *PaperLifecycleSession {
	t.Helper()

	ctx := context.Background()
	session := newTestPaperLifecycleSession(t)
	start := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	buyAck, err := session.SubmitOrder(ctx, paperLimitOrder("buy-open", types.SideTypeBuy, "0.05", "67820"))
	if err != nil {
		t.Fatalf("SubmitOrder buy returned error: %v", err)
	}
	if fills, err := session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{StartTime: &start, Limit: 1}); err != nil || len(fills) != 1 {
		t.Fatalf("buy AdvanceKLines fills=%#v err=%v, want one fill", fills, err)
	}
	if order := sessionMustOrder(t, session, buyAck.OrderID); order.Status != types.OrderStatusFilled {
		t.Fatalf("buy order = %#v, want filled", order)
	}

	sellAck, err := session.SubmitOrder(ctx, paperLimitOrder("sell-close", types.SideTypeSell, "0.04995", "67870"))
	if err != nil {
		t.Fatalf("SubmitOrder sell returned error: %v", err)
	}
	secondMinute := start.Add(time.Minute)
	if fills, err := session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{StartTime: &secondMinute, Limit: 1}); err != nil || len(fills) != 1 {
		t.Fatalf("sell AdvanceKLines fills=%#v err=%v, want one fill", fills, err)
	}
	if order := sessionMustOrder(t, session, sellAck.OrderID); order.Status != types.OrderStatusFilled {
		t.Fatalf("sell order = %#v, want filled", order)
	}

	cancelAck, err := session.SubmitOrder(ctx, paperLimitOrder("cancel-resting", types.SideTypeBuy, "0.01", "60000"))
	if err != nil {
		t.Fatalf("SubmitOrder cancel-resting returned error: %v", err)
	}
	if _, err := session.CancelOrder(ctx, cancelAck.OrderID); err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}
	return session
}

func evidenceAmounts(values []PaperEvidenceCurrencyAmount) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		out[value.Currency] = value.Amount
	}
	return out
}

func mustPaperLifecycleEvidence(t *testing.T) PaperLifecycleEvidence {
	t.Helper()

	evidence, err := ExportPaperLifecycleEvidence(completedTestPaperLifecycleSession(t))
	if err != nil {
		t.Fatalf("ExportPaperLifecycleEvidence returned error: %v", err)
	}
	return evidence
}

func mustPaperLifecycleEvidenceJSON(t *testing.T, mutate func(PaperLifecycleEvidence) PaperLifecycleEvidence) []byte {
	t.Helper()

	evidence := mustPaperLifecycleEvidence(t)
	if mutate != nil {
		evidence = mutate(evidence)
	}
	return mustMarshalPaperLifecycleEvidence(t, evidence)
}

func mustMarshalPaperLifecycleEvidence(t *testing.T, evidence PaperLifecycleEvidence) []byte {
	t.Helper()

	out, err := MarshalPaperLifecycleEvidenceJSON(evidence)
	if err != nil {
		t.Fatalf("MarshalPaperLifecycleEvidenceJSON returned error: %v", err)
	}
	return out
}

type rollbackEvidenceSink struct {
	bytes.Buffer
	limit int
	err   error
}

func (s *rollbackEvidenceSink) Write(p []byte) (int, error) {
	if s.limit >= 0 {
		n := s.limit
		if n > len(p) {
			n = len(p)
		}
		if n > 0 {
			_, _ = s.Buffer.Write(p[:n])
		}
		return n, s.err
	}
	return s.Buffer.Write(p)
}
