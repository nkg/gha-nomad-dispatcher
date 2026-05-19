package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func validSigFor(t *testing.T, body []byte, secret string) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidateSignature(t *testing.T) {
	const secret = "swordfish"
	body := []byte(`{"action":"queued"}`)

	tests := []struct {
		name   string
		header string
		body   []byte
		secret string
		want   bool
	}{
		{
			name:   "valid signature",
			header: validSigFor(t, body, secret),
			body:   body,
			secret: secret,
			want:   true,
		},
		{
			name:   "missing prefix",
			header: hex.EncodeToString([]byte("not-a-real-sig")),
			body:   body,
			secret: secret,
			want:   false,
		},
		{
			name:   "wrong secret",
			header: validSigFor(t, body, "different-secret"),
			body:   body,
			secret: secret,
			want:   false,
		},
		{
			name:   "tampered body",
			header: validSigFor(t, body, secret),
			body:   []byte(`{"action":"completed"}`),
			secret: secret,
			want:   false,
		},
		{
			name:   "empty header",
			header: "",
			body:   body,
			secret: secret,
			want:   false,
		},
		{
			name:   "garbage hex",
			header: "sha256=not-hex-at-all-zz",
			body:   body,
			secret: secret,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateSignature(tt.header, tt.body, tt.secret)
			if got != tt.want {
				t.Errorf("ValidateSignature() = %v, want %v", got, tt.want)
			}
		})
	}
}
