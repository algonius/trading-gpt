package htx

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

func TestPaperLifecycleReplayLedgerIsDeterministicAndReconciles(t *testing.T) {
	first := runPaperLifecycleScenario(t)
	second := runPaperLifecycleScenario(t)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("paper lifecycle replay snapshots differ:\nfirst=%#v\nsecond=%#v", first, second)
	}

	if first.Balances["USDT"].Available.String() != "9995.7163935" || first.Balances["USDT"].Locked.Sign() != 0 {
		t.Fatalf("USDT balance = %#v, want 9995.7163935 available and zero locked", first.Balances["USDT"])
	}
	if first.Balances["BTC"].Available.Sign() != 0 || first.Balances["BTC"].Locked.Sign() != 0 {
		t.Fatalf("BTC balance = %#v, want zero available and locked after close", first.Balances["BTC"])
	}
	if len(first.Ledger) != 8 {
		t.Fatalf("ledger entries = %d, want 8", len(first.Ledger))
	}
	if len(first.Drift) != 0 {
		t.Fatalf("ledger drift = %#v, want none", first.Drift)
	}
	if len(first.ClosedTrades) != 1 {
		t.Fatalf("closed trades = %d, want 1", len(first.ClosedTrades))
	}
	closed := first.ClosedTrades[0]
	if closed.RealizedPnL.String() != "-4.2836065" {
		t.Fatalf("closed trade pnl = %s, want -4.2836065", closed.RealizedPnL.String())
	}
	if closed.EntryNotional.String() != "3391" || closed.ExitNotional.String() != "3390.1065" || closed.ExitFee.String() != "3.3901065" {
		t.Fatalf("closed trade economics = %#v, want exact entry/exit/fee", closed)
	}

	buyOrder := first.Orders["1"]
	if buyOrder.Status != types.OrderStatusFilled || buyOrder.OriginalStatus != string(OrderStateFilled) || buyOrder.ExecutedQuantity.String() != "0.05" {
		t.Fatalf("buy order = %#v, want filled 0.05", buyOrder)
	}
	sellOrder := first.Orders["2"]
	if sellOrder.Status != types.OrderStatusFilled || sellOrder.OriginalStatus != string(OrderStateFilled) || sellOrder.ExecutedQuantity.String() != "0.04995" {
		t.Fatalf("sell order = %#v, want filled 0.04995", sellOrder)
	}
	canceledOrder := first.Orders["3"]
	if canceledOrder.Status != types.OrderStatusCanceled || canceledOrder.OriginalStatus != string(OrderStateCanceled) || canceledOrder.IsWorking {
		t.Fatalf("canceled order = %#v, want closed canceled state", canceledOrder)
	}

	buyTrades := first.Trades["1"]
	if len(buyTrades) != 1 || buyTrades[0].FeeCurrency != "BTC" || buyTrades[0].Fee.String() != "0.00005" {
		t.Fatalf("buy trades = %#v, want one BTC-fee fill", buyTrades)
	}
	sellTrades := first.Trades["2"]
	if len(sellTrades) != 1 || sellTrades[0].FeeCurrency != "USDT" || sellTrades[0].Fee.String() != "3.3901065" {
		t.Fatalf("sell trades = %#v, want one USDT-fee fill", sellTrades)
	}
	if trades := first.Trades["3"]; len(trades) != 0 {
		t.Fatalf("canceled order trades = %#v, want none", trades)
	}
}

func TestPaperLifecycleDoesNotFillOrderCreatedAfterClosedBar(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	session := newTestPaperLifecycleSessionAt(t, start.Add(time.Minute))

	ack, err := session.SubmitOrder(ctx, paperLimitOrder("after-bar", types.SideTypeBuy, "0.05", "67820"))
	if err != nil {
		t.Fatalf("SubmitOrder returned error: %v", err)
	}
	beforeBalances := sessionMustBalances(t, session)
	beforeLedger := session.Ledger()

	fills, err := session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("AdvanceKLines returned error: %v", err)
	}
	if len(fills) != 0 {
		t.Fatalf("fills = %#v, want none for order created after closed bar", fills)
	}
	assertPaperLifecycleNoMutation(t, session, beforeBalances, beforeLedger)

	order := sessionMustOrder(t, session, ack.OrderID)
	if order.Status != types.OrderStatusNew || !order.IsWorking || order.ExecutedQuantity.Sign() != 0 {
		t.Fatalf("order = %#v, want still-working unfilled order", order)
	}
	if trades := sessionMustTrades(t, session, ack.OrderID); len(trades) != 0 {
		t.Fatalf("trades = %#v, want none", trades)
	}
}

func TestPaperLifecycleDoesNotFillReplayedProcessedBar(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	session := newTestPaperLifecycleSession(t)

	fills, err := session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("initial AdvanceKLines returned error: %v", err)
	}
	if len(fills) != 0 {
		t.Fatalf("initial fills = %#v, want none before orders exist", fills)
	}
	if len(session.Ledger()) != 0 {
		t.Fatalf("ledger = %#v, want no ledger mutation from cursor-only bar processing", session.Ledger())
	}

	ack, err := session.SubmitOrder(ctx, paperLimitOrder("same-bar-replay", types.SideTypeBuy, "0.05", "67820"))
	if err != nil {
		t.Fatalf("SubmitOrder returned error: %v", err)
	}
	beforeBalances := sessionMustBalances(t, session)
	beforeLedger := session.Ledger()

	fills, err = session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("replayed AdvanceKLines returned error: %v", err)
	}
	if len(fills) != 0 {
		t.Fatalf("replayed fills = %#v, want none for already-processed bar", fills)
	}
	assertPaperLifecycleNoMutation(t, session, beforeBalances, beforeLedger)

	order := sessionMustOrder(t, session, ack.OrderID)
	if order.Status != types.OrderStatusNew || !order.IsWorking || order.ExecutedQuantity.Sign() != 0 {
		t.Fatalf("order = %#v, want still-working unfilled order", order)
	}
	if trades := sessionMustTrades(t, session, ack.OrderID); len(trades) != 0 {
		t.Fatalf("trades = %#v, want none", trades)
	}
}

func TestPaperLifecycleDoesNotConsumeCursorWhenFillFails(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)
	session := newTestPaperLifecycleSession(t)

	ack, err := session.SubmitOrder(ctx, paperLimitOrder("retry-after-fill-error", types.SideTypeBuy, "0.05", "67820"))
	if err != nil {
		t.Fatalf("SubmitOrder returned error: %v", err)
	}
	beforeBalances := sessionMustBalances(t, session)
	beforeLedger := session.Ledger()

	corrupted := beforeBalances["USDT"]
	corrupted.Locked = fixedpoint.Zero
	session.balances["USDT"] = corrupted

	fills, err := session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err == nil || !strings.Contains(err.Error(), "would make USDT balance negative") {
		t.Fatalf("AdvanceKLines error = %v, want injected locked-balance failure", err)
	}
	if len(fills) != 0 {
		t.Fatalf("fills after failed attempt = %#v, want none", fills)
	}
	if len(session.processed) != 0 {
		t.Fatalf("processed cursor = %#v, want no committed cursor after fill error", session.processed)
	}
	if after := session.Ledger(); !reflect.DeepEqual(after, beforeLedger) {
		t.Fatalf("ledger changed after failed attempt:\nbefore=%#v\nafter=%#v", beforeLedger, after)
	}

	session.balances = copyPaperBalances(beforeBalances)
	fills, err = session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("retry AdvanceKLines returned error: %v", err)
	}
	if len(fills) != 1 {
		t.Fatalf("retry fills = %#v, want exactly one fill", fills)
	}
	if fills[0].OrderID != 1 {
		t.Fatalf("fill order id = %d, want 1", fills[0].OrderID)
	}
	order := sessionMustOrder(t, session, ack.OrderID)
	if order.Status != types.OrderStatusFilled || order.ExecutedQuantity.String() != "0.05" {
		t.Fatalf("order after retry = %#v, want filled once", order)
	}
	if trades := sessionMustTrades(t, session, ack.OrderID); len(trades) != 1 {
		t.Fatalf("order trades after retry = %#v, want one trade", trades)
	}

	afterRetryBalances := sessionMustBalances(t, session)
	afterRetryLedger := session.Ledger()
	fills, err = session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("duplicate AdvanceKLines returned error: %v", err)
	}
	if len(fills) != 0 {
		t.Fatalf("duplicate fills = %#v, want none after cursor commit", fills)
	}
	assertPaperLifecycleNoMutation(t, session, afterRetryBalances, afterRetryLedger)
}

func TestPaperLifecycleFailsClosed(t *testing.T) {
	t.Run("unsupported live mode", func(t *testing.T) {
		_, err := NewPaperLifecycleSession(context.Background(), Config{Mode: Mode("live")}, newTestReplayMarketData(t))
		if err == nil || !strings.Contains(err.Error(), "unsupported HTX mode") {
			t.Fatalf("NewPaperLifecycleSession error = %v, want unsupported mode failure", err)
		}
	})

	t.Run("authenticated config", func(t *testing.T) {
		_, err := NewPaperLifecycleSession(context.Background(), Config{AllowAuthenticated: true}, newTestReplayMarketData(t))
		if err == nil || !strings.Contains(err.Error(), "authenticated access is disabled") {
			t.Fatalf("NewPaperLifecycleSession error = %v, want authenticated config failure", err)
		}
	})

	t.Run("nil market data source", func(t *testing.T) {
		_, err := NewPaperLifecycleSession(context.Background(), Config{Mode: ModeReplay}, nil)
		if err == nil || !strings.Contains(err.Error(), "requires market data source") {
			t.Fatalf("NewPaperLifecycleSession error = %v, want nil source failure", err)
		}
	})

	t.Run("below-default fee rate", func(t *testing.T) {
		_, err := NewPaperLifecycleSession(
			context.Background(),
			Config{Mode: ModeReplay},
			newTestReplayMarketData(t),
			WithPaperLifecycleFeeRate(fixedpoint.Zero),
		)
		if err == nil || !strings.Contains(err.Error(), "fee rate must not be below") {
			t.Fatalf("NewPaperLifecycleSession error = %v, want fee-rate failure", err)
		}
	})

	t.Run("invalid price precision", func(t *testing.T) {
		session := newTestPaperLifecycleSession(t)
		_, err := session.SubmitOrder(context.Background(), paperLimitOrder("bad-price", types.SideTypeBuy, "0.05", "67820.001"))
		if err == nil || !strings.Contains(err.Error(), "invalid price precision") {
			t.Fatalf("SubmitOrder error = %v, want price precision failure", err)
		}
	})

	t.Run("invalid quantity precision", func(t *testing.T) {
		session := newTestPaperLifecycleSession(t)
		_, err := session.SubmitOrder(context.Background(), paperLimitOrder("bad-quantity", types.SideTypeBuy, "0.0500001", "67820"))
		if err == nil || !strings.Contains(err.Error(), "invalid quantity precision") {
			t.Fatalf("SubmitOrder error = %v, want quantity precision failure", err)
		}
	})

	t.Run("margin side effect", func(t *testing.T) {
		session := newTestPaperLifecycleSession(t)
		order := paperLimitOrder("margin", types.SideTypeBuy, "0.05", "67820")
		order.MarginSideEffect = types.SideEffectTypeMarginBuy
		_, err := session.SubmitOrder(context.Background(), order)
		if err == nil || !strings.Contains(err.Error(), "rejects margin side effects") {
			t.Fatalf("SubmitOrder error = %v, want margin side-effect failure", err)
		}
	})
}

type paperLifecycleSnapshot struct {
	Balances     types.BalanceMap
	Orders       map[string]types.Order
	Trades       map[string][]types.Trade
	ClosedTrades []PaperClosedTrade
	Ledger       []PaperLedgerEntry
	Drift        types.BalanceMap
}

func runPaperLifecycleScenario(t *testing.T) paperLifecycleSnapshot {
	t.Helper()

	ctx := context.Background()
	session := newTestPaperLifecycleSession(t)
	start := time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC)

	buyAck, err := session.SubmitOrder(ctx, paperLimitOrder("buy-open", types.SideTypeBuy, "0.05", "67820"))
	if err != nil {
		t.Fatalf("SubmitOrder buy returned error: %v", err)
	}
	buyFills, err := session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &start,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("AdvanceKLines buy returned error: %v", err)
	}
	if len(buyFills) != 1 {
		t.Fatalf("buy fills = %d, want 1", len(buyFills))
	}

	sellAck, err := session.SubmitOrder(ctx, paperLimitOrder("sell-close", types.SideTypeSell, "0.04995", "67870"))
	if err != nil {
		t.Fatalf("SubmitOrder sell returned error: %v", err)
	}
	secondMinute := start.Add(time.Minute)
	sellFills, err := session.AdvanceKLines(ctx, "BTCUSDT", types.Interval1m, types.KLineQueryOptions{
		StartTime: &secondMinute,
		Limit:     1,
	})
	if err != nil {
		t.Fatalf("AdvanceKLines sell returned error: %v", err)
	}
	if len(sellFills) != 1 {
		t.Fatalf("sell fills = %d, want 1", len(sellFills))
	}

	cancelAck, err := session.SubmitOrder(ctx, paperLimitOrder("cancel-resting", types.SideTypeBuy, "0.01", "60000"))
	if err != nil {
		t.Fatalf("SubmitOrder cancel-resting returned error: %v", err)
	}
	if _, err := session.CancelOrder(ctx, cancelAck.OrderID); err != nil {
		t.Fatalf("CancelOrder returned error: %v", err)
	}

	reconciliation, err := session.ReconcileLedger()
	if err != nil {
		t.Fatalf("ReconcileLedger returned error: %v", err)
	}

	return paperLifecycleSnapshot{
		Balances: sessionMustBalances(t, session),
		Orders: map[string]types.Order{
			buyAck.OrderID:    sessionMustOrder(t, session, buyAck.OrderID),
			sellAck.OrderID:   sessionMustOrder(t, session, sellAck.OrderID),
			cancelAck.OrderID: sessionMustOrder(t, session, cancelAck.OrderID),
		},
		Trades: map[string][]types.Trade{
			buyAck.OrderID:    sessionMustTrades(t, session, buyAck.OrderID),
			sellAck.OrderID:   sessionMustTrades(t, session, sellAck.OrderID),
			cancelAck.OrderID: sessionMustTrades(t, session, cancelAck.OrderID),
		},
		ClosedTrades: session.ClosedTrades(),
		Ledger:       session.Ledger(),
		Drift:        reconciliation.Drift,
	}
}

func newTestPaperLifecycleSession(t *testing.T) *PaperLifecycleSession {
	t.Helper()

	return newTestPaperLifecycleSessionAt(t, time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC))
}

func newTestPaperLifecycleSessionAt(t *testing.T, now time.Time) *PaperLifecycleSession {
	t.Helper()

	session, err := NewPaperLifecycleSession(
		context.Background(),
		Config{Mode: ModeReplay},
		newTestReplayMarketData(t),
		WithPaperLifecycleBalances(types.BalanceMap{
			"USDT": {Currency: "USDT", Available: fixedpoint.MustNewFromString("10000")},
		}),
		WithPaperLifecycleClock(func() time.Time {
			return now
		}),
	)
	if err != nil {
		t.Fatalf("NewPaperLifecycleSession returned error: %v", err)
	}
	return session
}

func paperLimitOrder(clientOrderID string, side types.SideType, quantity string, price string) types.SubmitOrder {
	return types.SubmitOrder{
		ClientOrderID: clientOrderID,
		Symbol:        "BTCUSDT",
		Side:          side,
		Type:          types.OrderTypeLimit,
		Quantity:      fixedpoint.MustNewFromString(quantity),
		Price:         fixedpoint.MustNewFromString(price),
	}
}

func sessionMustBalances(t *testing.T, session *PaperLifecycleSession) types.BalanceMap {
	t.Helper()

	balances, err := session.QueryAccountBalances(context.Background())
	if err != nil {
		t.Fatalf("QueryAccountBalances returned error: %v", err)
	}
	return balances
}

func sessionMustOrder(t *testing.T, session *PaperLifecycleSession, orderID string) types.Order {
	t.Helper()

	order, err := session.QueryOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrder(%s) returned error: %v", orderID, err)
	}
	return order
}

func sessionMustTrades(t *testing.T, session *PaperLifecycleSession, orderID string) []types.Trade {
	t.Helper()

	trades, err := session.QueryOrderTrades(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrderTrades(%s) returned error: %v", orderID, err)
	}
	return trades
}

func assertPaperLifecycleNoMutation(t *testing.T, session *PaperLifecycleSession, beforeBalances types.BalanceMap, beforeLedger []PaperLedgerEntry) {
	t.Helper()

	if after := sessionMustBalances(t, session); !reflect.DeepEqual(after, beforeBalances) {
		t.Fatalf("balances changed after no-fill replay:\nbefore=%#v\nafter=%#v", beforeBalances, after)
	}
	if after := session.Ledger(); !reflect.DeepEqual(after, beforeLedger) {
		t.Fatalf("ledger changed after no-fill replay:\nbefore=%#v\nafter=%#v", beforeLedger, after)
	}
}
