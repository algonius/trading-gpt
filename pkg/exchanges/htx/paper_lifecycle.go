package htx

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

const (
	PaperLedgerSubmitLock = "submit-lock"
	PaperLedgerCancel     = "cancel-unlock"
	PaperLedgerFillDebit  = "fill-debit"
	PaperLedgerFillCredit = "fill-credit"
)

var defaultPaperTakerFeeRate = fixedpoint.MustNewFromString("0.001")

type PaperLifecycleSession struct {
	cfg        Config
	source     MarketData
	markets    types.MarketMap
	balances   types.BalanceMap
	starting   types.BalanceMap
	orders     map[uint64]types.Order
	trades     map[uint64][]types.Trade
	ledger     []PaperLedgerEntry
	positions  map[string][]paperPositionLot
	closed     []PaperClosedTrade
	feeRate    fixedpoint.Value
	now        func() time.Time
	nextOrder  uint64
	nextTrade  uint64
	nextLedger uint64
	nextClosed uint64
}

type PaperLifecycleOption func(*paperLifecycleOptions)

type paperLifecycleOptions struct {
	balances types.BalanceMap
	feeRate  fixedpoint.Value
	now      func() time.Time
}

type PaperLedgerEntry struct {
	Sequence       uint64
	Time           time.Time
	Kind           string
	OrderID        uint64
	TradeID        uint64
	Currency       string
	AvailableDelta fixedpoint.Value
	LockedDelta    fixedpoint.Value
	Balance        types.Balance
}

type PaperClosedTrade struct {
	ID            uint64
	Symbol        string
	EntryOrderID  uint64
	ExitOrderID   uint64
	Quantity      fixedpoint.Value
	EntryNotional fixedpoint.Value
	ExitNotional  fixedpoint.Value
	ExitFee       fixedpoint.Value
	FeeCurrency   string
	RealizedPnL   fixedpoint.Value
	ClosedAt      time.Time
}

type PaperLedgerReconciliation struct {
	Starting types.BalanceMap
	Expected types.BalanceMap
	Actual   types.BalanceMap
	Drift    types.BalanceMap
}

type paperPositionLot struct {
	orderID  uint64
	quantity fixedpoint.Value
	cost     fixedpoint.Value
}

func WithPaperLifecycleBalances(balances types.BalanceMap) PaperLifecycleOption {
	return func(o *paperLifecycleOptions) {
		o.balances = copyPaperBalances(balances)
	}
}

func WithPaperLifecycleFeeRate(rate fixedpoint.Value) PaperLifecycleOption {
	return func(o *paperLifecycleOptions) {
		o.feeRate = rate
	}
}

func WithPaperLifecycleClock(now func() time.Time) PaperLifecycleOption {
	return func(o *paperLifecycleOptions) {
		if now != nil {
			o.now = now
		}
	}
}

func NewPaperLifecycleSession(ctx context.Context, cfg Config, source MarketData, options ...PaperLifecycleOption) (*PaperLifecycleSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cfg, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	switch cfg.Mode {
	case ModePaper, ModeReplay:
	default:
		return nil, fmt.Errorf("unsupported HTX paper lifecycle mode %q", cfg.Mode)
	}
	if source == nil {
		return nil, fmt.Errorf("HTX paper lifecycle requires market data source")
	}

	opts := paperLifecycleOptions{
		feeRate: defaultPaperTakerFeeRate,
		now: func() time.Time {
			return time.Now().UTC()
		},
	}
	for _, option := range options {
		option(&opts)
	}
	if opts.feeRate.Compare(defaultPaperTakerFeeRate) < 0 {
		return nil, fmt.Errorf("HTX paper lifecycle fee rate must not be below the conservative default")
	}

	markets, err := source.QueryMarkets(ctx)
	if err != nil {
		return nil, err
	}
	if len(markets) == 0 {
		return nil, fmt.Errorf("HTX paper lifecycle requires at least one market")
	}

	balances := copyPaperBalances(opts.balances)
	return &PaperLifecycleSession{
		cfg:        cfg,
		source:     source,
		markets:    cloneMarketMap(markets),
		balances:   balances,
		starting:   copyPaperBalances(balances),
		orders:     make(map[uint64]types.Order),
		trades:     make(map[uint64][]types.Trade),
		positions:  make(map[string][]paperPositionLot),
		feeRate:    opts.feeRate,
		now:        opts.now,
		nextOrder:  1,
		nextTrade:  1,
		nextLedger: 1,
		nextClosed: 1,
	}, nil
}

func (s *PaperLifecycleSession) Config() Config {
	if s == nil {
		return Config{}
	}
	return s.cfg
}

func (s *PaperLifecycleSession) QueryAccount(ctx context.Context) (*types.Account, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}

	account := types.NewAccount()
	account.UpdateBalances(copyPaperBalances(s.balances))
	return account, nil
}

func (s *PaperLifecycleSession) QueryAccountBalances(ctx context.Context) (types.BalanceMap, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	return copyPaperBalances(s.balances), nil
}

func (s *PaperLifecycleSession) SubmitOrder(ctx context.Context, order types.SubmitOrder) (OrderAck, error) {
	if err := s.ready(ctx); err != nil {
		return OrderAck{}, err
	}

	market, normalized, err := s.validateSubmitOrder(order)
	if err != nil {
		return OrderAck{}, err
	}
	order.Symbol = normalized
	order.Market = market

	orderID := s.nextOrder
	s.nextOrder++
	now := s.now().UTC()
	reserveCurrency, reserveAmount := paperReserveForOrder(market, order)
	if err := s.applyBalanceDelta(PaperLedgerSubmitLock, orderID, 0, now, reserveCurrency, reserveAmount.Neg(), reserveAmount); err != nil {
		return OrderAck{}, err
	}

	s.orders[orderID] = types.Order{
		SubmitOrder:      order,
		Exchange:         types.ExchangeName(CanonicalExchange),
		OrderID:          orderID,
		Status:           types.OrderStatusNew,
		OriginalStatus:   string(OrderStateSubmitted),
		ExecutedQuantity: fixedpoint.Zero,
		IsWorking:        true,
		CreationTime:     types.Time(now),
		UpdateTime:       types.Time(now),
	}
	return OrderAck{OrderID: strconv.FormatUint(orderID, 10)}, nil
}

func (s *PaperLifecycleSession) CancelOrder(ctx context.Context, orderID string) (OrderAck, error) {
	if err := s.ready(ctx); err != nil {
		return OrderAck{}, err
	}

	id, err := parsePaperOrderID(orderID)
	if err != nil {
		return OrderAck{}, err
	}
	order, ok := s.orders[id]
	if !ok {
		return OrderAck{}, fmt.Errorf("HTX paper order %s not found", orderID)
	}
	if !order.IsWorking || order.Status.Closed() {
		return OrderAck{}, fmt.Errorf("HTX paper order %s is already closed", orderID)
	}

	remaining := order.Quantity.Sub(order.ExecutedQuantity)
	if remaining.Sign() < 0 {
		return OrderAck{}, fmt.Errorf("HTX paper order %s has negative remaining quantity", orderID)
	}
	market, ok := s.markets[NormalizeSymbol(order.Symbol)]
	if !ok {
		return OrderAck{}, fmt.Errorf("HTX paper market %q is not configured", order.Symbol)
	}
	reserveCurrency, reserveAmount := paperReserveForRemaining(market, order.SubmitOrder, remaining)
	now := s.now().UTC()
	if reserveAmount.Sign() > 0 {
		if err := s.applyBalanceDelta(PaperLedgerCancel, id, 0, now, reserveCurrency, reserveAmount, reserveAmount.Neg()); err != nil {
			return OrderAck{}, err
		}
	}

	if order.ExecutedQuantity.Sign() > 0 {
		order.Status = types.OrderStatusPartiallyFilled
		order.OriginalStatus = string(OrderStatePartialCanceled)
	} else {
		order.Status = types.OrderStatusCanceled
		order.OriginalStatus = string(OrderStateCanceled)
	}
	order.IsWorking = false
	order.UpdateTime = types.Time(now)
	s.orders[id] = order
	return OrderAck{OrderID: strconv.FormatUint(id, 10)}, nil
}

func (s *PaperLifecycleSession) QueryOrder(ctx context.Context, orderID string) (types.Order, error) {
	if err := s.ready(ctx); err != nil {
		return types.Order{}, err
	}
	id, err := parsePaperOrderID(orderID)
	if err != nil {
		return types.Order{}, err
	}
	order, ok := s.orders[id]
	if !ok {
		return types.Order{}, fmt.Errorf("HTX paper order %s not found", orderID)
	}
	return order, nil
}

func (s *PaperLifecycleSession) QueryOrderTrades(ctx context.Context, orderID string) ([]types.Trade, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	id, err := parsePaperOrderID(orderID)
	if err != nil {
		return nil, err
	}
	trades := s.trades[id]
	out := make([]types.Trade, len(trades))
	copy(out, trades)
	return out, nil
}

func (s *PaperLifecycleSession) AdvanceKLines(ctx context.Context, symbol string, interval types.Interval, options types.KLineQueryOptions) ([]types.Trade, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	symbol = NormalizeSymbol(symbol)
	if symbol == "" {
		return nil, fmt.Errorf("HTX paper lifecycle symbol is empty")
	}
	if interval == "" {
		return nil, fmt.Errorf("HTX paper lifecycle interval is empty")
	}

	klines, err := s.source.QueryKLines(ctx, symbol, interval, options)
	if err != nil {
		return nil, err
	}

	fills := make([]types.Trade, 0)
	for _, kline := range klines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		trades, err := s.advanceKLine(kline)
		if err != nil {
			return nil, err
		}
		fills = append(fills, trades...)
	}
	return fills, nil
}

func (s *PaperLifecycleSession) Ledger() []PaperLedgerEntry {
	if s == nil {
		return nil
	}
	out := make([]PaperLedgerEntry, len(s.ledger))
	copy(out, s.ledger)
	return out
}

func (s *PaperLifecycleSession) ClosedTrades() []PaperClosedTrade {
	if s == nil {
		return nil
	}
	out := make([]PaperClosedTrade, len(s.closed))
	copy(out, s.closed)
	return out
}

func (s *PaperLifecycleSession) ReconcileLedger() (PaperLedgerReconciliation, error) {
	if s == nil {
		return PaperLedgerReconciliation{}, fmt.Errorf("HTX paper lifecycle session is nil")
	}

	expected := copyPaperBalances(s.starting)
	for _, entry := range s.ledger {
		applyPaperBalanceDelta(expected, entry.Currency, entry.AvailableDelta, entry.LockedDelta)
	}

	actual := copyPaperBalances(s.balances)
	drift := paperBalanceDrift(expected, actual)
	return PaperLedgerReconciliation{
		Starting: copyPaperBalances(s.starting),
		Expected: expected,
		Actual:   actual,
		Drift:    drift,
	}, nil
}

func (s *PaperLifecycleSession) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return fmt.Errorf("HTX paper lifecycle session is nil")
	}
	if s.source == nil {
		return fmt.Errorf("HTX paper lifecycle market data source is not initialized")
	}
	return nil
}

func (s *PaperLifecycleSession) validateSubmitOrder(order types.SubmitOrder) (types.Market, string, error) {
	symbol := NormalizeSymbol(order.Symbol)
	if symbol == "" {
		return types.Market{}, "", fmt.Errorf("HTX paper order symbol is empty")
	}
	market, ok := s.markets[symbol]
	if !ok {
		return types.Market{}, "", fmt.Errorf("HTX paper market %q is not configured", symbol)
	}
	if market.PricePrecision < 0 || market.VolumePrecision < 0 || market.TickSize.Sign() <= 0 || market.StepSize.Sign() <= 0 {
		return types.Market{}, "", fmt.Errorf("HTX paper market %q has invalid precision", symbol)
	}
	if order.MarginSideEffect != "" && order.MarginSideEffect != types.SideEffectTypeNoSideEffect {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q rejects margin side effects", symbol)
	}
	if order.ReduceOnly || order.ClosePosition {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q rejects futures-only flags", symbol)
	}
	if order.Side != types.SideTypeBuy && order.Side != types.SideTypeSell {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q has unsupported side %q", symbol, order.Side)
	}
	if order.Type != types.OrderTypeLimit {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q supports limit orders only", symbol)
	}
	if order.Price.Sign() <= 0 {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q price must be positive", symbol)
	}
	if order.Quantity.Sign() <= 0 {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q quantity must be positive", symbol)
	}
	if order.Price.Compare(market.TruncatePrice(order.Price)) != 0 {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q has invalid price precision", symbol)
	}
	if order.Quantity.Compare(market.TruncateQuantity(order.Quantity)) != 0 {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q has invalid quantity precision", symbol)
	}
	if market.MinQuantity.Sign() > 0 && order.Quantity.Compare(market.MinQuantity) < 0 {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q quantity is below minimum", symbol)
	}
	if market.MinNotional.Sign() > 0 && order.Price.Mul(order.Quantity).Compare(market.MinNotional) < 0 {
		return types.Market{}, "", fmt.Errorf("HTX paper order %q notional is below minimum", symbol)
	}
	return market, symbol, nil
}

func (s *PaperLifecycleSession) advanceKLine(kline types.KLine) ([]types.Trade, error) {
	symbol := NormalizeSymbol(kline.Symbol)
	if symbol == "" {
		return nil, fmt.Errorf("HTX paper kline symbol is empty")
	}
	if !kline.Closed {
		return nil, nil
	}

	ids := make([]uint64, 0, len(s.orders))
	for id, order := range s.orders {
		if order.IsWorking && NormalizeSymbol(order.Symbol) == symbol {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	fills := make([]types.Trade, 0, len(ids))
	for _, id := range ids {
		order := s.orders[id]
		if !paperOrderCrossesKLine(order, kline) {
			continue
		}
		trade, err := s.fillOrder(order, kline)
		if err != nil {
			return nil, err
		}
		fills = append(fills, trade)
	}
	return fills, nil
}

func (s *PaperLifecycleSession) fillOrder(order types.Order, kline types.KLine) (types.Trade, error) {
	market, ok := s.markets[NormalizeSymbol(order.Symbol)]
	if !ok {
		return types.Trade{}, fmt.Errorf("HTX paper market %q is not configured", order.Symbol)
	}

	quantity := order.Quantity.Sub(order.ExecutedQuantity)
	if quantity.Sign() <= 0 {
		return types.Trade{}, fmt.Errorf("HTX paper order %d has no fillable quantity", order.OrderID)
	}
	price := order.Price
	notional := price.Mul(quantity)
	fillTime := kline.EndTime.Time().UTC()
	if fillTime.IsZero() {
		fillTime = kline.StartTime.Time().UTC()
	}

	tradeID := s.nextTrade
	s.nextTrade++
	trade := types.Trade{
		ID:            tradeID,
		OrderID:       order.OrderID,
		Exchange:      types.ExchangeName(CanonicalExchange),
		Price:         price,
		Quantity:      quantity,
		QuoteQuantity: notional,
		Symbol:        NormalizeSymbol(order.Symbol),
		Side:          order.Side,
		IsBuyer:       order.Side == types.SideTypeBuy,
		Time:          types.Time(fillTime),
	}

	switch order.Side {
	case types.SideTypeBuy:
		fee := quantity.Mul(s.feeRate)
		netQuantity := quantity.Sub(fee)
		if netQuantity.Sign() <= 0 {
			return types.Trade{}, fmt.Errorf("HTX paper buy order %d fee exceeds filled quantity", order.OrderID)
		}
		trade.Fee = fee
		trade.FeeCurrency = market.BaseCurrency
		if err := s.applyBalanceDelta(PaperLedgerFillDebit, order.OrderID, tradeID, fillTime, market.QuoteCurrency, fixedpoint.Zero, notional.Neg()); err != nil {
			return types.Trade{}, err
		}
		if err := s.applyBalanceDelta(PaperLedgerFillCredit, order.OrderID, tradeID, fillTime, market.BaseCurrency, netQuantity, fixedpoint.Zero); err != nil {
			return types.Trade{}, err
		}
		s.positions[trade.Symbol] = append(s.positions[trade.Symbol], paperPositionLot{
			orderID:  order.OrderID,
			quantity: netQuantity,
			cost:     notional,
		})

	case types.SideTypeSell:
		fee := notional.Mul(s.feeRate)
		netProceeds := notional.Sub(fee)
		if netProceeds.Sign() < 0 {
			return types.Trade{}, fmt.Errorf("HTX paper sell order %d fee exceeds proceeds", order.OrderID)
		}
		trade.Fee = fee
		trade.FeeCurrency = market.QuoteCurrency
		if err := s.applyBalanceDelta(PaperLedgerFillDebit, order.OrderID, tradeID, fillTime, market.BaseCurrency, fixedpoint.Zero, quantity.Neg()); err != nil {
			return types.Trade{}, err
		}
		if err := s.applyBalanceDelta(PaperLedgerFillCredit, order.OrderID, tradeID, fillTime, market.QuoteCurrency, netProceeds, fixedpoint.Zero); err != nil {
			return types.Trade{}, err
		}
		s.closePositionLots(trade, fee)
	}

	order.ExecutedQuantity = order.Quantity
	order.Status = types.OrderStatusFilled
	order.OriginalStatus = string(OrderStateFilled)
	order.IsWorking = false
	order.UpdateTime = types.Time(fillTime)
	s.orders[order.OrderID] = order
	s.trades[order.OrderID] = append(s.trades[order.OrderID], trade)
	return trade, nil
}

func (s *PaperLifecycleSession) closePositionLots(trade types.Trade, exitFee fixedpoint.Value) {
	lots := s.positions[trade.Symbol]
	if len(lots) == 0 {
		return
	}

	remaining := trade.Quantity
	kept := lots[:0]
	for _, lot := range lots {
		if remaining.Sign() <= 0 {
			kept = append(kept, lot)
			continue
		}

		closeQuantity := remaining
		if lot.quantity.Compare(closeQuantity) < 0 {
			closeQuantity = lot.quantity
		}
		allocatedCost := lot.cost
		if closeQuantity.Compare(lot.quantity) < 0 {
			allocatedCost = lot.cost.Mul(closeQuantity).Div(lot.quantity)
		}
		exitNotional := trade.Price.Mul(closeQuantity)
		allocatedFee := exitFee
		if closeQuantity.Compare(trade.Quantity) < 0 {
			allocatedFee = exitFee.Mul(closeQuantity).Div(trade.Quantity)
		}

		s.closed = append(s.closed, PaperClosedTrade{
			ID:            s.nextClosed,
			Symbol:        trade.Symbol,
			EntryOrderID:  lot.orderID,
			ExitOrderID:   trade.OrderID,
			Quantity:      closeQuantity,
			EntryNotional: allocatedCost,
			ExitNotional:  exitNotional,
			ExitFee:       allocatedFee,
			FeeCurrency:   trade.FeeCurrency,
			RealizedPnL:   exitNotional.Sub(allocatedFee).Sub(allocatedCost),
			ClosedAt:      trade.Time.Time(),
		})
		s.nextClosed++

		lot.quantity = lot.quantity.Sub(closeQuantity)
		lot.cost = lot.cost.Sub(allocatedCost)
		remaining = remaining.Sub(closeQuantity)
		if lot.quantity.Sign() > 0 {
			kept = append(kept, lot)
		}
	}
	s.positions[trade.Symbol] = kept
}

func (s *PaperLifecycleSession) applyBalanceDelta(kind string, orderID uint64, tradeID uint64, at time.Time, currency string, availableDelta fixedpoint.Value, lockedDelta fixedpoint.Value) error {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		return fmt.Errorf("HTX paper ledger currency is empty")
	}

	next := applyPaperBalanceDelta(copyPaperBalances(s.balances), currency, availableDelta, lockedDelta)
	balance := next[currency]
	if balance.Available.Sign() < 0 || balance.Locked.Sign() < 0 {
		return fmt.Errorf("HTX paper ledger %s would make %s balance negative", kind, currency)
	}

	s.balances = next
	s.ledger = append(s.ledger, PaperLedgerEntry{
		Sequence:       s.nextLedger,
		Time:           at.UTC(),
		Kind:           kind,
		OrderID:        orderID,
		TradeID:        tradeID,
		Currency:       currency,
		AvailableDelta: availableDelta,
		LockedDelta:    lockedDelta,
		Balance:        balance,
	})
	s.nextLedger++
	return nil
}

func paperOrderCrossesKLine(order types.Order, kline types.KLine) bool {
	switch order.Side {
	case types.SideTypeBuy:
		return kline.Low.Compare(order.Price) <= 0
	case types.SideTypeSell:
		return kline.High.Compare(order.Price) >= 0
	default:
		return false
	}
}

func paperReserveForOrder(market types.Market, order types.SubmitOrder) (string, fixedpoint.Value) {
	return paperReserveForRemaining(market, order, order.Quantity)
}

func paperReserveForRemaining(market types.Market, order types.SubmitOrder, remaining fixedpoint.Value) (string, fixedpoint.Value) {
	switch order.Side {
	case types.SideTypeBuy:
		return market.QuoteCurrency, order.Price.Mul(remaining)
	case types.SideTypeSell:
		return market.BaseCurrency, remaining
	default:
		return "", fixedpoint.Zero
	}
}

func parsePaperOrderID(orderID string) (uint64, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return 0, fmt.Errorf("HTX paper order id is empty")
	}
	id, err := strconv.ParseUint(orderID, 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("HTX paper order id %q is invalid", orderID)
	}
	return id, nil
}

func copyPaperBalances(balances types.BalanceMap) types.BalanceMap {
	out := make(types.BalanceMap, len(balances))
	for key, balance := range balances {
		currency := strings.ToUpper(strings.TrimSpace(balance.Currency))
		if currency == "" {
			currency = strings.ToUpper(strings.TrimSpace(key))
		}
		if currency == "" {
			continue
		}
		balance.Currency = currency
		out[currency] = balance
	}
	return out
}

func applyPaperBalanceDelta(balances types.BalanceMap, currency string, availableDelta fixedpoint.Value, lockedDelta fixedpoint.Value) types.BalanceMap {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if balances == nil {
		balances = make(types.BalanceMap)
	}
	balance := balances[currency]
	balance.Currency = currency
	balance.Available = balance.Available.Add(availableDelta)
	balance.Locked = balance.Locked.Add(lockedDelta)
	balances[currency] = balance
	return balances
}

func paperBalanceDrift(expected types.BalanceMap, actual types.BalanceMap) types.BalanceMap {
	drift := types.BalanceMap{}
	currencies := make(map[string]struct{}, len(expected)+len(actual))
	for currency := range expected {
		currencies[currency] = struct{}{}
	}
	for currency := range actual {
		currencies[currency] = struct{}{}
	}
	for currency := range currencies {
		exp := expected[currency]
		act := actual[currency]
		available := act.Available.Sub(exp.Available)
		locked := act.Locked.Sub(exp.Locked)
		if available.Sign() != 0 || locked.Sign() != 0 {
			drift[currency] = types.Balance{
				Currency:  currency,
				Available: available,
				Locked:    locked,
			}
		}
	}
	return drift
}
