// Package webhook handles GitHub webhook ingestion: signature
// validation and payload parsing for workflow_job events.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HeaderSignature is GitHub's signature header. Format:
//
//	X-Hub-Signature-256: sha256=<hex hmac>
const HeaderSignature = "X-Hub-Signature-256"

// HeaderEvent identifies the event type (e.g. "workflow_job").
const HeaderEvent = "X-GitHub-Event"

// HeaderDelivery is the unique delivery ID — useful in logs.
const HeaderDelivery = "X-GitHub-Delivery"

// ValidateSignature returns true if `headerValue` is a valid HMAC-SHA256
// signature of `body` using `secret`. Constant-time comparison.
//
// `headerValue` is the literal `X-Hub-Signature-256` header value,
// i.e. starts with the "sha256=" prefix.
func ValidateSignature(headerValue string, body []byte, secret string) bool {
	if !strings.HasPrefix(headerValue, "sha256=") {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(headerValue, "sha256="))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	got := mac.Sum(nil)

	return hmac.Equal(want, got)
}
