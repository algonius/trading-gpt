package htx

import (
	"bytes"
	"context"
	"errors"
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
	if got := evidenceAmounts(firstEvidence.Summary.GrossClosedPnL)["USDT"]; got != "-0.8935" {
		t.Fatalf("gross closed pnl USDT = %s, want -0.8935", got)
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
	if len(firstEvidence.ClosedTrades) != 1 || firstEvidence.ClosedTrades[0].NetPnL != "-4.2836065" {
		t.Fatalf("closed trades = %#v, want one retained net PnL row", firstEvidence.ClosedTrades)
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

	t.Run("failed writer", func(t *testing.T) {
		writer := &failingEvidenceWriter{err: errors.New("injected write failure")}
		err := AppendPaperLifecycleEvidenceJSONL(writer, completedTestPaperLifecycleSession(t))
		if err == nil || !strings.Contains(err.Error(), "injected write failure") {
			t.Fatalf("append error = %v, want injected write failure", err)
		}
		if writer.buf.Len() != 0 {
			t.Fatalf("writer buffer = %q, want no partial record after failed write", writer.buf.String())
		}

		writer.err = nil
		if err := AppendPaperLifecycleEvidenceJSONL(writer, completedTestPaperLifecycleSession(t)); err != nil {
			t.Fatalf("retry append returned error: %v", err)
		}
		records, err := ReadPaperLifecycleEvidenceJSONL(bytes.NewReader(writer.buf.Bytes()))
		if err != nil {
			t.Fatalf("ReadPaperLifecycleEvidenceJSONL returned error: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("records after failed-write retry = %d, want exactly one", len(records))
		}
	})
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

type failingEvidenceWriter struct {
	buf bytes.Buffer
	err error
}

func (w *failingEvidenceWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return w.buf.Write(p)
}
