package htx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/c9s/bbgo/pkg/fixedpoint"
	"github.com/c9s/bbgo/pkg/types"
)

const (
	PrivateAccountBalancePathPrefix = "/v1/account/accounts/"
	PrivateAccountBalancePathSuffix = "/balance"
	PrivateOrderPlacePath           = "/v1/order/orders/place"
	PrivateOrderPathPrefix          = "/v1/order/orders/"
	PrivateOrderCancelPathSuffix    = "/submitcancel"
	PrivateOrderTradesPathSuffix    = "/matchresults"

	DefaultPrivateMaxResponseBytes = int64(1 << 20)
	MaxPrivateRetryAttempts        = 3
)

const (
	privateHTTPStatusOK                  = 200
	privateHTTPStatusMultipleChoices     = 300
	privateHTTPStatusTooManyRequests     = 429
	privateHTTPStatusInternalServerError = 500
	privateHTTPStatusNetworkCeiling      = 600
)

type PrivateAction string

const (
	PrivateActionQueryAccountBalance PrivateAction = "query-account-balance"
	PrivateActionSubmitOrder         PrivateAction = "submit-order"
	PrivateActionCancelOrder         PrivateAction = "cancel-order"
	PrivateActionQueryOrder          PrivateAction = "query-order"
	PrivateActionQueryOrderTrades    PrivateAction = "query-order-trades"
)

type PrivateCredentials struct {
	AccessKeyID string
	SecretKey   string
}

type PrivateRequest struct {
	Action         PrivateAction
	Method         string
	Host           string
	Path           string
	Query          url.Values
	Body           []byte
	Endpoint       string
	SigningPayload string
	Signature      string
}

type PrivateResponse struct {
	StatusCode int
	Body       []byte
}

type PrivateTransport interface {
	RoundTrip(ctx context.Context, req PrivateRequest) (PrivateResponse, error)
}

type PrivateRetryClass string

const (
	PrivateRetryNone        PrivateRetryClass = "none"
	PrivateRetryTransient   PrivateRetryClass = "transient"
	PrivateRetryRateLimited PrivateRetryClass = "rate-limited"
)

func (c PrivateRetryClass) Retryable() bool {
	return c == PrivateRetryTransient || c == PrivateRetryRateLimited
}

type PrivateRetryPolicy struct {
	MaxAttempts int
}

type PrivateClient struct {
	baseURL          *url.URL
	credentials      PrivateCredentials
	transport        PrivateTransport
	now              func() time.Time
	maxResponseBytes int64
	retryPolicy      PrivateRetryPolicy
}

type PrivateClientOption func(*PrivateClient)

func WithPrivateClock(now func() time.Time) PrivateClientOption {
	return func(c *PrivateClient) {
		if now != nil {
			c.now = now
		}
	}
}

func WithPrivateMaxResponseBytes(limit int64) PrivateClientOption {
	return func(c *PrivateClient) {
		if limit > 0 {
			c.maxResponseBytes = limit
		}
	}
}

func WithPrivateRetryPolicy(policy PrivateRetryPolicy) PrivateClientOption {
	return func(c *PrivateClient) {
		c.retryPolicy = policy
	}
}

func NewPrivateClient(baseURL string, credentials PrivateCredentials, transport PrivateTransport, options ...PrivateClientOption) (*PrivateClient, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultRESTBaseURL
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid HTX private REST base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid HTX private REST base URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("invalid HTX private REST base URL host")
	}

	client := &PrivateClient{
		baseURL:          parsed,
		credentials:      credentials.normalized(),
		transport:        transport,
		now:              time.Now,
		maxResponseBytes: DefaultPrivateMaxResponseBytes,
		retryPolicy:      PrivateRetryPolicy{MaxAttempts: 1},
	}
	for _, option := range options {
		option(client)
	}
	if client.now == nil {
		client.now = time.Now
	}
	if client.maxResponseBytes <= 0 {
		client.maxResponseBytes = DefaultPrivateMaxResponseBytes
	}
	if err := client.ready(); err != nil {
		return nil, err
	}
	return client, nil
}

func (c *PrivateClient) BuildPrivateRequest(action PrivateAction, method string, path string, query url.Values, body []byte) (PrivateRequest, error) {
	if err := c.ready(); err != nil {
		return PrivateRequest{}, err
	}

	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	if !supportedPrivateRoute(action, method, path) {
		return PrivateRequest{}, fmt.Errorf("unsupported HTX private action/path: %s %s %s", action, method, path)
	}

	signed, err := SignRESTRequest(SigningRequest{
		Method:      method,
		Host:        c.baseURL.Host,
		Path:        path,
		Params:      cloneURLValues(query),
		AccessKeyID: c.credentials.AccessKeyID,
		SecretKey:   c.credentials.SecretKey,
		Timestamp:   c.now().UTC(),
	})
	if err != nil {
		return PrivateRequest{}, c.privateError(action, "signing failed: "+err.Error())
	}

	endpoint := *c.baseURL
	endpoint.Path = path
	endpoint.RawQuery = signed.Params.Encode()

	return PrivateRequest{
		Action:         action,
		Method:         method,
		Host:           strings.ToLower(c.baseURL.Host),
		Path:           path,
		Query:          cloneURLValues(signed.Params),
		Body:           append([]byte(nil), body...),
		Endpoint:       endpoint.String(),
		SigningPayload: signed.Payload,
		Signature:      signed.Signature,
	}, nil
}

func (c *PrivateClient) QueryAccount(ctx context.Context, accountID string) (*types.Account, error) {
	balances, err := c.QueryAccountBalances(ctx, accountID)
	if err != nil {
		return nil, err
	}

	account := types.NewAccount()
	account.AccountType = types.AccountTypeSpot
	account.UpdateBalances(balances)
	return account, nil
}

func (c *PrivateClient) QueryAccountBalances(ctx context.Context, accountID string) (types.BalanceMap, error) {
	accountID, err := cleanPrivatePathID("account id", accountID)
	if err != nil {
		return nil, err
	}

	body, err := c.do(ctx, PrivateActionQueryAccountBalance, "GET", PrivateAccountBalancePathPrefix+accountID+PrivateAccountBalancePathSuffix, nil, nil)
	if err != nil {
		return nil, err
	}
	return ParseAccountBalanceResponse(bytes.NewReader(body))
}

func (c *PrivateClient) SubmitOrder(ctx context.Context, accountID string, order types.SubmitOrder) (OrderAck, error) {
	requestBody, err := marshalSubmitOrderBody(accountID, order)
	if err != nil {
		return OrderAck{}, err
	}

	body, err := c.do(ctx, PrivateActionSubmitOrder, "POST", PrivateOrderPlacePath, nil, requestBody)
	if err != nil {
		return OrderAck{}, err
	}
	return ParseSubmitOrderResponse(bytes.NewReader(body))
}

func (c *PrivateClient) CancelOrder(ctx context.Context, orderID string) (OrderAck, error) {
	orderID, err := cleanPrivatePathID("order id", orderID)
	if err != nil {
		return OrderAck{}, err
	}

	body, err := c.do(ctx, PrivateActionCancelOrder, "POST", PrivateOrderPathPrefix+orderID+PrivateOrderCancelPathSuffix, nil, nil)
	if err != nil {
		return OrderAck{}, err
	}
	return ParseCancelOrderResponse(bytes.NewReader(body))
}

func (c *PrivateClient) QueryOrder(ctx context.Context, orderID string) (types.Order, error) {
	orderID, err := cleanPrivatePathID("order id", orderID)
	if err != nil {
		return types.Order{}, err
	}

	body, err := c.do(ctx, PrivateActionQueryOrder, "GET", PrivateOrderPathPrefix+orderID, nil, nil)
	if err != nil {
		return types.Order{}, err
	}
	return ParseOrderResponse(bytes.NewReader(body))
}

func (c *PrivateClient) QueryOrderTrades(ctx context.Context, orderID string) ([]types.Trade, error) {
	orderID, err := cleanPrivatePathID("order id", orderID)
	if err != nil {
		return nil, err
	}

	body, err := c.do(ctx, PrivateActionQueryOrderTrades, "GET", PrivateOrderPathPrefix+orderID+PrivateOrderTradesPathSuffix, nil, nil)
	if err != nil {
		return nil, err
	}
	return ParseOrderTrades(bytes.NewReader(body))
}

func ParseAccountBalanceResponse(r io.Reader) (types.BalanceMap, error) {
	var resp accountBalanceResponse
	if err := json.NewDecoder(r).Decode(&resp); err != nil {
		return nil, err
	}
	if err := checkOrderResponseStatus(resp.Status, resp.ErrCode, resp.ErrMsg, "account balance"); err != nil {
		return nil, err
	}

	balances := make(types.BalanceMap)
	for _, item := range resp.Data.List {
		currency := strings.ToUpper(strings.TrimSpace(item.Currency))
		if currency == "" {
			return nil, fmt.Errorf("HTX account balance currency is empty")
		}
		if item.Balance.Sign() < 0 {
			return nil, fmt.Errorf("HTX account balance %s amount is negative", currency)
		}

		balance := balances[currency]
		balance.Currency = currency
		switch strings.ToLower(strings.TrimSpace(item.Type)) {
		case "trade":
			balance.Available = balance.Available.Add(item.Balance)
		case "frozen":
			balance.Locked = balance.Locked.Add(item.Balance)
		default:
			return nil, fmt.Errorf("unsupported HTX account balance type %q", item.Type)
		}
		balances[currency] = balance
	}

	for currency, balance := range balances {
		balance.NetAsset = balance.Total()
		balances[currency] = balance
	}
	return balances, nil
}

func ClassifyPrivateRetry(statusCode int, body []byte, transportErr error) PrivateRetryClass {
	if transportErr != nil {
		return PrivateRetryTransient
	}
	if statusCode == privateHTTPStatusTooManyRequests {
		return PrivateRetryRateLimited
	}
	if statusCode >= privateHTTPStatusInternalServerError && statusCode < privateHTTPStatusNetworkCeiling {
		return PrivateRetryTransient
	}

	class := classifyHTXRetryBody(body)
	if class.Retryable() {
		return class
	}
	return PrivateRetryNone
}

func (c *PrivateClient) do(ctx context.Context, action PrivateAction, method string, path string, query url.Values, body []byte) ([]byte, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	req, err := c.BuildPrivateRequest(action, method, path, query, body)
	if err != nil {
		return nil, err
	}

	attempts := 1
	if readOnlyPrivateAction(action) {
		attempts = c.retryPolicy.attempts()
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, transportErr := c.transport.RoundTrip(ctx, req.clone())
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		responseBody := append([]byte(nil), resp.Body...)
		if transportErr == nil {
			if err := checkPrivateResponseBound(responseBody, c.maxResponseBytes); err != nil {
				return nil, err
			}
		}

		retryClass := ClassifyPrivateRetry(resp.StatusCode, responseBody, transportErr)
		if retryClass.Retryable() && attempt < attempts {
			continue
		}

		if transportErr != nil {
			return nil, c.privateError(action, "transport failed: "+transportErr.Error())
		}
		if resp.StatusCode < privateHTTPStatusOK || resp.StatusCode >= privateHTTPStatusMultipleChoices {
			return nil, c.privateError(action, fmt.Sprintf("returned HTTP %d: %s", resp.StatusCode, trimmedPrivateBody(responseBody)))
		}
		return responseBody, nil
	}

	return nil, c.privateError(action, "retry policy exhausted")
}

func (c *PrivateClient) ready() error {
	if c == nil {
		return fmt.Errorf("HTX private client is nil")
	}
	if c.baseURL == nil {
		return fmt.Errorf("HTX private client is not initialized")
	}
	if c.transport == nil {
		return fmt.Errorf("HTX private client transport is absent")
	}
	if err := c.credentials.validate(); err != nil {
		return err
	}
	return nil
}

func (c PrivateCredentials) normalized() PrivateCredentials {
	return PrivateCredentials{
		AccessKeyID: strings.TrimSpace(c.AccessKeyID),
		SecretKey:   strings.TrimSpace(c.SecretKey),
	}
}

func (c PrivateCredentials) validate() error {
	c = c.normalized()
	if c.AccessKeyID == "" {
		return fmt.Errorf("HTX private credentials access key id is empty")
	}
	if c.SecretKey == "" {
		return fmt.Errorf("HTX private credentials secret key is empty")
	}
	return nil
}

func (p PrivateRetryPolicy) attempts() int {
	if p.MaxAttempts <= 0 {
		return 1
	}
	if p.MaxAttempts > MaxPrivateRetryAttempts {
		return MaxPrivateRetryAttempts
	}
	return p.MaxAttempts
}

func readOnlyPrivateAction(action PrivateAction) bool {
	switch action {
	case PrivateActionQueryAccountBalance, PrivateActionQueryOrder, PrivateActionQueryOrderTrades:
		return true
	default:
		return false
	}
}

func supportedPrivateRoute(action PrivateAction, method string, path string) bool {
	switch action {
	case PrivateActionQueryAccountBalance:
		return method == "GET" && matchesPrivatePath(path, PrivateAccountBalancePathPrefix, PrivateAccountBalancePathSuffix)
	case PrivateActionSubmitOrder:
		return method == "POST" && path == PrivateOrderPlacePath
	case PrivateActionCancelOrder:
		return method == "POST" && matchesPrivatePath(path, PrivateOrderPathPrefix, PrivateOrderCancelPathSuffix)
	case PrivateActionQueryOrder:
		return method == "GET" && matchesPrivatePath(path, PrivateOrderPathPrefix, "")
	case PrivateActionQueryOrderTrades:
		return method == "GET" && matchesPrivatePath(path, PrivateOrderPathPrefix, PrivateOrderTradesPathSuffix)
	default:
		return false
	}
}

func matchesPrivatePath(path string, prefix string, suffix string) bool {
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	return id != "" && !strings.Contains(id, "/")
}

func cleanPrivatePathID(label string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("HTX private %s is empty", label)
	}
	if strings.ContainsAny(value, " \t\r\n/?#") {
		return "", fmt.Errorf("HTX private %s contains unsupported characters", label)
	}
	return value, nil
}

type submitOrderBody struct {
	AccountID     string `json:"account-id"`
	Symbol        string `json:"symbol"`
	Type          string `json:"type"`
	Amount        string `json:"amount"`
	Price         string `json:"price,omitempty"`
	ClientOrderID string `json:"client-order-id,omitempty"`
}

func marshalSubmitOrderBody(accountID string, order types.SubmitOrder) ([]byte, error) {
	accountID, err := cleanPrivatePathID("account id", accountID)
	if err != nil {
		return nil, err
	}

	symbol := NormalizeSymbol(order.Symbol)
	if symbol == "" {
		return nil, fmt.Errorf("HTX submit order symbol is empty")
	}
	if order.Quantity.Sign() <= 0 {
		return nil, fmt.Errorf("HTX submit order quantity must be positive")
	}

	orderType, err := htxSubmitOrderType(order.Side, order.Type)
	if err != nil {
		return nil, err
	}

	payload := submitOrderBody{
		AccountID:     accountID,
		Symbol:        strings.ToLower(symbol),
		Type:          orderType,
		Amount:        order.Quantity.String(),
		ClientOrderID: strings.TrimSpace(order.ClientOrderID),
	}
	if order.Type == types.OrderTypeMarket {
		if order.Price.Sign() > 0 {
			payload.Price = order.Price.String()
		}
	} else {
		if order.Price.Sign() <= 0 {
			return nil, fmt.Errorf("HTX submit order price must be positive")
		}
		payload.Price = order.Price.String()
	}

	return json.Marshal(payload)
}

func htxSubmitOrderType(side types.SideType, orderType types.OrderType) (string, error) {
	var sidePart string
	switch side {
	case types.SideTypeBuy:
		sidePart = "buy"
	case types.SideTypeSell:
		sidePart = "sell"
	default:
		return "", fmt.Errorf("unsupported HTX submit order side %q", side)
	}

	var typePart string
	switch orderType {
	case types.OrderTypeLimit:
		typePart = "limit"
	case types.OrderTypeLimitMaker:
		typePart = "limit-maker"
	case types.OrderTypeMarket:
		typePart = "market"
	default:
		return "", fmt.Errorf("unsupported HTX submit order type %q", orderType)
	}

	return sidePart + "-" + typePart, nil
}

type accountBalanceResponse struct {
	Status  string             `json:"status"`
	Data    accountBalanceData `json:"data"`
	ErrCode string             `json:"err-code"`
	ErrMsg  string             `json:"err-msg"`
}

type accountBalanceData struct {
	ID    int64                `json:"id"`
	Type  string               `json:"type"`
	State string               `json:"state"`
	List  []accountBalanceItem `json:"list"`
}

type accountBalanceItem struct {
	Currency string           `json:"currency"`
	Type     string           `json:"type"`
	Balance  fixedpoint.Value `json:"balance"`
}

type privateStatusEnvelope struct {
	Status  string `json:"status"`
	ErrCode string `json:"err-code"`
	ErrMsg  string `json:"err-msg"`
}

func classifyHTXRetryBody(body []byte) PrivateRetryClass {
	if len(body) == 0 {
		return PrivateRetryNone
	}

	var envelope privateStatusEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return PrivateRetryNone
	}
	if strings.EqualFold(strings.TrimSpace(envelope.Status), "ok") {
		return PrivateRetryNone
	}

	codeAndMessage := strings.ToLower(strings.TrimSpace(envelope.ErrCode + " " + envelope.ErrMsg))
	switch {
	case strings.Contains(codeAndMessage, "rate"), strings.Contains(codeAndMessage, "too many"):
		return PrivateRetryRateLimited
	case strings.Contains(codeAndMessage, "timeout"), strings.Contains(codeAndMessage, "temporar"), strings.Contains(codeAndMessage, "unavailable"):
		return PrivateRetryTransient
	default:
		return PrivateRetryNone
	}
}

func checkPrivateResponseBound(body []byte, limit int64) error {
	if limit <= 0 {
		return fmt.Errorf("HTX private response byte limit must be positive")
	}
	if int64(len(body)) > limit {
		return fmt.Errorf("HTX private response exceeds %d byte limit", limit)
	}
	return nil
}

func trimmedPrivateBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 256 {
		return text[:256] + "...(truncated)"
	}
	return text
}

func (c *PrivateClient) privateError(action PrivateAction, message string) error {
	message = strings.ReplaceAll(message, c.credentials.AccessKeyID, "[redacted-access-key]")
	message = strings.ReplaceAll(message, c.credentials.SecretKey, "[redacted-secret-key]")
	return fmt.Errorf("HTX private %s %s", action, message)
}

func (r PrivateRequest) clone() PrivateRequest {
	r.Query = cloneURLValues(r.Query)
	r.Body = append([]byte(nil), r.Body...)
	return r
}
