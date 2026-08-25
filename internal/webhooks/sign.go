package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader is the value of X-Webhook-Signature:
// t=<unix_ts>,v1=<hex hmac-sha256 over "timestamp.body">.
const SignatureHeaderName = "X-Webhook-Signature"

// Sign computes the hex HMAC-SHA256 over "<timestamp>.<body>".
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignatureHeader builds the full header value.
func SignatureHeader(secret string, timestamp int64, body []byte) string {
	return fmt.Sprintf("t=%d,v1=%s", timestamp, Sign(secret, timestamp, body))
}

// Verify checks a signature header against the body, rejecting timestamps that
// skew more than tolerance from now. Provided so tests (and the docs' consumer
// samples) share one implementation.
func Verify(secret, header string, body []byte, tolerance time.Duration, now time.Time) bool {
	var tsStr, sig string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			tsStr = kv[1]
		case "v1":
			sig = kv[1]
		}
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || sig == "" {
		return false
	}
	skew := now.Unix() - ts
	if skew < 0 {
		skew = -skew
	}
	if time.Duration(skew)*time.Second > tolerance {
		return false
	}
	expected := Sign(secret, ts, body)
	return hmac.Equal([]byte(expected), []byte(sig))
}
