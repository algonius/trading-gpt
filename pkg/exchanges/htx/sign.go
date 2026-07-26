package htx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	signatureMethod  = "HmacSHA256"
	signatureVersion = "2"
	signatureKey     = "Signature"
	timestampFormat  = "2006-01-02T15:04:05"
)

type SigningRequest struct {
	Method      string
	Host        string
	Path        string
	Params      url.Values
	AccessKeyID string
	SecretKey   string
	Timestamp   time.Time
}

type SignedRequest struct {
	Params    url.Values
	Payload   string
	Signature string
}

func SignRESTRequest(req SigningRequest) (SignedRequest, error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	host := strings.ToLower(strings.TrimSpace(req.Host))
	path := strings.TrimSpace(req.Path)

	switch {
	case method == "":
		return SignedRequest{}, fmt.Errorf("HTX signing method is empty")
	case host == "":
		return SignedRequest{}, fmt.Errorf("HTX signing host is empty")
	case strings.Contains(host, "://"):
		return SignedRequest{}, fmt.Errorf("HTX signing host must not include scheme")
	case path == "":
		return SignedRequest{}, fmt.Errorf("HTX signing path is empty")
	case !strings.HasPrefix(path, "/"):
		return SignedRequest{}, fmt.Errorf("HTX signing path must start with slash")
	case strings.TrimSpace(req.AccessKeyID) == "":
		return SignedRequest{}, fmt.Errorf("HTX signing access key id is empty")
	case strings.TrimSpace(req.SecretKey) == "":
		return SignedRequest{}, fmt.Errorf("HTX signing secret key is empty")
	case req.Timestamp.IsZero():
		return SignedRequest{}, fmt.Errorf("HTX signing timestamp is empty")
	}

	params := cloneURLValues(req.Params)
	params.Del(signatureKey)
	params.Set("AccessKeyId", req.AccessKeyID)
	params.Set("SignatureMethod", signatureMethod)
	params.Set("SignatureVersion", signatureVersion)
	params.Set("Timestamp", req.Timestamp.UTC().Format(timestampFormat))

	payload := strings.Join([]string{
		method,
		host,
		path,
		params.Encode(),
	}, "\n")

	mac := hmac.New(sha256.New, []byte(req.SecretKey))
	_, _ = mac.Write([]byte(payload))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	signedParams := cloneURLValues(params)
	signedParams.Set(signatureKey, signature)
	return SignedRequest{
		Params:    signedParams,
		Payload:   payload,
		Signature: signature,
	}, nil
}

func cloneURLValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, vals := range values {
		copied := make([]string, len(vals))
		copy(copied, vals)
		cloned[key] = copied
	}
	return cloned
}
