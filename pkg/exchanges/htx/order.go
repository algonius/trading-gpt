package htx

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

type OrderState string

const (
	OrderStateCreated         OrderState = "created"
	OrderStateSubmitted       OrderState = "submitted"
	OrderStatePartialFilled   OrderState = "partial-filled"
	OrderStatePartialCanceled OrderState = "partial-canceled"
	OrderStateFilled          OrderState = "filled"
	OrderStateCanceled        OrderState = "canceled"
	OrderStateCanceling       OrderState = "canceling"
)

type OrderAck struct {
	OrderID string
}

type orderIDResponse struct {
	Status  string          `json:"status"`
	Data    json.RawMessage `json:"data"`
	ErrCode string          `json:"err-code"`
	ErrMsg  string          `json:"err-msg"`
}

type orderDetailResponse struct {
	Status  string      `json:"status"`
	Data    OrderDetail `json:"data"`
	ErrCode string      `json:"err-code"`
	ErrMsg  string      `json:"err-msg"`
}

type OrderDetail struct {
	ID                  uint64           `json:"id"`
	Symbol              string           `json:"symbol"`
	Type                string           `json:"type"`
	State               OrderState       `json:"state"`
	Price               fixedpoint.Value `json:"price"`
	Amount              fixedpoint.Value `json:"amount"`
	FilledAmount        fixedpoint.Value `json:"field-amount"`
	FilledCashAmount    fixedpoint.Value `json:"field-cash-amount"`
	FilledFees          fixedpoint.Value `json:"field-fees"`
	CreatedAtMillis     int64            `json:"created-at"`
	FinishedAtMillis    int64            `json:"finished-at"`
	CanceledAtMillis    int64            `json:"canceled-at"`
	ClientOrderID       string           `json:"client-order-id"`
	ExchangeOrderSource string           `json:"source"`
}

type matchResultsResponse struct {
	Status  string        `json:"status"`
	Data    []MatchResult `json:"data"`
	ErrCode string        `json:"err-code"`
	ErrMsg  string        `json:"err-msg"`
}

type MatchResult struct {
	ID              uint64           `json:"id"`
	MatchID         uint64           `json:"match-id"`
	OrderID         uint64           `json:"order-id"`
	Symbol          string           `json:"symbol"`
	Type            string           `json:"type"`
	Price           fixedpoint.Value `json:"price"`
	FilledAmount    fixedpoint.Value `json:"filled-amount"`
	FilledFees      fixedpoint.Value `json:"filled-fees"`
	FeeCurrency     string           `json:"fee-currency"`
	CreatedAtMillis int64            `json:"created-at"`
}

func ParseSubmitOrderResponse(r io.Reader) (OrderAck, error) {
	return parseOrderIDResponse(r, "submit order")
}

func ParseCancelOrderResponse(r io.Reader) (OrderAck, error) {
	return parseOrderIDResponse(r, "cancel order")
}

func ParseOrderResponse(r io.Reader) (types.Order, error) {
	var resp orderDetailResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return types.Order{}, err
	}
	if err := checkOrderResponseStatus(resp.Status, resp.ErrCode, resp.ErrMsg, "order detail"); err != nil {
		return types.Order{}, err
	}
	return resp.Data.Order()
}

func ParseOrderTrades(r io.Reader) ([]types.Trade, error) {
	var resp matchResultsResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, err
	}
	if err := checkOrderResponseStatus(resp.Status, resp.ErrCode, resp.ErrMsg, "order match results"); err != nil {
		return nil, err
	}

	trades := make([]types.Trade, 0, len(resp.Data))
	for _, result := range resp.Data {
		trade, err := result.Trade()
		if err != nil {
			return nil, err
		}
		trades = append(trades, trade)
	}
	return trades, nil
}

func (d OrderDetail) Order() (types.Order, error) {
	if d.ID == 0 {
		return types.Order{}, fmt.Errorf("HTX order detail id is empty")
	}
	symbol := NormalizeSymbol(d.Symbol)
	if symbol == "" {
		return types.Order{}, fmt.Errorf("HTX order detail symbol is empty")
	}

	side, orderType, err := ParseOrderType(d.Type)
	if err != nil {
		return types.Order{}, err
	}
	status, working, err := MapOrderState(d.State)
	if err != nil {
		return types.Order{}, err
	}

	createdAt := millisToTime(d.CreatedAtMillis)
	updatedAt := createdAt
	if d.FinishedAtMillis > 0 {
		updatedAt = millisToTime(d.FinishedAtMillis)
	} else if d.CanceledAtMillis > 0 {
		updatedAt = millisToTime(d.CanceledAtMillis)
	}

	return types.Order{
		SubmitOrder: types.SubmitOrder{
			ClientOrderID: d.ClientOrderID,
			Symbol:        symbol,
			Side:          side,
			Type:          orderType,
			Price:         d.Price,
			Quantity:      d.Amount,
		},
		Exchange:         types.ExchangeName(CanonicalExchange),
		OrderID:          d.ID,
		Status:           status,
		OriginalStatus:   string(d.State),
		ExecutedQuantity: d.FilledAmount,
		IsWorking:        working,
		CreationTime:     types.Time(createdAt),
		UpdateTime:       types.Time(updatedAt),
	}, nil
}

func (m MatchResult) Trade() (types.Trade, error) {
	if m.OrderID == 0 {
		return types.Trade{}, fmt.Errorf("HTX match result order id is empty")
	}
	symbol := NormalizeSymbol(m.Symbol)
	if symbol == "" {
		return types.Trade{}, fmt.Errorf("HTX match result symbol is empty")
	}

	side, _, err := ParseOrderType(m.Type)
	if err != nil {
		return types.Trade{}, err
	}

	id := m.MatchID
	if id == 0 {
		id = m.ID
	}
	if id == 0 {
		return types.Trade{}, fmt.Errorf("HTX match result id is empty")
	}

	return types.Trade{
		ID:            id,
		OrderID:       m.OrderID,
		Exchange:      types.ExchangeName(CanonicalExchange),
		Price:         m.Price,
		Quantity:      m.FilledAmount,
		QuoteQuantity: m.Price.Mul(m.FilledAmount),
		Symbol:        symbol,
		Side:          side,
		IsBuyer:       side == types.SideTypeBuy,
		Time:          types.Time(millisToTime(m.CreatedAtMillis)),
		Fee:           m.FilledFees,
		FeeCurrency:   strings.ToUpper(strings.TrimSpace(m.FeeCurrency)),
	}, nil
}

func MapOrderState(state OrderState) (types.OrderStatus, bool, error) {
	switch state {
	case OrderStateCreated, OrderStateSubmitted:
		return types.OrderStatusNew, true, nil
	case OrderStatePartialFilled:
		return types.OrderStatusPartiallyFilled, true, nil
	case OrderStatePartialCanceled:
		return types.OrderStatusPartiallyFilled, false, nil
	case OrderStateFilled:
		return types.OrderStatusFilled, false, nil
	case OrderStateCanceled:
		return types.OrderStatusCanceled, false, nil
	case OrderStateCanceling:
		return types.OrderStatusNew, true, nil
	default:
		return "", false, fmt.Errorf("unknown HTX order state %q", state)
	}
}

func ParseOrderType(orderType string) (types.SideType, types.OrderType, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(orderType)), "-")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("unknown HTX order type %q", orderType)
	}

	var side types.SideType
	switch parts[0] {
	case "buy":
		side = types.SideTypeBuy
	case "sell":
		side = types.SideTypeSell
	default:
		return "", "", fmt.Errorf("unknown HTX order side %q", orderType)
	}

	switch strings.Join(parts[1:], "-") {
	case "market":
		return side, types.OrderTypeMarket, nil
	case "limit":
		return side, types.OrderTypeLimit, nil
	case "limit-maker":
		return side, types.OrderTypeLimitMaker, nil
	case "stop-limit":
		return side, types.OrderTypeStopLimit, nil
	default:
		return "", "", fmt.Errorf("unknown HTX order type %q", orderType)
	}
}

func parseOrderIDResponse(r io.Reader, context string) (OrderAck, error) {
	var resp orderIDResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return OrderAck{}, err
	}
	if err := checkOrderResponseStatus(resp.Status, resp.ErrCode, resp.ErrMsg, context); err != nil {
		return OrderAck{}, err
	}

	orderID, err := parseOrderID(resp.Data)
	if err != nil {
		return OrderAck{}, fmt.Errorf("HTX %s response: %w", context, err)
	}
	return OrderAck{OrderID: orderID}, nil
}

func parseOrderID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("order id is empty")
	}

	var id string
	if err := json.Unmarshal(raw, &id); err == nil {
		id = strings.TrimSpace(id)
		if id == "" {
			return "", fmt.Errorf("order id is empty")
		}
		return id, nil
	}

	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", fmt.Errorf("order id is not a string or number")
	}
	id = number.String()
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return "", fmt.Errorf("order id is invalid")
	}
	return id, nil
}

func checkOrderResponseStatus(status string, code string, message string, context string) error {
	if status == "" || status == "ok" {
		return nil
	}
	if code != "" {
		return fmt.Errorf("HTX %s response status %q code %q", context, status, code)
	}
	if message != "" {
		return fmt.Errorf("HTX %s response status %q: %s", context, status, message)
	}
	return fmt.Errorf("HTX %s response status %q", context, status)
}

func millisToTime(milliseconds int64) time.Time {
	if milliseconds <= 0 {
		return time.Time{}
	}
	return time.Unix(0, milliseconds*int64(time.Millisecond)).UTC()
}
