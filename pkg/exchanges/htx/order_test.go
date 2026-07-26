package htx

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/c9s/bbgo/pkg/types"
)

func TestParseOrderAcknowledgements(t *testing.T) {
	submitFile := openHTXFixture(t, "testdata/order_submit_ack.json")
	defer submitFile.Close()
	submitAck, err := ParseSubmitOrderResponse(submitFile)
	if err != nil {
		t.Fatalf("ParseSubmitOrderResponse returned error: %v", err)
	}
	if submitAck.OrderID != "59378" {
		t.Fatalf("submit order id = %q, want 59378", submitAck.OrderID)
	}

	cancelFile := openHTXFixture(t, "testdata/order_cancel_ack.json")
	defer cancelFile.Close()
	cancelAck, err := ParseCancelOrderResponse(cancelFile)
	if err != nil {
		t.Fatalf("ParseCancelOrderResponse returned error: %v", err)
	}
	if cancelAck.OrderID != "59378" {
		t.Fatalf("cancel order id = %q, want 59378", cancelAck.OrderID)
	}
}

func TestParseOrderResponsesRejectMissingOrBlankStatus(t *testing.T) {
	orderDetailData := `{"id":59378,"symbol":"btcusdt","amount":"0.1","price":"67800.12","created-at":1785024000000,"type":"buy-limit","field-amount":"0","state":"submitted"}`
	matchResultsData := `[{"id":9001,"match-id":99001,"order-id":59379,"symbol":"btcusdt","type":"sell-limit","price":"67810","filled-amount":"0.02","filled-fees":"0.002","fee-currency":"usdt","created-at":1785024140000}]`

	tests := []struct {
		name    string
		payload string
		parse   func(string) error
	}{
		{
			name:    "submit ack missing",
			payload: `{"data":"59378"}`,
			parse: func(payload string) error {
				_, err := ParseSubmitOrderResponse(strings.NewReader(payload))
				return err
			},
		},
		{
			name:    "submit ack blank",
			payload: `{"status":" ","data":"59378"}`,
			parse: func(payload string) error {
				_, err := ParseSubmitOrderResponse(strings.NewReader(payload))
				return err
			},
		},
		{
			name:    "cancel ack missing",
			payload: `{"data":"59378"}`,
			parse: func(payload string) error {
				_, err := ParseCancelOrderResponse(strings.NewReader(payload))
				return err
			},
		},
		{
			name:    "cancel ack blank",
			payload: `{"status":" ","data":"59378"}`,
			parse: func(payload string) error {
				_, err := ParseCancelOrderResponse(strings.NewReader(payload))
				return err
			},
		},
		{
			name:    "order detail missing",
			payload: `{"data":` + orderDetailData + `}`,
			parse: func(payload string) error {
				_, err := ParseOrderResponse(strings.NewReader(payload))
				return err
			},
		},
		{
			name:    "order detail blank",
			payload: `{"status":" ","data":` + orderDetailData + `}`,
			parse: func(payload string) error {
				_, err := ParseOrderResponse(strings.NewReader(payload))
				return err
			},
		},
		{
			name:    "match results missing",
			payload: `{"data":` + matchResultsData + `}`,
			parse: func(payload string) error {
				_, err := ParseOrderTrades(strings.NewReader(payload))
				return err
			},
		},
		{
			name:    "match results blank",
			payload: `{"status":" ","data":` + matchResultsData + `}`,
			parse: func(payload string) error {
				_, err := ParseOrderTrades(strings.NewReader(payload))
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.parse(tc.payload)
			if err == nil || !strings.Contains(err.Error(), "status is missing or blank") {
				t.Fatalf("parse error = %v, want missing/blank status failure", err)
			}
		})
	}
}

func TestParseOrderResponsePartialCanceledConservativeMapping(t *testing.T) {
	f := openHTXFixture(t, "testdata/order_partial_canceled.json")
	defer f.Close()

	order, err := ParseOrderResponse(f)
	if err != nil {
		t.Fatalf("ParseOrderResponse returned error: %v", err)
	}

	if order.Exchange.String() != CanonicalExchange {
		t.Fatalf("exchange = %q, want %q", order.Exchange, CanonicalExchange)
	}
	if order.OrderID != 59378 {
		t.Fatalf("order id = %d, want 59378", order.OrderID)
	}
	if order.Symbol != "BTCUSDT" {
		t.Fatalf("symbol = %q, want BTCUSDT", order.Symbol)
	}
	if order.Side != types.SideTypeBuy || order.Type != types.OrderTypeLimit {
		t.Fatalf("side/type = %s/%s, want BUY/LIMIT", order.Side, order.Type)
	}
	if order.Status != types.OrderStatusPartiallyFilled {
		t.Fatalf("status = %s, want PARTIALLY_FILLED", order.Status)
	}
	if order.OriginalStatus != string(OrderStatePartialCanceled) {
		t.Fatalf("original status = %q, want partial-canceled", order.OriginalStatus)
	}
	if order.IsWorking {
		t.Fatal("partial-canceled order should not be working")
	}
	if order.ExecutedQuantity.String() != "0.04" {
		t.Fatalf("executed quantity = %s, want 0.04", order.ExecutedQuantity.String())
	}
	if got := order.CreationTime.Time().Format(time.RFC3339); got != "2026-07-26T00:00:00Z" {
		t.Fatalf("created time = %s, want 2026-07-26T00:00:00Z", got)
	}
	if got := order.UpdateTime.Time().Format(time.RFC3339); got != "2026-07-26T00:01:00Z" {
		t.Fatalf("updated time = %s, want 2026-07-26T00:01:00Z", got)
	}
}

func TestParseOrderResponseFilled(t *testing.T) {
	f := openHTXFixture(t, "testdata/order_filled.json")
	defer f.Close()

	order, err := ParseOrderResponse(f)
	if err != nil {
		t.Fatalf("ParseOrderResponse returned error: %v", err)
	}

	if order.Status != types.OrderStatusFilled {
		t.Fatalf("status = %s, want FILLED", order.Status)
	}
	if order.IsWorking {
		t.Fatal("filled order should not be working")
	}
	if order.Side != types.SideTypeSell || order.Type != types.OrderTypeLimit {
		t.Fatalf("side/type = %s/%s, want SELL/LIMIT", order.Side, order.Type)
	}
	if order.ExecutedQuantity.String() != "0.05" {
		t.Fatalf("executed quantity = %s, want 0.05", order.ExecutedQuantity.String())
	}
}

func TestOrderStateMappingDocumentsUnknownFailure(t *testing.T) {
	tests := map[OrderState]struct {
		status  types.OrderStatus
		working bool
	}{
		OrderStateCreated:         {types.OrderStatusNew, true},
		OrderStateSubmitted:       {types.OrderStatusNew, true},
		OrderStatePartialFilled:   {types.OrderStatusPartiallyFilled, true},
		OrderStatePartialCanceled: {types.OrderStatusPartiallyFilled, false},
		OrderStateFilled:          {types.OrderStatusFilled, false},
		OrderStateCanceled:        {types.OrderStatusCanceled, false},
		OrderStateCanceling:       {types.OrderStatusNew, true},
	}

	for state, expected := range tests {
		t.Run(string(state), func(t *testing.T) {
			status, working, err := MapOrderState(state)
			if err != nil {
				t.Fatalf("MapOrderState returned error: %v", err)
			}
			if status != expected.status || working != expected.working {
				t.Fatalf("MapOrderState(%q) = %s/%t, want %s/%t", state, status, working, expected.status, expected.working)
			}
		})
	}

	if _, _, err := MapOrderState("mystery-state"); err == nil || !strings.Contains(err.Error(), "unknown HTX order state") {
		t.Fatalf("unknown state error = %v, want explicit unknown-state failure", err)
	}

	f := openHTXFixture(t, "testdata/order_unknown_state.json")
	defer f.Close()
	if _, err := ParseOrderResponse(f); err == nil || !strings.Contains(err.Error(), "unknown HTX order state") {
		t.Fatalf("ParseOrderResponse error = %v, want explicit unknown-state failure", err)
	}
}

func TestParseOrderTrades(t *testing.T) {
	f := openHTXFixture(t, "testdata/order_match_results.json")
	defer f.Close()

	trades, err := ParseOrderTrades(f)
	if err != nil {
		t.Fatalf("ParseOrderTrades returned error: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("len(trades) = %d, want 2", len(trades))
	}

	first := trades[0]
	if first.ID != 99001 || first.OrderID != 59379 {
		t.Fatalf("trade id/order id = %d/%d, want 99001/59379", first.ID, first.OrderID)
	}
	if first.Exchange.String() != CanonicalExchange || first.Symbol != "BTCUSDT" {
		t.Fatalf("exchange/symbol = %s/%s, want htx/BTCUSDT", first.Exchange, first.Symbol)
	}
	if first.Side != types.SideTypeSell || first.IsBuyer {
		t.Fatalf("side/isBuyer = %s/%t, want SELL/false", first.Side, first.IsBuyer)
	}
	if first.Quantity.String() != "0.02" {
		t.Fatalf("quantity = %s, want 0.02", first.Quantity.String())
	}
	if first.QuoteQuantity.String() != "1356.2" {
		t.Fatalf("quote quantity = %s, want 1356.2", first.QuoteQuantity.String())
	}
	if first.FeeCurrency != "USDT" {
		t.Fatalf("fee currency = %s, want USDT", first.FeeCurrency)
	}
	if got := first.Time.Time().Format(time.RFC3339); got != "2026-07-26T00:02:20Z" {
		t.Fatalf("trade time = %s, want 2026-07-26T00:02:20Z", got)
	}
}

func TestParseOrderTypeRejectsUnknown(t *testing.T) {
	side, orderType, err := ParseOrderType("buy-limit-maker")
	if err != nil {
		t.Fatalf("ParseOrderType returned error: %v", err)
	}
	if side != types.SideTypeBuy || orderType != types.OrderTypeLimitMaker {
		t.Fatalf("side/type = %s/%s, want BUY/LIMIT_MAKER", side, orderType)
	}

	if _, _, err := ParseOrderType("borrow-limit"); err == nil {
		t.Fatal("expected unknown order side to fail")
	}
	if _, _, err := ParseOrderType("buy-super-limit"); err == nil {
		t.Fatal("expected unknown order type to fail")
	}
}

func openHTXFixture(t *testing.T, path string) *os.File {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
