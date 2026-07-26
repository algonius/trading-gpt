package htx

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	dummyAccessKey = "fixture-access-key"
	dummySecretKey = "fixture-secret-key"
)

func TestSignRESTRequestOfficialStyleVector(t *testing.T) {
	timestamp := time.Date(2017, 5, 11, 16, 22, 6, 0, time.UTC)
	inputParams := url.Values{
		"order-id": {"1234567890"},
	}

	signed, err := SignRESTRequest(SigningRequest{
		Method:      "get",
		Host:        "API.HUOBI.PRO",
		Path:        "/v1/order/orders",
		Params:      inputParams,
		AccessKeyID: dummyAccessKey,
		SecretKey:   dummySecretKey,
		Timestamp:   timestamp,
	})
	if err != nil {
		t.Fatalf("SignRESTRequest returned error: %v", err)
	}

	expectedPayload := strings.Join([]string{
		"GET",
		"api.huobi.pro",
		"/v1/order/orders",
		"AccessKeyId=fixture-access-key&SignatureMethod=HmacSHA256&SignatureVersion=2&Timestamp=2017-05-11T16%3A22%3A06&order-id=1234567890",
	}, "\n")
	if signed.Payload != expectedPayload {
		t.Fatalf("payload = %q, want %q", signed.Payload, expectedPayload)
	}

	expectedSignature := "kt/+mZQfakZPUNWaHtHvRLlE3b7bxZls89yMdVPLBkA="
	if signed.Signature != expectedSignature {
		t.Fatalf("signature = %q, want fixture vector", signed.Signature)
	}
	if got := signed.Params.Get(signatureKey); got != expectedSignature {
		t.Fatalf("signed Signature param = %q, want fixture vector", got)
	}
	if strings.Contains(signed.Payload, signatureKey+"=") {
		t.Fatalf("payload must not include Signature param: %s", signed.Payload)
	}
	if inputParams.Get(signatureKey) != "" || inputParams.Get("AccessKeyId") != "" {
		t.Fatalf("input params were mutated: %s", inputParams.Encode())
	}

	encoded := signed.Params.Encode()
	if !strings.Contains(encoded, "Signature=kt%2F%2BmZQfakZPUNWaHtHvRLlE3b7bxZls89yMdVPLBkA%3D") {
		t.Fatalf("encoded signature missing or not URL-escaped: %s", encoded)
	}
}

func TestSignRESTRequestDeterministicAndNoSecretLeak(t *testing.T) {
	req := SigningRequest{
		Method:      "POST",
		Host:        "api.huobi.pro",
		Path:        "/v1/order/orders/place",
		Params:      url.Values{"account-id": {"100009"}, "amount": {"0.1"}, "symbol": {"btcusdt"}, "type": {"buy-limit"}},
		AccessKeyID: dummyAccessKey,
		SecretKey:   dummySecretKey,
		Timestamp:   time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC),
	}

	first, err := SignRESTRequest(req)
	if err != nil {
		t.Fatalf("first SignRESTRequest returned error: %v", err)
	}
	second, err := SignRESTRequest(req)
	if err != nil {
		t.Fatalf("second SignRESTRequest returned error: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("signatures are not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}

	_, err = SignRESTRequest(SigningRequest{
		Method:      "GET",
		Host:        "https://api.huobi.pro",
		Path:        "/v1/order/orders",
		AccessKeyID: dummyAccessKey,
		SecretKey:   dummySecretKey,
		Timestamp:   req.Timestamp,
	})
	if err == nil {
		t.Fatal("expected host with scheme to fail")
	}
	errText := err.Error()
	if strings.Contains(errText, dummyAccessKey) || strings.Contains(errText, dummySecretKey) {
		t.Fatalf("signing error leaked dummy credentials: %s", errText)
	}
}

func TestSignRESTRequestRejectsMissingInputs(t *testing.T) {
	valid := SigningRequest{
		Method:      "GET",
		Host:        "api.huobi.pro",
		Path:        "/v1/order/orders",
		AccessKeyID: dummyAccessKey,
		SecretKey:   dummySecretKey,
		Timestamp:   time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC),
	}

	tests := map[string]SigningRequest{
		"method":     {Host: valid.Host, Path: valid.Path, AccessKeyID: valid.AccessKeyID, SecretKey: valid.SecretKey, Timestamp: valid.Timestamp},
		"host":       {Method: valid.Method, Path: valid.Path, AccessKeyID: valid.AccessKeyID, SecretKey: valid.SecretKey, Timestamp: valid.Timestamp},
		"path":       {Method: valid.Method, Host: valid.Host, AccessKeyID: valid.AccessKeyID, SecretKey: valid.SecretKey, Timestamp: valid.Timestamp},
		"access key": {Method: valid.Method, Host: valid.Host, Path: valid.Path, SecretKey: valid.SecretKey, Timestamp: valid.Timestamp},
		"secret key": {Method: valid.Method, Host: valid.Host, Path: valid.Path, AccessKeyID: valid.AccessKeyID, Timestamp: valid.Timestamp},
		"timestamp":  {Method: valid.Method, Host: valid.Host, Path: valid.Path, AccessKeyID: valid.AccessKeyID, SecretKey: valid.SecretKey},
		"path slash": {Method: valid.Method, Host: valid.Host, Path: "v1/order/orders", AccessKeyID: valid.AccessKeyID, SecretKey: valid.SecretKey, Timestamp: valid.Timestamp},
	}

	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := SignRESTRequest(req)
			if err == nil {
				t.Fatal("expected signing request to fail")
			}
			if strings.Contains(err.Error(), dummyAccessKey) || strings.Contains(err.Error(), dummySecretKey) {
				t.Fatalf("signing error leaked dummy credentials: %s", err)
			}
		})
	}
}
